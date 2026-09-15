// sync.go 实现双向同步引擎：push（本地脏行 → Bitable 幂等 upsert + 软删墓碑）→
// pull（全量搜索 + last_modified_time 水位增量 → 自回声跳过 / 缺字段跳过告警 /
// 本地新行合入 / 双方同改 LWW 冲突登记后 Bitable 覆盖）。
//
// 回声与冲突的判定基准（关键设计）：
//   - 回声：远端内容指纹 == 本行当前 bitable_synced_hash。push 回写后 synced_hash
//     即为刚推送的内容，故 push 后的搜索回读必然命中回声，不会重复合入；
//   - 冲突：push 会先把本地脏行推送干净，若冲突只比对"当前 synced_hash"则永远不可达。
//     因此 push 前对每行快照"祖先指纹"（本轮 push 前的 synced_hash），pull 时双方
//     内容指纹均 ≠ 祖先 ⇒ 双方在共同基线之后都改过 ⇒ 记 sync_conflict（含双方 JSON）
//     并按 Bitable 覆盖本地（记录级 LWW）。
package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// SyncResult 汇总一次同步的成果；Warnings 收集非致命告警（缺字段跳过、单行推送失败等）。
type SyncResult struct {
	Pushed, Pulled, SkippedEcho, Deprecated int
	Conflicts                               []string // 如 "task#12 本地有未同步修改，已被 Bitable 侧覆盖(LWW)"
	Warnings                                []string
}

// —— sync_state 键约定与读写封装 ————————————————————————————————————————————

// watermarkKey 是某项目某张表的 pull 水位键（已处理过的最大 last_modified_time）。
func watermarkKey(projectID int64, table string) string {
	return fmt.Sprintf("pull_watermark:%d:%s", projectID, table)
}

// lastPullKey 是某项目最近一次成功 pull 的时间戳键。
func lastPullKey(projectID int64) string {
	return fmt.Sprintf("last_pull:%d", projectID)
}

// LastPullKey 导出 last_pull 键名约定，供 CLI 报表 stale 检查读取。
func LastPullKey(projectID int64) string { return lastPullKey(projectID) }

// pendingRecordKey 是"远端记录已建、本地回填尚未完成"的过渡态键：RecordCreate 成功后
// 先把取得的 record_id 记到这里再回填行内元数据；回填因任何原因中断时，下一轮 push
// 与 pull 都依据它复用同一条远端记录，保证重跑幂等（不在远端/本地产生重复行）。
func pendingRecordKey(entity string, id int64) string {
	return fmt.Sprintf("pending_record:%s:%d", entity, id)
}

// capWatermark 把本轮水位上限压到最早失败记录之前：失败记录的合入下轮必须重试
// （成功合入的记录重处理时会被回声判定跳过，代价可忽略）。
func capWatermark(maxLMT, minFailedLMT int64) int64 {
	if minFailedLMT > 0 && minFailedLMT-1 < maxLMT {
		return minFailedLMT - 1
	}
	return maxLMT
}

// GetSyncState 读取 sync_state（store 同名方法的包内封装）。
func GetSyncState(s *store.Store, key string) (string, bool, error) { return s.GetSyncState(key) }

// SetSyncState 写入 sync_state（store 同名方法的包内封装）。
func SetSyncState(s *store.Store, key, val string) error { return s.SetSyncState(key, val) }

// MarkSynced 只回写 bitable_synced_hash（记录 record_id 不变时使用）。
func MarkSynced(s *store.Store, entity string, id int64, hash string) error {
	return s.MarkSyncedHash(entity, id, hash)
}

// MarkSyncedRecord 回写 bitable_record_id + bitable_synced_hash（push 新建回填、
// pull 合入链接远端记录时使用）。
func MarkSyncedRecord(s *store.Store, entity string, id int64, recordID, hash string) error {
	return s.MarkSyncedRecord(entity, id, recordID, hash)
}

