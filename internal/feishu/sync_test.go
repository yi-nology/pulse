package feishu

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// deprecatedHash 与 sync.go 的软删墓碑标记一致（bitable_synced_hash 的终态值）。
const deprecatedHash = "deprecated"

// syncEnv 在 openStore 基础上补齐 sync 所需：绑定 token 的项目 + tester 成员。
func syncEnv(t *testing.T) (*store.Store, model.Project, model.Member) {
	t.Helper()
	s, p := openStore(t)
	p.FeishuBitableAppToken = "appT"
	p.FeishuTaskTableID = "tblTask"
	p.FeishuVersionTableID = "tblVer"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	return s, p, m
}

// mustMember 取回（必要时创建）指定名字的 human 成员。
func mustMember(t *testing.T, s *store.Store, name string) model.Member {
	t.Helper()
	m, err := s.GetOrCreateMember(name, "human")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// mustCreateTask 建一个已指派 tester 的任务。
func mustCreateTask(t *testing.T, s *store.Store, projectID int64, actor model.Member, title string) model.Task {
	t.Helper()
	m, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	tk, err := s.CreateTask(model.Task{
		ProjectID: projectID, Title: title, Status: "todo",
		Priority: 3, EstimateDays: 2, AssigneeID: m.ID,
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// searchScript 组装 RecordSearch 的按表脚本。
func searchScript(tableID string, recs ...Record) map[string][]Record {
	return map[string][]Record{tableID: recs}
}

// taskRecord 把已映射字段包成远端记录形态。
func taskRecord(recordID string, lmt int64, fields map[string]any) Record {
	return Record{RecordID: recordID, Fields: fields, LastModifiedTime: lmt}
}

// wantTaskSynced 断言本地任务的 record_id 与 synced_hash（按当前内容重算）。
func wantTaskSynced(t *testing.T, s *store.Store, id int64, wantRecordID string) model.Task {
	t.Helper()
	tk, found, err := s.GetTask(id)
	if err != nil || !found {
		t.Fatalf("reload task %d: found=%v err=%v", id, found, err)
	}
	if tk.BitableRecordID != wantRecordID {
		t.Fatalf("record_id = %q, want %q", tk.BitableRecordID, wantRecordID)
	}
	return tk
}

// —— 例 1：本地新建 task → push 产生 RecordCreate 且 record_id 回填、hash 落库 ————————

func TestSyncPushCreatesRecordAndBackfills(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "写周报")
	fake := &fakeAPI{createIDs: map[string][]string{"tblTask": {"recA"}}}

	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("Pushed = %d, want 1（其余 %+v）", res.Pushed, res)
	}
	if len(fake.createdFields) != 1 {
		t.Fatalf("RecordCreate 次数 = %d, want 1", len(fake.createdFields))
	}
	f := fake.createdFields[0]
	if f["任务名"] != "写周报" || f["状态"] != "todo" || f["负责人"] != "tester" ||
		f["优先级"] != "3" || f["预估人日"] != float64(2) || f["已废弃"] != false {
		t.Fatalf("推送字段不符: %+v", f)
	}
	// record_id 回填 + hash 落库（按当前内容与映射重算一致）
	tk = wantTaskSynced(t, s, tk.ID, "recA")
	if len(tk.BitableSyncedHash) != 16 {
		t.Fatalf("synced_hash 未按 16 位落库: %q", tk.BitableSyncedHash)
	}
	got := ContentHash(TaskToFields(tk, map[int64]string{tk.AssigneeID: "tester"}, map[int64]string{}))
	want := ContentHash(TaskToFields(model.Task{Title: "写周报", Status: "todo", Priority: 3,
		EstimateDays: 2, AssigneeID: 1}, map[int64]string{1: "tester"}, map[int64]string{}))
	if got != want {
		t.Fatalf("hash 不稳定: got %s want %s", got, want)
	}
	if tk.BitableSyncedHash != got {
		t.Fatalf("synced_hash = %s, want %s", tk.BitableSyncedHash, got)
	}
}

// —— 例 2：push 后立即 pull 同一条 → SkippedEcho=1（自回声） ————————————————————————

func TestSyncPullEchoSkipped(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "写周报")
	fake := &fakeAPI{}
	ctx := context.Background()

	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// 远端回读我们刚推的字段（内容一致 → 自回声）
	tk = wantTaskSynced(t, s, tk.ID, "rec1")
	fake.searchByTable = searchScript("tblTask",
		taskRecord("rec1", time.Now().Unix(), fake.createdFields[0]))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.SkippedEcho != 1 || res.Pulled != 0 || res.Pushed != 0 {
		t.Fatalf("应自回声跳过: SkippedEcho=%d Pulled=%d Pushed=%d (%+v)",
			res.SkippedEcho, res.Pulled, res.Pushed, res)
	}
	// 水位已写入
	if _, found, err := GetSyncState(s, "pull_watermark:1:tasks"); err != nil || !found {
		t.Fatalf("pull_watermark 未写: found=%v err=%v", found, err)
	}
}

// —— 例 3：远端修改过的记录 → 本地字段被覆盖、synced_hash 更新（含缺字段告警跳过）—————

func TestSyncPullRemoteChangeOverwritesAndWarnsOnMissingFields(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "写周报")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	tk = wantTaskSynced(t, s, tk.ID, "rec1")

	remote := map[string]any{
		"任务名": "写周报（远端改名）", "状态": "done", "负责人": "tester",
		"优先级": "2", "预估人日": float64(3), "已废弃": false,
	}
	// 缺任务名的记录：应跳过并告警，不得落库
	broken := map[string]any{"状态": "todo", "预估人日": float64(1)}
	fake.searchByTable = searchScript("tblTask",
		taskRecord("rec1", 2000, remote),
		taskRecord("recBad", 2001, broken))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	after, _, err := s.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "写周报（远端改名）" || after.Status != "done" || after.Priority != 2 || after.EstimateDays != 3 {
		t.Fatalf("远端值未覆盖本地: %+v", after)
	}
	if after.Description != tk.Description {
		t.Fatalf("未映射的本地字段不应被改: %+v", after)
	}
	wantHash := ContentHash(TaskToFields(model.Task{Title: "写周报（远端改名）", Status: "done",
		Priority: 2, EstimateDays: 3, AssigneeID: after.AssigneeID},
		map[int64]string{after.AssigneeID: "tester"}, map[int64]string{}))
	if after.BitableSyncedHash != wantHash {
		t.Fatalf("synced_hash = %s, want %s", after.BitableSyncedHash, wantHash)
	}
	if res.Pulled != 1 {
		t.Fatalf("Pulled = %d, want 1 (%+v)", res.Pulled, res)
	}
	foundWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "recBad") && strings.Contains(w, "任务名") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("缺字段记录应告警: %+v", res.Warnings)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	if len(tasks) != 1 {
		t.Fatalf("缺字段记录不应落库: %+v", tasks)
	}
}

