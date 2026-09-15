// sync_entities.go 把 v1.1 六实体（需求/评审/会议/bug/提测单/发版）接入同步引擎。
//
// 设计选择（计划授权二选一：泛型会话 or 六份显式循环）：采用「一个泛型同步循环 +
// 每实体一个适配器」——pushTasks/pullTasks 的循环体约百行，复制六份的漂移风险高于
// 一层泛型抽象；既有任务/版本路径（pushVersions/pushTasks/pullVersions/pullTasks）
// 保持原样不动，作为行为基准（v1.0 路径零改动）。
//
// 每表沿用任务表的全部机制：ContentHash 回声、祖先基线 LWW 冲突（snapshotAncestors）、
// per-table 水位（键 pull_watermark:<pid>:<表名>）、failureTracker 水位封顶、
// pending_record 幂等、软删墓碑（已废弃 checkbox）、synced_at 覆盖警告。
// 差异点：
//   - 表 id 取自 projects.feishu_tables_json（旧项目/采用既有 base 的 bind 未配置时
//     为空 → 该表整体跳过，行为与 v1.0 一致，只同步任务与版本，不报错）；
//   - 指纹以「落库后的行」重算（create/applyRemote 返回持久化行）：创建即定字段
//     （评审类型/评审时间/需求ID、会议标题/时间、发布时间）本地保留值与远端不一致时，
//     回声判定按本地实盘收敛，不会每轮重复合入；
//   - 会议无更新路径：远端修改吸收不回流（告警一次，水位照常推进）。
package feishu