// SyncProject 对单个项目执行完整同步：push（版本→任务→六实体）→ pull（版本→任务→六实体）。
// 六实体表 id 取自 projects.feishu_tables_json（旧项目/采用既有 base 的 bind 未配置时
// 为零值，六实体整体跳过，行为与 v1.0 一致）。单行推送失败不中断（记 Warnings 继续
// 补推其余行，dirty 标记保留待下轮重试）；pull 的搜索失败视为本轮失败返回错误（幂等可重跑）。
func SyncProject(ctx context.Context, c *Client, s *store.Store, p model.Project, actor model.Member) (SyncResult, error) {
	res := SyncResult{}
	ss := &syncSession{
		ctx: ctx, api: c.API(), s: s, p: p, actor: actor, res: &res,
		idToName: map[int64]string{}, nameToID: map[string]int64{},
		verIDToName: map[int64]string{}, verNameToID: map[string]int64{},
		entityAncestors: map[string]map[int64]string{},
	}
	if err := ss.loadMaps(); err != nil {
		return res, err
	}
	tables, err := s.GetFeishuTables(p.ID)
	if err != nil {
		return res, err
	}
	// 冲突判定的共同基线必须在 push 之前快照：push 会把脏行的 synced_hash 改成
	// 刚推送的内容，之后再读就丢失了"本地有未同步修改"这一事实。
	if err := ss.snapshotAncestors(); err != nil {
		return res, err
	}
	for _, step := range entityAncestorSteps(ss, tables) {
		if err := step(); err != nil {
			return res, err
		}
	}
	if err := ss.pushVersions(); err != nil {
		return res, err
	}
	if tables.Members != "" { // 成员表单向镜像：未配置（旧项目）时跳过
		if err := ss.pushMembers(tables.Members); err != nil {
			return res, err
		}
	}
	if err := ss.pushTasks(); err != nil {
		return res, err
	}
	for _, step := range entityPushSteps(ss, tables) {
		if err := step(); err != nil {
			return res, err
		}
	}
	if err := ss.pullVersions(); err != nil {
		return res, err
	}
	if err := ss.pullTasks(); err != nil {
		return res, err
	}
	for _, step := range entityPullSteps(ss, tables) {
		if err := step(); err != nil {
			return res, err
		}
	}
	if err := SetSyncState(s, lastPullKey(p.ID), strconv.FormatInt(time.Now().Unix(), 10)); err != nil {
		return res, fmt.Errorf("记录 last_pull 失败: %w", err)
	}
	return res, nil
}

// syncSession 聚合一次同步的上下文与共享映射（成员/版本名↔ID 在合入过程中会增长，
// 由 ensureMember 等方法同步维护）。
type syncSession struct {
	ctx   context.Context
	api   bitableAPI
	s     *store.Store
	p     model.Project
	actor model.Member
	res   *SyncResult

	idToName    map[int64]string
	nameToID    map[string]int64
	verIDToName map[int64]string
	verNameToID map[string]int64

	taskAncestor    map[int64]string // 本轮 push 前的 synced_hash 快照（LWW 共同基线）
	versionAncestor map[int64]string
	// 六实体的祖先指纹快照，按实体名索引（"requirement" 等）；
	// 仅 feishu_tables_json 已配置对应表的实体才有快照。
	entityAncestors map[string]map[int64]string
}

// snapshotAncestors 在 push 之前记下每行的 synced_hash，作为 pull 冲突判定的共同基线。
func (ss *syncSession) snapshotAncestors() error {
	tasks, err := ss.s.ListTasks(ss.p.ID, store.TaskFilter{IncludeArchived: true})
	if err != nil {
		return fmt.Errorf("加载任务失败: %w", err)
	}
	ss.taskAncestor = make(map[int64]string, len(tasks))
	for _, t := range tasks {
		ss.taskAncestor[t.ID] = t.BitableSyncedHash
	}
	versions, err := ss.s.ListVersions(ss.p.ID)
	if err != nil {
		return fmt.Errorf("加载版本失败: %w", err)
	}
	ss.versionAncestor = make(map[int64]string, len(versions))
	for _, v := range versions {
		ss.versionAncestor[v.ID] = v.BitableSyncedHash
	}
	return nil
}

// loadMaps 从 store 构建成员与版本的查找映射。
func (ss *syncSession) loadMaps() error {
	members, err := ss.s.ListMembers()
	if err != nil {
		return fmt.Errorf("加载成员失败: %w", err)
	}
	for _, m := range members {
		ss.idToName[m.ID] = m.Name
		ss.nameToID[m.Name] = m.ID
	}
	versions, err := ss.s.ListVersions(ss.p.ID)
	if err != nil {
		return fmt.Errorf("加载版本失败: %w", err)
	}
	for _, v := range versions {
		ss.verIDToName[v.ID] = v.Name
		ss.verNameToID[v.Name] = v.ID
	}
	return nil
}