// —— 例 4：本地脏改 + 远端同记录也改 → Conflicts 长度 1 且本地值=远端值 ————————————————

func TestSyncConflictLocalDirtyAndRemoteChanged(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "原始标题")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// 本地脏改（未同步）
	newTitle := "本地改"
	if _, err := s.UpdateTask(tk.ID, store.TaskChanges{Title: &newTitle}, actor, nil); err != nil {
		t.Fatal(err)
	}
	// 远端同一记录也被改
	remote := map[string]any{
		"任务名": "远端改", "状态": "todo", "负责人": "tester",
		"优先级": "3", "预估人日": float64(2), "已废弃": false,
	}
	fake.searchByTable = searchScript("tblTask", taskRecord("rec1", 3000, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("Conflicts = %#v, want 长度 1 (%+v)", res.Conflicts, res)
	}
	if !strings.Contains(res.Conflicts[0], fmt.Sprintf("task#%d", tk.ID)) ||
		!strings.Contains(res.Conflicts[0], "LWW") {
		t.Fatalf("冲突文案不符: %q", res.Conflicts[0])
	}
	after, _, err := s.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "远端改" {
		t.Fatalf("LWW 后本地值 = %q, want 远端值", after.Title)
	}
	// sync_conflict 活动已落库
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Action == "sync_conflict" && a.EntityType == "task" && a.EntityID == tk.ID {
			found = true
			if !strings.Contains(a.Detail, "本地改") || !strings.Contains(a.Detail, "远端改") {
				t.Fatalf("活动 detail 应含双方 JSON: %q", a.Detail)
			}
		}
	}
	if !found {
		t.Fatalf("sync_conflict 活动未落库: %+v", acts)
	}
}