import (
	"fmt"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// entityRow 是泛型循环所需的行级同步元数据视图。
type entityRow struct {
	id         int64
	recordID   string
	syncedHash string
	syncedAt   string
	archived   bool
}

// entityDef 把一种实体接入泛型同步循环的最小协议；实体名与 activity.entity_type
// 及 pending_record/sync_state 键一致，table 为水位键用的表名。
type entityDef[T any] struct {
	entity string // "requirement" 等（sync_state/pending/冲突活动实体名）
	table  string // 水位键表名（"requirements" 等，per-table 命名空间）
	label  string // 中文称呼（警告文案）

	list       func(ss *syncSession) ([]T, error) // 项目全部行（含 archived）
	meta       func(row T) entityRow
	toFields   func(ss *syncSession, row T) map[string]any
	fromFields func(ss *syncSession, f map[string]any, local T) (changed T, missing []string, warnings []string)
	newBase    func(ss *syncSession) T // 全新远端行的底值（注入 ProjectID）

	create      func(ss *syncSession, remote T) (T, error)                 // 返回持久化后的行
	applyRemote func(ss *syncSession, id int64, remote T) (T, error)       // nil = create-only 实体
	softDelete  func(ss *syncSession, id int64) error                      // pull 墓碑归档本地行
	prepareRec  func(ss *syncSession, fields map[string]any, recID string) // 人名列 get-or-create
	title       func(row T) string                                         // 覆盖警告文案用
}

// ensureRecordMembers 对远端记录里的人名列先行 get-or-create 成员（FieldsToX 解析
// ID 的前置步骤，与 pullTasks 对负责人列的处理一致）；失败仅告警不中断本轮合入。
func ensureRecordMembers(ss *syncSession, recordID string, fields map[string]any, columns ...string) {
	for _, col := range columns {
		if name := toText(fields[col]); name != "" {
			if _, err := ss.ensureMember(name); err != nil {
				ss.warn("记录 %s: %v", recordID, err)
			}
		}
	}
}

// warnEntityOverwrite 是 warnRecentLocalOverwrite 的六实体泛化版（语义一致：判定基准
// 是 synced_at；远端 lmt 晚于它说明覆盖丢弃的是已同步到飞书的成果）。仅适用于有更新
// 路径的实体；会议为只读记录，覆盖不发生，不适用。
func (ss *syncSession) warnEntityOverwrite(label, title string, lmt int64, m entityRow) {
	if lmt <= 0 || m.syncedAt == "" {
		return
	}
	ts, err := time.Parse(storeTimeLayout, m.syncedAt)
	if err != nil || lmt <= ts.Unix() {
		return
	}
	ss.warn("%s #%d %s 被飞书侧更新覆盖（覆盖的是你已同步到飞书的修改）", label, m.id, title)
}

// snapshotEntityAncestors 在 push 之前记下该实体每行的 synced_hash，作为 pull 冲突
// 判定的共同基线（表未配置时无需快照）。
func snapshotEntityAncestors[T any](ss *syncSession, def entityDef[T], tableID string) error {
	if tableID == "" {
		return nil
	}
	rows, err := def.list(ss)
	if err != nil {
		return fmt.Errorf("加载%s失败: %w", def.label, err)
	}
	m := make(map[int64]string, len(rows))
	for _, row := range rows {
		mm := def.meta(row)
		m[mm.id] = mm.syncedHash
	}
	ss.entityAncestors[def.entity] = m
	return nil
}

// pushEntity 推送本地脏行：无 record_id 建记录并回填，否则更新；archived 行只推一次
// 墓碑（已废弃=true），推完置 synced_hash=DeprecatedHash 终态，不再重推。
// 单行失败不中断（记 Warnings 继续补推其余行，dirty 标记保留待下轮重试）。
func pushEntity[T any](ss *syncSession, def entityDef[T], tableID string) error {
	if tableID == "" {
		return nil // 项目未配置该表：跳过
	}
	rows, err := def.list(ss)
	if err != nil {
		return fmt.Errorf("加载%s失败: %w", def.label, err)
	}
	for _, row := range rows {
		m := def.meta(row)
		if m.archived {
			if m.recordID != "" && m.syncedHash != DeprecatedHash {
				fields := map[string]any{"已废弃": true}
				if err := ss.api.RecordUpdate(ss.ctx, ss.p.FeishuBitableAppToken, tableID, m.recordID, fields); err != nil {
					ss.warn("推送%s %d 的废弃标记失败: %v", def.label, m.id, err)
					continue
				}
				if err := MarkSynced(ss.s, def.entity, m.id, DeprecatedHash); err != nil {
					return err
				}
				ss.res.Deprecated++
			}
			continue
		}
		fields := def.toFields(ss, row)
		hash := ContentHash(fields)
		if hash == m.syncedHash {
			continue // 与上次同步一致，免调用
		}
		recID := ss.resolvePushRecordID(def.entity, m.id, m.recordID)
		if recID == "" {
			recID, err = ss.api.RecordCreate(ss.ctx, ss.p.FeishuBitableAppToken, tableID, fields)
			if err != nil && isTimeoutErr(err) { // 超时防重复（REAL-2）：服务端可能已写入，按内容核对一次
				if found := ss.timeoutCreateReconcile(ss.p.FeishuBitableAppToken, tableID, fields); found != "" {
					ss.warn("推送%s %d 超时，经内容核对复用远端记录 %s（未重复建行）", def.label, m.id, found)
					recID = found
				}
			}
			if err != nil && recID == "" {
				ss.warn("推送%s %d 失败（本地变更已保留，恢复后可补推）: %v", def.label, m.id, err)
				continue
			}
			// 先记 pending id 再回填：回填中断时下轮按它复用同一远端记录（幂等重跑）
			if err := SetSyncState(ss.s, pendingRecordKey(def.entity, m.id), recID); err != nil {
				ss.warn("记录%s %d 的 pending record_id 失败: %v", def.label, m.id, err)
			}
		} else if err = ss.api.RecordUpdate(ss.ctx, ss.p.FeishuBitableAppToken, tableID, recID, fields); err != nil {
			ss.warn("推送%s %d 失败（本地变更已保留，恢复后可补推）: %v", def.label, m.id, err)
			continue
		}
		if err := MarkSyncedRecord(ss.s, def.entity, m.id, recID, hash); err != nil {
			ss.warn("回填%s %d 的同步元数据失败（远端记录 %s 已保留，下轮复用）: %v", def.label, m.id, recID, err)
			continue
		}
		_ = SetSyncState(ss.s, pendingRecordKey(def.entity, m.id), "") // 回填成功即清 pending
		ss.res.Pushed++
	}
	return nil
}

// pullEntity 拉取一张六实体表：已废弃墓碑归档 → 水位增量 → 本地缺行插入 →
// 回声/LWW 覆盖。与 pullTasks 同构：合入失败的记录压住水位（capWatermark）下轮重试；
// 成功行重处理由回声判定跳过。指纹以「落库后的行」重算（create/applyRemote 返回持久化
// 行），创建即定字段按本地实盘收敛，见文件头说明。
func pullEntity[T any](ss *syncSession, def entityDef[T], tableID string) error {
	if tableID == "" {
		return nil // 项目未配置该表：跳过
	}
	prev := ss.getWatermark(def.table)
	recs, err := ss.api.RecordSearch(ss.ctx, ss.p.FeishuBitableAppToken, tableID)
	if err != nil {
		return fmt.Errorf("拉取%s失败: %w", def.label, err)
	}
	rows, err := def.list(ss)
	if err != nil {
		return fmt.Errorf("加载%s失败: %w", def.label, err)
	}
	byRecord := map[string]T{}
	var failures failureTracker
	maxLMT := prev
	for _, row := range rows {
		m := def.meta(row)
		if rid := ss.resolvePushRecordID(def.entity, m.id, m.recordID); rid != "" {
			byRecord[rid] = row // 含回填中断的 pending id，pull 不得据此重复建行
		}
	}
	for _, rec := range recs {
		if rec.LastModifiedTime > maxLMT {
			maxLMT = rec.LastModifiedTime
		}
		if truthy(rec.Fields["已废弃"]) { // 远端墓碑 → 本地软删（幂等）
			if local, ok := byRecord[rec.RecordID]; ok {
				m := def.meta(local)
				if !m.archived {
					if err := def.softDelete(ss, m.id); err != nil {
						failures.note(rec.LastModifiedTime)
						ss.warn("归档%s %d 失败: %v", def.label, m.id, err)
						continue
					}
				}
				ss.res.Deprecated++
			}
			continue
		}
		if rec.LastModifiedTime < prev {
			continue // 增量：上一轮已处理过的老记录
		}
		if def.prepareRec != nil {
			def.prepareRec(ss, rec.Fields, rec.RecordID)
		}
		local, found := byRecord[rec.RecordID]
		base := local
		if !found {
			base = def.newBase(ss) // 全新远端记录：不可能自回声，直接合入
		}
		changed, missing, warns := def.fromFields(ss, rec.Fields, base)
		if len(missing) > 0 {
			failures.note(rec.LastModifiedTime) // 缺字段跳过也压住水位，字段补全后下轮重试
			ss.warn("跳过%s记录 %s: 缺字段 %v", def.label, rec.RecordID, missing)
			continue
		}
		// warns 延迟到回声判定之后才输出：回声命中的记录（内容与本地一致）不该刷"创建即定保留本地"类噪音
		remoteFields := def.toFields(ss, changed)
		remoteHash := ContentHash(remoteFields)
		if !found {
			created, err := def.create(ss, changed)
			if err != nil { // 常见为引用不存在等数据问题：告警跳过并压住水位，下轮重试
				failures.note(rec.LastModifiedTime)
				ss.warn("合入远端%s失败（记录 %s）: %v", def.label, rec.RecordID, err)
				continue
			}
			byRecord[rec.RecordID] = created
			if !ss.markPulled(def.entity, def.meta(created).id, rec.RecordID,
				ContentHash(def.toFields(ss, created))) {
				failures.note(rec.LastModifiedTime)
				continue
			}
			for _, w := range warns {
				ss.warn("%s记录 %s: %s", def.label, rec.RecordID, w)
			}
			ss.res.Pulled++
			continue
		}
		lm := def.meta(local)
		if echo, _ := ss.lwwPlan(def.entity, def.toFields(ss, local), lm.syncedHash,
			ss.entityAncestors[def.entity][lm.id], lm.id, remoteHash, remoteFields); echo {
			ss.res.SkippedEcho++
			continue
		}
		for _, w := range warns {
			ss.warn("%s记录 %s: %s", def.label, rec.RecordID, w)
		}
		if def.applyRemote != nil {
			updated, err := def.applyRemote(ss, lm.id, changed)
			if err != nil {
				failures.note(rec.LastModifiedTime)
				ss.warn("覆盖本地%s %d 失败: %v", def.label, lm.id, err)
				continue
			}
			// 本地已同步到飞书的成果被远端更新覆盖：补提示（与任务表 E2E-3 同语义）
			ss.warnEntityOverwrite(def.label, def.title(local), rec.LastModifiedTime, lm)
			if !ss.markPulled(def.entity, lm.id, rec.RecordID,
				ContentHash(def.toFields(ss, updated))) {
				failures.note(rec.LastModifiedTime)
				continue
			}
		} else {
			// create-only 实体（会议）：远端修改吸收不回流，hash 以本地实盘为准，
			// 水位照常推进（下轮由回声判定静默跳过）
			ss.warn("%s #%d 为只读记录（仅创建与废弃同步），远端修改已忽略", def.label, lm.id)
			if !ss.markPulled(def.entity, lm.id, rec.RecordID,
				ContentHash(def.toFields(ss, local))) {
				failures.note(rec.LastModifiedTime)
				continue
			}
		}
		ss.res.Pulled++
	}
	ss.setWatermark(def.table, capWatermark(maxLMT, failures.minLMT))
	return nil
}

// —— 六实体适配器（依赖序：需求先于评审合入；版本先于提测/发版由外层 pullVersions 保证）——

func requirementDef() entityDef[model.Requirement] {
	return entityDef[model.Requirement]{
		entity: "requirement", table: "requirements", label: "需求",
		list: func(ss *syncSession) ([]model.Requirement, error) { return ss.s.ListRequirements(ss.p.ID, "") },
		meta: func(r model.Requirement) entityRow {
			return entityRow{id: r.ID, recordID: r.BitableRecordID, syncedHash: r.BitableSyncedHash,
				syncedAt: r.SyncedAt, archived: r.Archived}
		},
		toFields: func(ss *syncSession, r model.Requirement) map[string]any {
			return RequirementToFields(r, ss.idToName)
		},
		fromFields: func(ss *syncSession, f map[string]any, local model.Requirement) (model.Requirement, []string, []string) {
			return FieldsToRequirement(f, local, ss.nameToID)
		},
		newBase: func(ss *syncSession) model.Requirement { return model.Requirement{ProjectID: ss.p.ID} },
		create: func(ss *syncSession, r model.Requirement) (model.Requirement, error) {
			return ss.s.CreateRequirement(r, ss.actor, nil)
		},
		applyRemote: func(ss *syncSession, id int64, r model.Requirement) (model.Requirement, error) {
			priority := int64(r.Priority)
			return ss.s.UpdateRequirement(id, store.RequirementChanges{
				Title: &r.Title, Description: &r.Description, Status: &r.Status,
				OwnerID: &r.OwnerID, Priority: &priority,
			}, ss.actor, nil)
		},
		softDelete: func(ss *syncSession, id int64) error {
			return ss.s.SoftDeleteSyncEntity("requirement", id, ss.actor, nil)
		},
		prepareRec: func(ss *syncSession, fields map[string]any, recID string) {
			ensureRecordMembers(ss, recID, fields, "负责人")
		},
		title: func(r model.Requirement) string { return r.Title },
	}
}

func reviewDef() entityDef[model.Review] {
	return entityDef[model.Review]{
		entity: "review", table: "reviews", label: "评审",
		list: func(ss *syncSession) ([]model.Review, error) { return ss.s.ListReviews(ss.p.ID, 0) },
		meta: func(v model.Review) entityRow {
			return entityRow{id: v.ID, recordID: v.BitableRecordID, syncedHash: v.BitableSyncedHash,
				syncedAt: v.SyncedAt, archived: v.Archived}
		},
		toFields: func(ss *syncSession, v model.Review) map[string]any { return ReviewToFields(v) },
		fromFields: func(ss *syncSession, f map[string]any, local model.Review) (model.Review, []string, []string) {
			return FieldsToReview(f, local)
		},
		newBase: func(ss *syncSession) model.Review { return model.Review{ProjectID: ss.p.ID} },
		create: func(ss *syncSession, v model.Review) (model.Review, error) {
			// 需求ID 是本地引用：本地不存在、或存在但属于其它项目（跨机 ID 撞号）时
			// 置空并告警（外键拒绝悬空引用；语义对齐任务表"版本不在本地保留原值"的容错）
			if v.RequirementID != 0 {
				req, found, err := ss.s.GetRequirement(v.RequirementID)
				if err != nil || !found || req.ProjectID != ss.p.ID {
					ss.warn("评审引用的需求 #%d 不在本地，已置空关联", v.RequirementID)
					v.RequirementID = 0
				}
			}
			return ss.s.CreateReview(v, ss.actor, nil)
		},
		applyRemote: func(ss *syncSession, id int64, v model.Review) (model.Review, error) {
			// 仅结论可远端合入（评审类型/评审时间/需求ID 创建即定）；经
			// UpdateReviewConclusion 落库并落 update 活动，同值为 no-op
			return ss.s.UpdateReviewConclusion(id, v.Conclusion, ss.actor, nil)
		},
		softDelete: func(ss *syncSession, id int64) error {
			return ss.s.SoftDeleteSyncEntity("review", id, ss.actor, nil)
		},
		title: func(v model.Review) string { return v.Kind },
	}
}

func meetingDef() entityDef[model.Meeting] {
	return entityDef[model.Meeting]{
		entity: "meeting", table: "meetings", label: "会议",
		list: func(ss *syncSession) ([]model.Meeting, error) { return ss.s.ListMeetings(ss.p.ID) },
		meta: func(m model.Meeting) entityRow {
			return entityRow{id: m.ID, recordID: m.BitableRecordID, syncedHash: m.BitableSyncedHash,
				syncedAt: m.SyncedAt, archived: m.Archived}
		},
		toFields: func(ss *syncSession, m model.Meeting) map[string]any { return MeetingToFields(m) },
		fromFields: func(ss *syncSession, f map[string]any, local model.Meeting) (model.Meeting, []string, []string) {
			return FieldsToMeeting(f, local)
		},
		newBase: func(ss *syncSession) model.Meeting { return model.Meeting{ProjectID: ss.p.ID} },
		create: func(ss *syncSession, m model.Meeting) (model.Meeting, error) {
			return ss.s.CreateMeeting(m, ss.actor, nil)
		},
		applyRemote: nil, // 会议无更新路径：create + 墓碑；远端修改吸收不回流
		softDelete: func(ss *syncSession, id int64) error {
			return ss.s.SoftDeleteSyncEntity("meeting", id, ss.actor, nil)
		},
		title: func(m model.Meeting) string { return m.Title },
	}
}

func bugDef() entityDef[model.Bug] {
	return entityDef[model.Bug]{
		entity: "bug", table: "bugs", label: "Bug",
		list: func(ss *syncSession) ([]model.Bug, error) { return ss.s.ListBugs(ss.p.ID, store.BugFilter{}) },
		meta: func(b model.Bug) entityRow {
			return entityRow{id: b.ID, recordID: b.BitableRecordID, syncedHash: b.BitableSyncedHash,
				syncedAt: b.SyncedAt, archived: b.Archived}
		},
		toFields: func(ss *syncSession, b model.Bug) map[string]any {
			return BugToFields(b, ss.idToName, ss.verIDToName)
		},
		fromFields: func(ss *syncSession, f map[string]any, local model.Bug) (model.Bug, []string, []string) {
			return FieldsToBug(f, local, ss.nameToID, ss.verNameToID)
		},
		newBase: func(ss *syncSession) model.Bug { return model.Bug{ProjectID: ss.p.ID} },
		create: func(ss *syncSession, b model.Bug) (model.Bug, error) {
			return ss.s.CreateBug(b, ss.actor, nil)
		},
		applyRemote: func(ss *syncSession, id int64, b model.Bug) (model.Bug, error) {
			severity := int64(b.Severity)
			return ss.s.UpdateBug(id, store.BugChanges{
				Title: &b.Title, Status: &b.Status, Severity: &severity,
				AssigneeID: &b.AssigneeID, FoundVersionID: &b.FoundVersionID,
			}, ss.actor, nil)
		},
		softDelete: func(ss *syncSession, id int64) error {
			return ss.s.SoftDeleteSyncEntity("bug", id, ss.actor, nil)
		},
		prepareRec: func(ss *syncSession, fields map[string]any, recID string) {
			ensureRecordMembers(ss, recID, fields, "负责人")
		},
		title: func(b model.Bug) string { return b.Title },
	}
}

func submissionDef() entityDef[model.TestSubmission] {
	return entityDef[model.TestSubmission]{
		entity: "test_submission", table: "test_submissions", label: "提测单",
		list: func(ss *syncSession) ([]model.TestSubmission, error) {
			return ss.s.ListTestSubmissions(ss.p.ID, 0)
		},
		meta: func(t model.TestSubmission) entityRow {
			return entityRow{id: t.ID, recordID: t.BitableRecordID, syncedHash: t.BitableSyncedHash,
				syncedAt: t.SyncedAt, archived: t.Archived}
		},
		toFields: func(ss *syncSession, t model.TestSubmission) map[string]any {
			return SubmissionToFields(t, ss.idToName, ss.verIDToName)
		},
		fromFields: func(ss *syncSession, f map[string]any, local model.TestSubmission) (model.TestSubmission, []string, []string) {
			return FieldsToSubmission(f, local, ss.nameToID, ss.verNameToID)
		},
		newBase: func(ss *syncSession) model.TestSubmission { return model.TestSubmission{ProjectID: ss.p.ID} },
		create: func(ss *syncSession, t model.TestSubmission) (model.TestSubmission, error) {
			// version_id 非空且外键约束：远端版本名未解析时建行失败，告警并压住水位，
			// 待版本表（先行拉取）补齐后下轮自动重试
			return ss.s.CreateTestSubmission(t, ss.actor, nil)
		},
		applyRemote: func(ss *syncSession, id int64, t model.TestSubmission) (model.TestSubmission, error) {
			return ss.s.UpdateTestSubmission(id, store.SubmissionChanges{
				Status: &t.Status, Scope: &t.Scope, TestOwnerID: &t.TestOwnerID,
			}, ss.actor, nil)
		},
		softDelete: func(ss *syncSession, id int64) error {
			return ss.s.SoftDeleteSyncEntity("test_submission", id, ss.actor, nil)
		},
		prepareRec: func(ss *syncSession, fields map[string]any, recID string) {
			ensureRecordMembers(ss, recID, fields, "提测人", "测试负责人")
		},
		title: func(t model.TestSubmission) string { return t.Status },
	}
}

func releaseDef() entityDef[model.Release] {
	return entityDef[model.Release]{
		entity: "release", table: "releases", label: "发版记录",
		list: func(ss *syncSession) ([]model.Release, error) { return ss.s.ListReleases(ss.p.ID) },
		meta: func(r model.Release) entityRow {
			return entityRow{id: r.ID, recordID: r.BitableRecordID, syncedHash: r.BitableSyncedHash,
				syncedAt: r.SyncedAt, archived: r.Archived}
		},
		toFields: func(ss *syncSession, r model.Release) map[string]any {
			return ReleaseToFields(r, ss.idToName, ss.verIDToName)
		},
		fromFields: func(ss *syncSession, f map[string]any, local model.Release) (model.Release, []string, []string) {
			return FieldsToRelease(f, local, ss.nameToID, ss.verNameToID)
		},
		newBase: func(ss *syncSession) model.Release { return model.Release{ProjectID: ss.p.ID} },
		create: func(ss *syncSession, r model.Release) (model.Release, error) {
			// version_id 非空且外键约束：失败重试语义同提测单
			return ss.s.CreateRelease(r, ss.actor, nil)
		},
		applyRemote: func(ss *syncSession, id int64, r model.Release) (model.Release, error) {
			return ss.s.UpdateRelease(id, store.ReleaseChanges{
				Status: &r.Status, Notes: &r.Notes,
			}, ss.actor, nil)
		},
		softDelete: func(ss *syncSession, id int64) error {
			return ss.s.SoftDeleteSyncEntity("release", id, ss.actor, nil)
		},
		prepareRec: func(ss *syncSession, fields map[string]any, recID string) {
			ensureRecordMembers(ss, recID, fields, "发布负责人")
		},
		title: func(r model.Release) string { return r.Status },
	}
}

// —— 步骤装配（依赖序执行；表未配置的实体在步骤内部自跳过）——————————————————————

// entityAncestorSteps 返回六实体的祖先指纹快照步骤。
func entityAncestorSteps(ss *syncSession, tables store.FeishuTables) []func() error {
	return []func() error{
		func() error { return snapshotEntityAncestors(ss, requirementDef(), tables.Requirements) },
		func() error { return snapshotEntityAncestors(ss, reviewDef(), tables.Reviews) },
		func() error { return snapshotEntityAncestors(ss, meetingDef(), tables.Meetings) },
		func() error { return snapshotEntityAncestors(ss, bugDef(), tables.Bugs) },
		func() error { return snapshotEntityAncestors(ss, submissionDef(), tables.TestSubmissions) },
		func() error { return snapshotEntityAncestors(ss, releaseDef(), tables.Releases) },
	}
}

// entityPushSteps 返回六实体的 push 步骤（依赖序）。
func entityPushSteps(ss *syncSession, tables store.FeishuTables) []func() error {
	return []func() error{
		func() error { return pushEntity(ss, requirementDef(), tables.Requirements) },
		func() error { return pushEntity(ss, reviewDef(), tables.Reviews) },
		func() error { return pushEntity(ss, meetingDef(), tables.Meetings) },
		func() error { return pushEntity(ss, bugDef(), tables.Bugs) },
		func() error { return pushEntity(ss, submissionDef(), tables.TestSubmissions) },
		func() error { return pushEntity(ss, releaseDef(), tables.Releases) },
	}
}

// entityPullSteps 返回六实体的 pull 步骤（依赖序：需求先于评审；版本先行由外层保证）。
func entityPullSteps(ss *syncSession, tables store.FeishuTables) []func() error {
	return []func() error{
		func() error { return pullEntity(ss, requirementDef(), tables.Requirements) },
		func() error { return pullEntity(ss, reviewDef(), tables.Reviews) },
		func() error { return pullEntity(ss, meetingDef(), tables.Meetings) },
		func() error { return pullEntity(ss, bugDef(), tables.Bugs) },
		func() error { return pullEntity(ss, submissionDef(), tables.TestSubmissions) },
		func() error { return pullEntity(ss, releaseDef(), tables.Releases) },
	}
}