// ensureMember 取回（或创建）成员并维护双向映射；远端提到的成员名本地不存在时按 human 创建。
func (ss *syncSession) ensureMember(name string) (int64, error) {
	if id, ok := ss.nameToID[name]; ok {
		return id, nil
	}
	m, err := ss.s.GetOrCreateMember(name, "human")
	if err != nil {
		return 0, fmt.Errorf("get-or-create member %q: %w", name, err)
	}
	ss.nameToID[m.Name] = m.ID
	ss.idToName[m.ID] = m.Name
	return m.ID, nil
}

// warn 收录一条非致命告警。
func (ss *syncSession) warn(format string, args ...any) {
	ss.res.Warnings = append(ss.res.Warnings, fmt.Sprintf(format, args...))
}

// storeTimeLayout 与 store 落库的时间文本格式一致（UTC，无时区后缀）。
const storeTimeLayout = "2006-01-02 15:04:05"

// versionFields 是 VersionToFields 的会话包装：附带按 tasks 实时计算的版本进度
// （任务数/已完成/逾期/完成度），使版本表在飞书侧成为"活"的版本规划视图。
func (ss *syncSession) versionFields(v model.Version) map[string]any {
	prog, err := ss.s.VersionProgress(ss.p.ID, v.ID)
	if err != nil {
		prog = store.VersionProgress{} // 统计失败按零值推送，不阻塞同步
	}
	return VersionToFields(v, prog)
}

// warnRecentLocalOverwrite 是 LWW 覆盖的 UX 增强（E2E-3，不改变合并语义）：
// 双机 autopush 场景下后写者赢，被覆盖方在 sync 输出中原本毫无感知。判定基准是
// synced_at（本行上次与飞书收敛的时刻，随 bitable_synced_hash 同点写入）：远端记录
// 的 last_modified_time 晚于它，说明远端在我上次同步之后又变过——本次覆盖丢弃的是
// 我已同步到飞书的成果，补一条显式提示。（不可用 pull 水位比较：autopush 的内嵌
// pull 会把水位推进到晚于本地编辑时刻，水位比较在真实主路径上永不触发。）
// synced_at 为空（元数据未回填：新拉行中断、旧库升级遗留）或不可解析、远端 lmt
// 不晚于它时静默跳过（启发式宁缺毋滥）。版本表无 synced_at，该提示仅适用于任务。
func (ss *syncSession) warnRecentLocalOverwrite(lmt int64, t model.Task) {
	if lmt <= 0 || t.SyncedAt == "" {
		return
	}
	ts, err := time.Parse(storeTimeLayout, t.SyncedAt)
	if err != nil || lmt <= ts.Unix() {
		return
	}
	ss.warn("任务 #%d %s 被飞书侧更新覆盖（覆盖的是你已同步到飞书的修改）", t.ID, t.Title)
}

// getWatermark 读取上轮 pull 水位（无记录/解析失败按 0 = 首轮全量）。
func (ss *syncSession) getWatermark(table string) int64 {
	raw, found, err := GetSyncState(ss.s, watermarkKey(ss.p.ID, table))
	if err != nil || !found {
		return 0
	}
	n, _ := strconv.ParseInt(raw, 10, 64)
	return n
}

// setWatermark 单调推进水位（只在增大时写，避免回退漏处理）。
func (ss *syncSession) setWatermark(table string, next int64) {
	if next <= ss.getWatermark(table) {
		return
	}
	_ = SetSyncState(ss.s, watermarkKey(ss.p.ID, table), strconv.FormatInt(next, 10))
}

// failureTracker 收集本轮合入失败记录的最早 last_modified_time，供水位封顶。
type failureTracker struct{ minLMT int64 }

// note 记一条失败记录的水位（0 值 lmt 无法参与比较，忽略）。
func (f *failureTracker) note(lmt int64) {
	if lmt > 0 && (f.minLMT == 0 || lmt < f.minLMT) {
		f.minLMT = lmt
	}
}

// resolvePushRecordID 返回该行应使用的远端 record_id：优先行内已回填值，
// 其次回填中断时记录在 sync_state 的 pending id（重跑据此复用同一远端记录）。
func (ss *syncSession) resolvePushRecordID(entity string, id int64, recordID string) string {
	if recordID != "" {
		return recordID
	}
	if pending, found, err := GetSyncState(ss.s, pendingRecordKey(entity, id)); err == nil && found {
		return pending // 可能为空串：无 pending
	}
	return ""
}