// —— 例 5：远端 已废弃=true → 本地 archived=1 ————————————————————————————————————————

func TestSyncRemoteDeprecatedArchivesLocal(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "被队友删掉的任务")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	remote := map[string]any{
		"任务名": "被队友删掉的任务", "状态": "todo", "负责人": "tester",
		"优先级": "3", "预估人日": float64(2), "已废弃": true,
	}
	fake.searchByTable = searchScript("tblTask", taskRecord("rec1", 4000, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	after, _, err := s.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Archived {
		t.Fatalf("远端已废弃应置本地 archived=1: %+v", after)
	}
	if res.Deprecated != 1 {
		t.Fatalf("Deprecated = %d, want 1 (%+v)", res.Deprecated, res)
	}
}

// —— 例 6：本地 SoftDelete 后 sync → RecordUpdate(已废弃=true) 且 synced_hash="deprecated" —

func TestSyncLocalSoftDeletePushesTombstone(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "要删的任务")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	if err := s.SoftDeleteTask(tk.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(fake.updatedFields) != 1 || fake.updatedFields[0]["已废弃"] != true {
		t.Fatalf("应推送已废弃=true: %+v", fake.updatedFields)
	}
	after, _, err := s.GetTask(tk.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BitableSyncedHash != deprecatedHash {
		t.Fatalf("synced_hash = %q, want %q", after.BitableSyncedHash, deprecatedHash)
	}
	if res.Deprecated != 1 {
		t.Fatalf("Deprecated = %d, want 1 (%+v)", res.Deprecated, res)
	}
	// 终态：再 sync 不再重复推墓碑
	n := len(fake.calls)
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	for _, c := range fake.calls[n:] {
		if c.method == "RecordUpdate" || c.method == "RecordCreate" {
			t.Fatalf("墓碑应终态不再重推: %+v", fake.calls[n:])
		}
	}
}

// —— 补充：水位增量（老记录不再重处理）———————————————————————————————————————

func TestSyncWatermarkSkipsOldRecords(t *testing.T) {
	s, p, actor := syncEnv(t)
	fake := &fakeAPI{}
	ctx := context.Background()
	// 首轮：水位记到 1000
	remote := map[string]any{
		"任务名": "老任务", "状态": "todo", "优先级": "3", "预估人日": float64(1), "已废弃": false,
	}
	fake.searchByTable = searchScript("tblTask", taskRecord("recOld", 1000, remote))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首轮: %v", err)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	if len(tasks) != 1 {
		t.Fatalf("首轮全量应合入: %+v", tasks)
	}
	// 二轮：同一老记录（lmt 不变）不再处理，也不新增
	fake.searchByTable = searchScript("tblTask", taskRecord("recOld", 1000, remote))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二轮: %v", err)
	}
	if res.Pulled != 0 {
		t.Fatalf("老记录不应重复处理: %+v", res)
	}
	tasks, _ = s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	if len(tasks) != 1 {
		t.Fatalf("不应重复插入: %+v", tasks)
	}
}

// —— 补充：版本双向（本地 push + 远端合入）———————————————————————————————————————

func TestSyncVersionPushAndPull(t *testing.T) {
	s, p, actor := syncEnv(t)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0",
		TargetDate: "2026-10-01", Status: "planned"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{createIDs: map[string][]string{"tblVer": {"recV1"}}}
	ctx := context.Background()
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pushed != 1 || len(fake.createdFields) != 1 {
		t.Fatalf("版本应被推送: %+v fields=%+v", res, fake.createdFields)
	}
	f := fake.createdFields[0]
	if f["版本名"] != "v1.0" || f["目标日期"] != "2026-10-01" || f["状态"] != "planned" || f["备注"] != "" {
		t.Fatalf("版本字段不符: %+v", f)
	}
	// 远端新版本 → 本地创建
	fake.searchByTable = searchScript("tblVer", taskRecord("recV2", 5000, map[string]any{
		"版本名": "v2.0", "目标日期": "2026-11-01", "状态": "in_dev", "备注": "远端建的",
	}))
	res, err = SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端版本应合入: %+v", res)
	}
	vs, err := s.ListVersions(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var found *model.Version
	for i := range vs {
		if vs[i].Name == "v2.0" {
			found = &vs[i]
		}
	}
	if found == nil || found.Status != "in_dev" || found.Notes != "远端建的" {
		t.Fatalf("远端版本未合入: %+v", vs)
	}
}