// markPulled 在本地合入完成后回填同步元数据：先记 pending id（回填中断时 push/pull
// 仍能按 id 复用同一远端记录），回填失败降级为告警并返回 false——本轮继续、
// 该记录经水位封顶在下轮重试，不再中止整轮同步。
func (ss *syncSession) markPulled(entity string, id int64, recordID, hash string) bool {
	if err := SetSyncState(ss.s, pendingRecordKey(entity, id), recordID); err != nil {
		ss.warn("记录 %s#%d 的 pending record_id 失败: %v", entity, id, err)
	}
	if err := MarkSyncedRecord(ss.s, entity, id, recordID, hash); err != nil {
		ss.warn("回填 %s#%d 的同步元数据失败（下轮将重试合入）: %v", entity, id, err)
		return false
	}
	_ = SetSyncState(ss.s, pendingRecordKey(entity, id), "") // 清除 pending；失败无害（仅在 record_id 为空时才查）
	return true
}

// timeoutCreateReconcile 是 RecordCreate 超时防重复核对（REAL-2）：创建请求超时类
// 失败时服务端可能已成功写入，客户端却拿不到 record_id，盲目重试会建重复行。
// 立即对该表做一次 RecordSearch，对每条候选记录先经 normalize 归一化再按 ContentHash
// 精确比对（fields 是本次 push 的归一化字段）→ 命中返回其 record_id 供正常回填；
// 未命中或核对本身失败返回空串（维持原失败语义：告警 + dirty 保留）。
// 非超时错误不触发核对（调用方保证）。normalize 必须与 push/echo 哈希同构——真实租户
// text 列读回是富文本数组，原始 fields 直接哈希恒不匹配（调用点按实体传入与 pull 回声
// 判定同一条 fromFields→toFields 往返；成员表无反向适配器，退化为逐值 flattenRichText）。
func (ss *syncSession) timeoutCreateReconcile(appToken, tableID string, fields map[string]any, normalize func(map[string]any) map[string]any) string {
	recs, err := ss.api.RecordSearch(ss.ctx, appToken, tableID)
	if err != nil {
		ss.warn("创建超时后核对 %s 失败（恢复后可补推）: %v", tableID, err)
		return ""
	}
	want := ContentHash(fields)
	for _, rec := range recs {
		if ContentHash(normalize(rec.Fields)) == want {
			return rec.RecordID
		}
	}
	return ""
}

// flattenFields 对 fields 逐值 flattenRichText 后浅拷贝：无 fromFields 反向适配器的表
// （成员单向镜像）的超时核对归一化，使 text 列读回的富文本数组与 push 的裸值同构。
func flattenFields(f map[string]any) map[string]any {
	out := make(map[string]any, len(f))
	for k, v := range f {
		out[k] = flattenRichText(v)
	}
	return out
}

// —— push ————————————————————————————————————————————————————————————————

// pushVersions 推送本地版本脏行：无 record_id 建记录并回填，否则更新；软删不适用版本表。
func (ss *syncSession) pushVersions() error {
	versions, err := ss.s.ListVersions(ss.p.ID)
	if err != nil {
		return fmt.Errorf("加载版本失败: %w", err)
	}
	for _, v := range versions {
		fields := ss.versionFields(v)
		hash := ContentHash(fields)
		if hash == v.BitableSyncedHash {
			continue // 与上次同步一致，免调用
		}
		recID := ss.resolvePushRecordID("version", v.ID, v.BitableRecordID)
		if recID == "" {
			recID, err = ss.api.RecordCreate(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuVersionTableID, fields)
			if err != nil && isTimeoutErr(err) { // 超时防重复（REAL-2）：服务端可能已写入，按内容核对一次
				prog, progErr := ss.s.VersionProgress(ss.p.ID, v.ID)
				if progErr != nil {
					prog = store.VersionProgress{}
				}
				normalize := func(f map[string]any) map[string]any { // 与 pull 回声同一条往返
					changed, _, _ := FieldsToVersion(f, v)
					return VersionToFields(changed, prog)
				}
				if found := ss.timeoutCreateReconcile(ss.p.FeishuBitableAppToken, ss.p.FeishuVersionTableID, fields, normalize); found != "" {
					ss.warn("推送版本 %s 超时，经内容核对复用远端记录 %s（未重复建行）", v.Name, found)
					recID = found
				}
			}
			if err != nil && recID == "" {
				ss.warn("推送版本 %s 失败（本地变更已保留，恢复后可补推）: %v", v.Name, err)
				continue
			}
			// 先记 pending id 再回填：回填中断时下轮按它复用同一远端记录（幂等重跑）
			if err := SetSyncState(ss.s, pendingRecordKey("version", v.ID), recID); err != nil {
				ss.warn("记录版本 %d 的 pending record_id 失败: %v", v.ID, err)
			}
		} else if err = ss.api.RecordUpdate(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuVersionTableID, recID, fields); err != nil {
			ss.warn("推送版本 %s 失败（本地变更已保留，恢复后可补推）: %v", v.Name, err)
			continue
		}
		if err := MarkSyncedRecord(ss.s, "version", v.ID, recID, hash); err != nil {
			ss.warn("回填版本 %s 的同步元数据失败（远端记录 %s 已保留，下轮复用）: %v", v.Name, recID, err)
			continue
		}
		_ = SetSyncState(ss.s, pendingRecordKey("version", v.ID), "") // 回填成功即清 pending
		ss.res.Pushed++
	}
	return nil
}

// pushMembers 把成员镜像推送到飞书"成员表"（单向：pulse → 飞书）。名单与容量供人
// 查看；成员维护走 CLI/MCP，飞书侧修改不回流。成员无软删语义、低频变更，按内容
// 指纹判脏（首次全量建行，其后仅变更行 PUT）。
func (ss *syncSession) pushMembers(membersTableID string) error {
	members, err := ss.s.ListMembers()
	if err != nil {
		return fmt.Errorf("加载成员失败: %w", err)
	}
	for _, m := range members {
		fields := MemberToFields(m)
		hash := ContentHash(fields)
		if hash == m.BitableSyncedHash {
			continue // 与上次同步一致，免调用
		}
		recID := m.BitableRecordID
		if recID == "" {
			recID, err = ss.api.RecordCreate(ss.ctx, ss.p.FeishuBitableAppToken, membersTableID, fields)
			if err != nil && isTimeoutErr(err) { // 超时防重复（REAL-2）：同 pushTasks（成员无反向适配器，逐值 flattenRichText）
				if found := ss.timeoutCreateReconcile(ss.p.FeishuBitableAppToken, membersTableID, fields, flattenFields); found != "" {
					ss.warn("推送成员 %s 超时，经内容核对复用远端记录 %s（未重复建行）", m.Name, found)
					recID = found
				}
			}
			if err != nil && recID == "" {
				ss.warn("推送成员 %s 失败（本地变更已保留，恢复后可补推）: %v", m.Name, err)
				continue
			}
		} else if err = ss.api.RecordUpdate(ss.ctx, ss.p.FeishuBitableAppToken, membersTableID, recID, fields); err != nil {
			ss.warn("推送成员 %s 失败（本地变更已保留，恢复后可补推）: %v", m.Name, err)
			continue
		}
		if err := MarkSyncedRecord(ss.s, "member", m.ID, recID, hash); err != nil {
			ss.warn("回填成员 %s 的同步元数据失败: %v", m.Name, err)
			continue
		}
		ss.res.Pushed++
	}
	return nil
}

// pushTasks 推送本地任务脏行；archived 行只推一次墓碑（已废弃=true），推完置
// synced_hash=DeprecatedHash 终态，不再重推。
func (ss *syncSession) pushTasks() error {
	tasks, err := ss.s.ListTasks(ss.p.ID, store.TaskFilter{IncludeArchived: true})
	if err != nil {
		return fmt.Errorf("加载任务失败: %w", err)
	}
	for _, t := range tasks {
		if t.Archived {
			if t.BitableRecordID != "" && t.BitableSyncedHash != DeprecatedHash {
				fields := map[string]any{"已废弃": true}
				if err := ss.api.RecordUpdate(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuTaskTableID, t.BitableRecordID, fields); err != nil {
					ss.warn("推送任务 %d 的废弃标记失败: %v", t.ID, err)
					continue
				}
				if err := MarkSynced(ss.s, "task", t.ID, DeprecatedHash); err != nil {
					return err
				}
				ss.res.Deprecated++
			}
			continue
		}
		fields := TaskToFields(t, ss.idToName, ss.verIDToName)
		hash := ContentHash(fields)
		if hash == t.BitableSyncedHash {
			continue
		}
		recID := ss.resolvePushRecordID("task", t.ID, t.BitableRecordID)
		if recID == "" {
			recID, err = ss.api.RecordCreate(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuTaskTableID, fields)
			if err != nil && isTimeoutErr(err) { // 超时防重复（REAL-2）：服务端可能已写入，按内容核对一次
				normalize := func(f map[string]any) map[string]any { // 与 pull 回声同一条往返（本行为底值）
					changed, _, _ := FieldsToTask(f, t, ss.nameToID, ss.verNameToID)
					return TaskToFields(changed, ss.idToName, ss.verIDToName)
				}
				if found := ss.timeoutCreateReconcile(ss.p.FeishuBitableAppToken, ss.p.FeishuTaskTableID, fields, normalize); found != "" {
					ss.warn("推送任务 %d 超时，经内容核对复用远端记录 %s（未重复建行）", t.ID, found)
					recID = found
				}
			}
			if err != nil && recID == "" {
				ss.warn("推送任务 %d 失败（本地变更已保留，恢复后可补推）: %v", t.ID, err)
				continue
			}
			// 先记 pending id 再回填：回填中断时下轮按它复用同一远端记录（幂等重跑）
			if err := SetSyncState(ss.s, pendingRecordKey("task", t.ID), recID); err != nil {
				ss.warn("记录任务 %d 的 pending record_id 失败: %v", t.ID, err)
			}
		} else if err = ss.api.RecordUpdate(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuTaskTableID, recID, fields); err != nil {
			ss.warn("推送任务 %d 失败（本地变更已保留，恢复后可补推）: %v", t.ID, err)
			continue
		}
		if err := MarkSyncedRecord(ss.s, "task", t.ID, recID, hash); err != nil {
			ss.warn("回填任务 %d 的同步元数据失败（远端记录 %s 已保留，下轮复用）: %v", t.ID, recID, err)
			continue
		}
		_ = SetSyncState(ss.s, pendingRecordKey("task", t.ID), "") // 回填成功即清 pending
		ss.res.Pushed++
	}
	return nil
}