// —— 补充：ContentHash 稳定性（键序无关 + 前 16 位）—————————————————————————————————

func TestContentHashStable(t *testing.T) {
	a := map[string]any{"任务名": "x", "状态": "todo", "预估人日": float64(2)}
	b := map[string]any{"预估人日": float64(2), "状态": "todo", "任务名": "x"}
	if ContentHash(a) != ContentHash(b) {
		t.Fatal("同内容不同键序必须同 hash")
	}
	if len(ContentHash(a)) != 16 {
		t.Fatalf("hash 应取前 16 位: %q", ContentHash(a))
	}
	c := map[string]any{"任务名": "y", "状态": "todo", "预估人日": float64(2)}
	if ContentHash(a) == ContentHash(c) {
		t.Fatal("不同内容必须不同 hash")
	}
}

// —— 补充：未绑定/错误传播 ————————————————————————————————————————————————————————

func TestSyncSearchErrorPropagates(t *testing.T) {
	s, p, actor := syncEnv(t)
	fake := &fakeAPI{searchErr: errors.New("网络断了")}
	if _, err := SyncProject(context.Background(), clientWith(fake), s, p, actor); err == nil {
		t.Fatal("pull 失败必须返回错误")
	}
}

// —— 评审修复 1：同一轮 pull 的版本与任务要立即关联（版本映射表需随合入刷新）—————————

func TestSyncPulledVersionLinksToPulledTask(t *testing.T) {
	s, p, _ := syncEnv(t)
	fake := &fakeAPI{searchByTable: map[string][]Record{
		"tblVer": {taskRecord("recV9", 2000, map[string]any{
			"版本名": "v9", "状态": "planned",
		})},
		"tblTask": {taskRecord("recT", 2001, map[string]any{
			"任务名": "关联任务", "状态": "todo", "优先级": "3", "预估人日": float64(1), "版本": "v9",
		})},
	}}

	res, err := SyncProject(context.Background(), clientWith(fake), s, p,
		mustMember(t, s, "tester"))
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 2 {
		t.Fatalf("Pulled = %d, want 2（版本+任务）(%+v)", res.Pulled, res)
	}
	vs, err := s.ListVersions(p.ID)
	if err != nil || len(vs) != 1 || vs[0].Name != "v9" {
		t.Fatalf("远端版本未合入: %+v err=%v", vs, err)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	if len(tasks) != 1 {
		t.Fatalf("远端任务未合入: %+v", tasks)
	}
	if tasks[0].VersionID != vs[0].ID {
		t.Fatalf("任务的 VersionID = %d, want 同轮合入的版本 %d（版本映射表必须随合入刷新）",
			tasks[0].VersionID, vs[0].ID)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "不在本地版本表") {
			t.Fatalf("同轮合入的版本不应再告警: %+v", res.Warnings)
		}
	}
}

// —— 评审修复 2：水位必须压在最早失败记录之前，下轮重试（成功行重处理走回声跳过）———