// —— pull ————————————————————————————————————————————————————————————————

// conflictLog 是 sync_conflict 活动的 detail 结构（双方映射字段 JSON）。
type conflictLog struct {
	Local  map[string]any `json:"local"`
	Remote map[string]any `json:"remote"`
}

// logConflict 落一条 action="sync_conflict" 活动，detail 存双方字段 JSON。
func (ss *syncSession) logConflict(entityType string, entityID int64, localFields, remoteFields map[string]any) {
	detail, err := json.Marshal(conflictLog{Local: localFields, Remote: remoteFields})
	if err != nil { // 字段值均可序列化，理论不可达
		detail = []byte("{}")
	}
	a := model.Activity{
		ProjectID: ss.p.ID, ActorID: ss.actor.ID, ActorType: ss.actor.Type,
		Action: "sync_conflict", EntityType: entityType, EntityID: entityID, Detail: string(detail),
	}
	if err := ss.s.LogActivity(a); err != nil {
		ss.warn("记录冲突活动失败: %v", err)
	}
}

// lwwPlan 决定一条已存在本地行的远端记录如何合入：
// 返回 (是否回声跳过, 是否冲突, 需要覆盖本地)。
func (ss *syncSession) lwwPlan(entity string, local map[string]any, localSyncedHash, ancestorHash string, localID int64, remoteHash string, remoteFields map[string]any) (echo, conflict bool) {
	if remoteHash == localSyncedHash {
		return true, false // 自回声：内容与上次同步一致
	}
	localHash := ContentHash(local)
	localDirty := localHash != ancestorHash
	remoteChanged := remoteHash != ancestorHash
	// 远端内容与本地当前内容一致时没有"丢失的本地修改"，不构成冲突
	//（回填中断等场景下两侧已自然收敛，只需刷新 synced_hash）。
	if localDirty && remoteChanged && remoteHash != localHash {
		conflict = true
		ss.res.Conflicts = append(ss.res.Conflicts,
			fmt.Sprintf("%s#%d 本地有未同步修改，已被 Bitable 侧覆盖(LWW)", entity, localID))
		ss.logConflict(entity, localID, local, remoteFields)
	}
	return false, conflict
}

// pullVersions 拉取版本表：水位增量 + 本地缺行合入 + 回声/LWW 覆盖。
// 合入失败的记录压住水位（capWatermark），下轮重试；成功行重处理由回声判定跳过。
func (ss *syncSession) pullVersions() error {
	prev := ss.getWatermark("versions")
	recs, err := ss.api.RecordSearch(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuVersionTableID)
	if err != nil {
		return fmt.Errorf("拉取版本表失败: %w", err)
	}
	versions, err := ss.s.ListVersions(ss.p.ID)
	if err != nil {
		return fmt.Errorf("加载版本失败: %w", err)
	}
	byRecord := map[string]model.Version{}
	byName := map[string]model.Version{}
	var failures failureTracker
	maxLMT := prev
	for _, v := range versions {
		if rid := ss.resolvePushRecordID("version", v.ID, v.BitableRecordID); rid != "" {
			byRecord[rid] = v // 含回填中断的 pending id，pull 不得据此重复建行
		}
		byName[v.Name] = v
	}
	for _, rec := range recs {
		if rec.LastModifiedTime > maxLMT {
			maxLMT = rec.LastModifiedTime
		}
		if rec.LastModifiedTime < prev {
			continue // 增量：上一轮已处理过的老记录
		}
		changed, missing, warns := FieldsToVersion(rec.Fields, model.Version{ProjectID: ss.p.ID})
		if len(missing) > 0 {
			failures.note(rec.LastModifiedTime) // 缺字段跳过也压住水位，字段补全后下轮重试
			ss.warn("跳过版本记录 %s: 缺字段 %v", rec.RecordID, missing)
			continue
		}
		for _, w := range warns {
			ss.warn("版本记录 %s: %s", rec.RecordID, w)
		}
		local, found := byRecord[rec.RecordID]
		if !found {
			if linked, ok := byName[changed.Name]; ok {
				// 双机各自创建过同名版本：按名字合并到本地行（幂等链接远端记录）
				local = linked
				found = true
			}
		}
		if !found {
			v, err := ss.s.CreateVersion(model.Version{
				ProjectID: ss.p.ID, Name: changed.Name,
				TargetDate: changed.TargetDate, Status: changed.Status, Notes: changed.Notes,
			}, ss.actor, nil)
			if err != nil { // 常见为状态非法等数据问题：告警跳过并压住水位，下轮重试
				failures.note(rec.LastModifiedTime)
				ss.warn("合入远端版本 %q 失败: %v", changed.Name, err)
				continue
			}
			// 版本映射表必须随合入刷新：同轮后续任务记录的版本列要能解析到它
			ss.verIDToName[v.ID] = v.Name
			ss.verNameToID[v.Name] = v.ID
			byName[v.Name] = v
			byRecord[rec.RecordID] = v
			if !ss.markPulled("version", v.ID, rec.RecordID, ContentHash(ss.versionFields(v))) {
				failures.note(rec.LastModifiedTime)
				continue
			}
			ss.res.Pulled++
			continue
		}
		// 进度列按本地行的 versionID 实时计算（changed 来自 FieldsToVersion 无 ID，
		// 直接用会查到 version_id=0 的空统计，导致远端哈希恒不匹配、每轮误判变更）。
		prog, progErr := ss.s.VersionProgress(ss.p.ID, local.ID)
		if progErr != nil {
			prog = store.VersionProgress{}
		}
		remoteFields := VersionToFields(changed, prog)
		remoteHash := ContentHash(remoteFields)
		if echo, _ := ss.lwwPlan("version", ss.versionFields(local), local.BitableSyncedHash,
			ss.versionAncestor[local.ID], local.ID, remoteHash, remoteFields); echo {
			ss.res.SkippedEcho++
			continue
		}
		if changed.Name != local.Name {
			ss.warn("远端版本 %d 名字 %q 与本地 %q 不一致，保留本地名", local.ID, changed.Name, local.Name)
		}
		if _, err := ss.s.UpdateVersion(local.ID, store.VersionChanges{
			Status: &changed.Status, TargetDate: &changed.TargetDate, Notes: &changed.Notes,
		}, ss.actor, nil); err != nil {
			failures.note(rec.LastModifiedTime)
			ss.warn("覆盖本地版本 %d 失败: %v", local.ID, err)
			continue
		}
		if !ss.markPulled("version", local.ID, rec.RecordID, remoteHash) {
			failures.note(rec.LastModifiedTime)
			continue
		}
		ss.res.Pulled++
	}
	ss.setWatermark("versions", capWatermark(maxLMT, failures.minLMT))
	return nil
}