func TestSyncWatermarkCappedOnFailedMerge(t *testing.T) {
	s, p, _ := syncEnv(t)
	fake := &fakeAPI{}
	ctx := context.Background()

	// 第一轮：recOK 正常合入（lmt 2500）；recBad 缺任务名（lmt 2450）告警跳过
	bad := map[string]any{"状态": "todo", "优先级": "3", "预估人日": float64(2)}
	good := map[string]any{"任务名": "正常任务", "状态": "todo", "优先级": "3", "预估人日": float64(2)}
	fake.searchByTable = searchScript("tblTask",
		taskRecord("recOK", 2500, good), taskRecord("recBad", 2450, bad))
	res1, err := SyncProject(ctx, clientWith(fake), s, p, mustMember(t, s, "tester"))
	if err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if res1.Pulled != 1 || len(res1.Warnings) == 0 {
		t.Fatalf("第一轮应合入 1 条并告警 1 条: %+v", res1)
	}
	// 水位必须停在最早失败记录之前（2450-1），不得推进到 2500
	wm, found, err := GetSyncState(s, "pull_watermark:1:tasks")
	if err != nil || !found || wm != "2449" {
		t.Fatalf("水位 = %q found=%v err=%v, want 2449（压在失败记录之前）", wm, found, err)
	}

	// 第二轮：recBad 补全字段（lmt 不变，模拟本地阻塞解除），必须被重试合入
	fixed := map[string]any{"任务名": "补全的任务", "状态": "todo", "优先级": "3", "预估人日": float64(2)}
	fake.searchByTable = searchScript("tblTask",
		taskRecord("recOK", 2500, good), taskRecord("recBad", 2450, fixed))
	res2, err := SyncProject(ctx, clientWith(fake), s, p, mustMember(t, s, "tester"))
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if res2.Pulled != 1 {
		t.Fatalf("失败记录必须被下轮重试合入: %+v", res2)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	var merged bool
	for _, tk := range tasks {
		if tk.Title == "补全的任务" {
			merged = true
		}
	}
	if !merged {
		t.Fatalf("补全的记录未合入: %+v", tasks)
	}
}

// —— 评审修复 3：Create 成功但回填中断后，重跑必须复用既有远端记录（不重复建行）———

func TestSyncReusesPendingRecordAfterInterruptedBackfill(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "写周报")
	// 模拟中断态：远端记录已建（recX），本地 record_id 为空，仅 sync_state 里有 pending id
	if err := SetSyncState(s, pendingRecordKey("task", tk.ID), "recX"); err != nil {
		t.Fatal(err)
	}
	m, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	remote := TaskToFields(model.Task{Title: "写周报", Status: "todo", Priority: 3,
		EstimateDays: 2, AssigneeID: m.ID}, map[int64]string{m.ID: "tester"}, map[int64]string{})
	fake := &fakeAPI{searchByTable: searchScript("tblTask", taskRecord("recX", 3000, remote))}

	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// push：复用 pending id 走 RecordUpdate，绝不再 RecordCreate（防远端重复）
	if len(fake.createdFields) != 0 {
		t.Fatalf("重跑不得重复建行: created=%+v", fake.createdFields)
	}
	if len(fake.updatedFields) != 1 {
		t.Fatalf("应复用 pending id 走更新: %+v", fake.updatedFields)
	}
	// pull：远端 recX 匹配到本地行 → 自回声，不插重复行
	if res.SkippedEcho != 1 || res.Pulled != 0 {
		t.Fatalf("应自回声跳过: %+v", res)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	if len(tasks) != 1 || tasks[0].BitableRecordID != "recX" || len(tasks[0].BitableSyncedHash) != 16 {
		t.Fatalf("record_id/hash 未回填: %+v", tasks)
	}
	// pending 用完即清
	if v, found, err := GetSyncState(s, pendingRecordKey("task", tk.ID)); err != nil || (found && v != "") {
		t.Fatalf("pending id 未清除: v=%q found=%v err=%v", v, found, err)
	}
}

// —— E2E-3：远端覆盖"我已同步到飞书的修改"时补显式提示（不改 LWW 语义，仅 UX）—————
// 判定基准是 synced_at（本行上次与飞书收敛的时刻），不能用 pull 水位：BestEffort
// 的 autopush 走完整 SyncProject，其内嵌 pull 会把水位推进到晚于本地编辑时刻，
// 水位比较在真实主路径上永不触发。

// TestSyncOverwriteWarnsRemoteChangedAfterSyncedAt：本地改 + sync（synced_at 落下）→
// 远端（另一台机器）改同记录且 lmt 更新 → 再 sync 覆盖本地 → Warnings 含提示。
func TestSyncOverwriteWarnsRemoteChangedAfterSyncedAt(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "双机任务")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	tk = wantTaskSynced(t, s, tk.ID, "rec1")
	// 本地修改并同步（与 autopush 同效：synced_at 随此次 push 落下）
	localTitle := "本地改"
	if _, err := s.UpdateTask(tk.ID, store.TaskChanges{Title: &localTitle}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	// 远端另一台机器改了同一记录（lmt 晚于本地 synced_at）
	remote := map[string]any{
		"任务名": "远端改", "状态": "todo", "负责人": "tester",
		"优先级": "3", "预估人日": float64(2), "已废弃": false,
	}
	fake.searchByTable = searchScript("tblTask", taskRecord("rec1", time.Now().Unix()+10, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("三次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端修改应覆盖本地: %+v", res)
	}
	want := fmt.Sprintf("任务 #%d 本地改 被飞书侧更新覆盖（覆盖的是你已同步到飞书的修改）", tk.ID)
	for _, w := range res.Warnings {
		if w == want {
			return
		}
	}
	t.Fatalf("Warnings 应含 %q: %#v", want, res.Warnings)
}

// TestSyncOverwriteSilentWhenRemoteNotAfterSyncedAt：远端 lmt 不晚于本地 synced_at
// （正常时序不可能出现，防御性保护）→ 覆盖照常发生但无提示。
func TestSyncOverwriteSilentWhenRemoteNotAfterSyncedAt(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "稳定任务")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	tk = wantTaskSynced(t, s, tk.ID, "rec1")

	remote := map[string]any{
		"任务名": "稳定任务（远端改）", "状态": "todo", "负责人": "tester",
		"优先级": "3", "预估人日": float64(2), "已废弃": false,
	}
	fake.searchByTable = searchScript("tblTask", taskRecord("rec1", 1000, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端修改应照常覆盖本地: %+v", res)
	}
	after, _, err := s.GetTask(tk.ID)
	if err != nil || after.Title != "稳定任务（远端改）" {
		t.Fatalf("覆盖未落地: %+v err=%v", after, err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "被飞书侧更新覆盖") {
			t.Fatalf("远端不晚于 synced_at 不应告警: %#v", res.Warnings)
		}
	}
}

// TestSyncOverwriteSilentWhenSyncedAtMissing：synced_at 为空（同步元数据未回填过的行：
// 回填中断、旧库升级遗留）→ 无从判定"远端晚于我上次同步"，不告警（宁缺毋滥）。
func TestSyncOverwriteSilentWhenSyncedAtMissing(t *testing.T) {
	s, p, actor := syncEnv(t)
	tk := mustCreateTask(t, s, p.ID, actor, "中断任务")
	// 模拟"远端记录已建、本地元数据未回填"的过渡态：仅 sync_state 里有 pending id
	if err := SetSyncState(s, pendingRecordKey("task", tk.ID), "recX"); err != nil {
		t.Fatal(err)
	}
	remote := map[string]any{
		"任务名": "中断任务（远端改）", "状态": "todo", "负责人": "tester",
		"优先级": "3", "预估人日": float64(2), "已废弃": false,
	}
	// push 复用 pending id 但注入失败：同步元数据（含 synced_at）保持未回填
	fake := &fakeAPI{updateErr: errors.New("写入失败"),
		searchByTable: searchScript("tblTask", taskRecord("recX", time.Now().Unix()+10, remote))}

	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端修改应覆盖本地: %+v", res)
	}
	after, _, err := s.GetTask(tk.ID)
	if err != nil || after.Title != "中断任务（远端改）" {
		t.Fatalf("覆盖未落地: %+v err=%v", after, err)
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, "被飞书侧更新覆盖") {
			t.Fatalf("synced_at 为空不应告警: %#v", res.Warnings)
		}
	}
}

// —— 补充：TaskToFields/FieldsToTask 往返一致（回声判定的根基）———————————————————
func TestTaskFieldsRoundTrip(t *testing.T) {

	verIDToName := map[int64]string{7: "v1.0"}
	nameToID := map[string]int64{"tester": 3}
	tk := model.Task{Title: "T", Status: "in_progress", Priority: 1, EstimateDays: 2.5,
		StartDate: "2026-09-15", DueDate: "2026-09-20", AssigneeID: 3, VersionID: 7}
	fields := TaskToFields(tk, map[int64]string{3: "tester"}, verIDToName)
	got, missing, warns := FieldsToTask(fields, model.Task{}, nameToID, map[string]int64{"v1.0": 7})
	if len(missing) != 0 || len(warns) != 0 {
		t.Fatalf("完整字段不应有缺失/告警: %v %v", missing, warns)
	}
	fields2 := TaskToFields(got, map[int64]string{3: "tester"}, verIDToName)
	if !reflect.DeepEqual(fields, fields2) {
		t.Fatalf("往返不一致:\n got  %+v\n want %+v", fields2, fields)
	}
	if ContentHash(fields) != ContentHash(fields2) {
		t.Fatal("往返后 hash 必须一致（自回声判定的根基）")
	}
}