// pullTasks 拉取任务表：已废弃墓碑归档 → 水位增量 → 本地缺行插入 → 回声/LWW 覆盖。
// 合入失败的记录压住水位（capWatermark），下轮重试；成功行重处理由回声判定跳过。
func (ss *syncSession) pullTasks() error {
	prev := ss.getWatermark("tasks")
	recs, err := ss.api.RecordSearch(ss.ctx, ss.p.FeishuBitableAppToken, ss.p.FeishuTaskTableID)
	if err != nil {
		return fmt.Errorf("拉取任务表失败: %w", err)
	}
	tasks, err := ss.s.ListTasks(ss.p.ID, store.TaskFilter{IncludeArchived: true})
	if err != nil {
		return fmt.Errorf("加载任务失败: %w", err)
	}
	byRecord := map[string]model.Task{}
	var failures failureTracker
	maxLMT := prev
	for _, t := range tasks {
		if rid := ss.resolvePushRecordID("task", t.ID, t.BitableRecordID); rid != "" {
			byRecord[rid] = t // 含回填中断的 pending id，pull 不得据此重复建行
		}
	}
	for _, rec := range recs {
		if rec.LastModifiedTime > maxLMT {
			maxLMT = rec.LastModifiedTime
		}
		if truthy(rec.Fields["已废弃"]) { // 远端墓碑 → 本地软删（幂等）
			if local, ok := byRecord[rec.RecordID]; ok {
				if !local.Archived {
					if err := ss.s.SoftDeleteTask(local.ID, ss.actor, nil); err != nil {
						failures.note(rec.LastModifiedTime)
						ss.warn("归档任务 %d 失败: %v", local.ID, err)
						continue
					}
				}
				ss.res.Deprecated++
			}
			continue
		}
		if rec.LastModifiedTime < prev {
			continue
		}
		// 远端提到的负责人先落成员表（get-or-create），FieldsToTask 才能解析 ID
		if name := toText(rec.Fields["负责人"]); name != "" {
			if _, err := ss.ensureMember(name); err != nil {
				ss.warn("记录 %s: %v", rec.RecordID, err)
			}
		}
		local, found := byRecord[rec.RecordID]
		base := local
		if !found {
			base = model.Task{ProjectID: ss.p.ID} // 全新远端记录：不可能自回声，直接合入
		}
		changed, missing, warns := FieldsToTask(rec.Fields, base, ss.nameToID, ss.verNameToID)
		if len(missing) > 0 {
			failures.note(rec.LastModifiedTime) // 缺字段跳过也压住水位，字段补全后下轮重试
			ss.warn("跳过任务记录 %s: 缺字段 %v", rec.RecordID, missing)
			continue
		}
		for _, w := range warns {
			ss.warn("任务记录 %s: %s", rec.RecordID, w)
		}
		remoteFields := TaskToFields(changed, ss.idToName, ss.verIDToName)
		remoteHash := ContentHash(remoteFields)
		if !found {
			t, err := ss.s.CreateTask(model.Task{
				ProjectID: ss.p.ID, Title: changed.Title, AssigneeID: changed.AssigneeID,
				Status: changed.Status, Priority: changed.Priority, EstimateDays: changed.EstimateDays,
				StartDate: changed.StartDate, DueDate: changed.DueDate, VersionID: changed.VersionID,
			}, ss.actor, nil)
			if err != nil {
				failures.note(rec.LastModifiedTime)
				ss.warn("合入远端任务 %q 失败: %v", changed.Title, err)
				continue
			}
			if !ss.markPulled("task", t.ID, rec.RecordID, remoteHash) {
				failures.note(rec.LastModifiedTime)
				continue
			}
			ss.res.Pulled++
			continue
		}
		if echo, _ := ss.lwwPlan("task", TaskToFields(local, ss.idToName, ss.verIDToName),
			local.BitableSyncedHash, ss.taskAncestor[local.ID], local.ID, remoteHash, remoteFields); echo {
			ss.res.SkippedEcho++
			continue
		}
		// Bitable 覆盖本地（LWW）；未映射的 Description 等本地字段保持不动
		priority := int64(changed.Priority)
		if _, err := ss.s.UpdateTask(local.ID, store.TaskChanges{
			Title: &changed.Title, Status: &changed.Status, Priority: &priority,
			EstimateDays: &changed.EstimateDays, StartDate: &changed.StartDate, DueDate: &changed.DueDate,
			AssigneeID: &changed.AssigneeID, VersionID: &changed.VersionID,
		}, ss.actor, nil); err != nil {
			failures.note(rec.LastModifiedTime)
			ss.warn("覆盖本地任务 %d 失败: %v", local.ID, err)
			continue
		}
		// 本地已同步到飞书的成果被远端更新覆盖：补提示（E2E-3）
		ss.warnRecentLocalOverwrite(rec.LastModifiedTime, local)
		if !ss.markPulled("task", local.ID, rec.RecordID, remoteHash) {
			failures.note(rec.LastModifiedTime)
			continue
		}
		ss.res.Pulled++
	}
	ss.setWatermark("tasks", capWatermark(maxLMT, failures.minLMT))
	return nil
}
