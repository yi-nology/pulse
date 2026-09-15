package feishu

// sync_entities_test.go：六实体同步的 fakeAPI 脚本化场景（v1.1 Task 5 TDD）。
// 深度覆盖：需求（push 新建/回声/远端覆盖警告/双端墓碑/双机互推互拉）、
// bug（版本按名解析/LWW 冲突）、提测单（水位封顶重试/pending 幂等/状态流转）；
// 评审/会议/发版按 create+回声+墓碑轻量覆盖（评审另验结论经 UpdateReviewConclusion
// 合入并落活动；会议另验只读语义）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// mustCreateRequirement 建一个已指派 tester 的需求（优先级 2、含描述）。
func mustCreateRequirement(t *testing.T, s *store.Store, projectID int64, actor model.Member, title string) model.Requirement {
	t.Helper()
	m, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequirement(model.Requirement{
		ProjectID: projectID, Title: title, Description: "支持 CSV 导出",
		Status: "proposed", Priority: 2, OwnerID: m.ID,
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// wantRequirementSynced 断言需求的 record_id 回填并返回该行。
func wantRequirementSynced(t *testing.T, s *store.Store, id int64, wantRecordID string) model.Requirement {
	t.Helper()
	r, found, err := s.GetRequirement(id)
	if err != nil || !found {
		t.Fatalf("reload requirement %d: found=%v err=%v", id, found, err)
	}
	if r.BitableRecordID != wantRecordID {
		t.Fatalf("record_id = %q, want %q", r.BitableRecordID, wantRecordID)
	}
	return r
}

// —— 需求：push 新建 → RecordCreate 字段正确、record_id/hash/synced_at 回填 ————————

func TestSyncRequirementPushCreatesRecordAndBackfills(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "导出报表")
	fake := &fakeAPI{createIDs: map[string][]string{"tblReq": {"recR1"}}}

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
	if f["需求名"] != "导出报表" || f["状态"] != "proposed" || f["负责人"] != "tester" ||
		f["优先级"] != "2" || f["描述"] != "支持 CSV 导出" || f["已废弃"] != false {
		t.Fatalf("推送字段不符: %+v", f)
	}
	r = wantRequirementSynced(t, s, r.ID, "recR1")
	if len(r.BitableSyncedHash) != 16 {
		t.Fatalf("synced_hash 未按 16 位落库: %q", r.BitableSyncedHash)
	}
	if r.SyncedAt == "" {
		t.Fatal("synced_at 未随 mark synced 落库（覆盖警告的判定基准）")
	}
	want := ContentHash(RequirementToFields(model.Requirement{
		Title: "导出报表", Description: "支持 CSV 导出", Status: "proposed",
		Priority: 2, OwnerID: 1,
	}, map[int64]string{1: "tester"}))
	if r.BitableSyncedHash != want {
		t.Fatalf("synced_hash = %s, want %s", r.BitableSyncedHash, want)
	}
}

// —— 需求：push 后立即 pull 同一条 → 自回声跳过 + per-table 水位 ————————————————

func TestSyncRequirementEchoSkipped(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "导出报表")
	fake := &fakeAPI{}
	ctx := context.Background()

	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	r = wantRequirementSynced(t, s, r.ID, "rec1")
	fake.searchByTable = searchScript("tblReq",
		taskRecord("rec1", time.Now().Unix(), fake.createdFields[0]))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.SkippedEcho != 1 || res.Pulled != 0 || res.Pushed != 0 {
		t.Fatalf("应自回声跳过: %+v", res)
	}
	if wm, found, err := GetSyncState(s, "pull_watermark:1:requirements"); err != nil || !found || wm == "" {
		t.Fatalf("需求表水位未按表名命名空间写入: wm=%q found=%v err=%v", wm, found, err)
	}
}

// —— 需求：远端覆盖（含状态流转）→ 本地合入 + synced_at 覆盖警告 + update_status 活动 —

func TestSyncRequirementRemoteOverwriteWarns(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "导出报表")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	r = wantRequirementSynced(t, s, r.ID, "rec1")
	// 本地改标题并同步（synced_at 随 push 落下）
	newTitle := "导出报表（本地改）"
	if _, err := s.UpdateRequirement(r.ID, store.RequirementChanges{Title: &newTitle}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	// 远端（另一台机器）改了状态与标题，lmt 晚于本地 synced_at
	remote := map[string]any{
		"需求名": "导出报表（远端改）", "状态": "in_dev", "负责人": "tester",
		"优先级": "1", "描述": "支持 CSV 导出", "已废弃": false,
	}
	fake.searchByTable = searchScript("tblReq", taskRecord("rec1", time.Now().Unix()+10, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("三次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端修改应覆盖本地: %+v", res)
	}
	after, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "导出报表（远端改）" || after.Status != "in_dev" || after.Priority != 1 {
		t.Fatalf("远端值未覆盖本地: %+v", after)
	}
	want := fmt.Sprintf("需求 #%d 导出报表（本地改） 被飞书侧更新覆盖（覆盖的是你已同步到飞书的修改）", r.ID)
	foundWarn := false
	for _, w := range res.Warnings {
		if w == want {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("Warnings 应含 %q: %#v", want, res.Warnings)
	}
	// 状态流转落 update_status 活动（entity_type=requirement）
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	foundAct := false
	for _, a := range acts {
		if a.Action == "update_status" && a.EntityType == "requirement" && a.EntityID == r.ID {
			foundAct = true
		}
	}
	if !foundAct {
		t.Fatalf("远端状态流转应落 update_status 活动: %+v", acts)
	}
}

// —— 需求：本地软删 → 推墓碑 → 终态不再重推；远端墓碑 → 本地归档 ————————————————

func TestSyncRequirementTombstonesBothWays(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "要删的需求")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// 本地软删 → push 墓碑
	if err := s.SoftDeleteSyncEntity("requirement", r.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(fake.updatedFields) != 1 || fake.updatedFields[0]["已废弃"] != true {
		t.Fatalf("应推送已废弃=true: %+v", fake.updatedFields)
	}
	after, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BitableSyncedHash != deprecatedHash || res.Deprecated != 1 {
		t.Fatalf("墓碑终态未落: hash=%q res=%+v", after.BitableSyncedHash, res)
	}
	// 终态：再 sync 不再重推
	n := len(fake.calls)
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	for _, c := range fake.calls[n:] {
		if c.method == "RecordUpdate" || c.method == "RecordCreate" {
			t.Fatalf("墓碑应终态不再重推: %+v", fake.calls[n:])
		}
	}
}

func TestSyncRequirementRemoteTombstoneArchives(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "被队友删掉的需求")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	remote := map[string]any{
		"需求名": "被队友删掉的需求", "状态": "proposed", "负责人": "tester",
		"优先级": "2", "描述": "支持 CSV 导出", "已废弃": true,
	}
	fake.searchByTable = searchScript("tblReq", taskRecord("rec1", 4000, remote))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	after, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Archived || res.Deprecated != 1 {
		t.Fatalf("远端已废弃应置本地 archived: %+v res=%+v", after, res)
	}
	// 归档后的行下轮 push 补推本地墓碑终态（record_id 存在且 hash != deprecated）
	fake.searchByTable = nil
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	updated := false
	for _, f := range fake.updatedFields {
		if f["已废弃"] == true {
			updated = true
		}
	}
	if !updated {
		t.Fatalf("归档行应向远端补推墓碑: %+v", fake.updatedFields)
	}
}

// —— 双机流：p1 建需求 → p2 拉入 → p2 改状态推回 → p1 拉回收敛（无冲突）——————————

func TestSyncRequirementDualMachineFlow(t *testing.T) {
	// 两块独立库共用同一组假表
	sA, pA, actorA := syncEnv(t)
	sB, pB, actorB := syncEnv(t)
	fake := &fakeAPI{}
	ctx := context.Background()

	// p1 建需求并推送
	rA := mustCreateRequirement(t, sA, pA.ID, actorA, "多机需求")
	if _, err := SyncProject(ctx, clientWith(fake), sA, pA, actorA); err != nil {
		t.Fatalf("p1 首次 sync: %v", err)
	}
	if len(fake.createdFields) != 1 {
		t.Fatalf("p1 应推送 1 条需求: %+v", fake.createdFields)
	}
	pushedFields := fake.createdFields[0]

	// p2 拉入（负责人 tester 由 ensureMember 自动落 p2 成员表）
	fake2 := &fakeAPI{searchByTable: searchScript("tblReq", taskRecord("rec1", 2000, pushedFields))}
	res, err := SyncProject(ctx, clientWith(fake2), sB, pB, actorB)
	if err != nil {
		t.Fatalf("p2 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("p2 应合入远端需求: %+v", res)
	}
	rsB, err := sB.ListRequirements(pB.ID, "")
	if err != nil || len(rsB) != 1 || rsB[0].Title != "多机需求" {
		t.Fatalf("p2 未建本地行: %+v err=%v", rsB, err)
	}
	if rsB[0].OwnerID == 0 {
		t.Fatalf("p2 应按名解析负责人: %+v", rsB[0])
	}
	rB := rsB[0]

	// p2 改状态并推送（RecordUpdate 到同一远端记录）
	if _, err := sB.UpdateRequirement(rB.ID, store.RequirementChanges{
		Status: func(v string) *string { return &v }("in_dev"),
	}, actorB, nil); err != nil {
		t.Fatal(err)
	}
	fake2.searchByTable = nil
	if _, err := SyncProject(ctx, clientWith(fake2), sB, pB, actorB); err != nil {
		t.Fatalf("p2 二次 sync: %v", err)
	}
	if len(fake2.updatedFields) != 1 || fake2.updatedFields[0]["状态"] != "in_dev" {
		t.Fatalf("p2 应推回状态修改: %+v", fake2.updatedFields)
	}
	remoteFields := fake2.updatedFields[0]

	// p1 拉回收敛：状态 in_dev、无冲突
	fake.searchByTable = searchScript("tblReq", taskRecord("rec1", 3000, remoteFields))
	res, err = SyncProject(ctx, clientWith(fake), sA, pA, actorA)
	if err != nil {
		t.Fatalf("p1 二次 sync: %v", err)
	}
	if res.Pulled != 1 || len(res.Conflicts) != 0 {
		t.Fatalf("p1 应无冲突收敛: %+v", res)
	}
	after, _, err := sA.GetRequirement(rA.ID)
	if err != nil || after.Status != "in_dev" {
		t.Fatalf("p1 未收敛: %+v err=%v", after, err)
	}
}

// —— bug：push 字段（严重级/发现版本按名）+ 远端合入（成员/版本解析）————————————————

func TestSyncBugPushAndPullWithResolution(t *testing.T) {
	s, p, actor := syncEnv(t)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", Status: "in_dev"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	qa := mustMember(t, s, "qa")
	vs, _ := s.ListVersions(p.ID)
	if _, err := s.CreateBug(model.Bug{
		ProjectID: p.ID, Title: "登录崩溃", Severity: 1, Status: "fixing",
		AssigneeID: qa.ID, FoundVersionID: vs[0].ID,
	}, actor, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{createIDs: map[string][]string{"tblBug": {"recB1"}}}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// createdFields[0] 是版本、[1] 才是 bug（push 先版本后实体）
	if len(fake.createdFields) != 2 {
		t.Fatalf("版本+bug 应被推送: %+v", fake.createdFields)
	}
	f := fake.createdFields[1]
	if f["标题"] != "登录崩溃" || f["严重级"] != "1" || f["状态"] != "fixing" ||
		f["负责人"] != "qa" || f["发现版本"] != "v1.0" {
		t.Fatalf("bug 推送字段不符: %+v", f)
	}

	// 远端新 bug：负责人 tester 由 ensureMember 落表、发现版本按名解析到本地 v1.0
	remote := map[string]any{
		"标题": "列表偶现空白", "严重级": "3", "状态": "open",
		"负责人": "tester", "发现版本": "v1.0", "已废弃": false,
	}
	fake.searchByTable = searchScript("tblBug", taskRecord("recB2", 5000, remote))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端 bug 应合入: %+v", res)
	}
	bs, err := s.ListBugs(p.ID, store.BugFilter{})
	if err != nil || len(bs) != 2 {
		t.Fatalf("bug 未合入: %+v err=%v", bs, err)
	}
	var merged *model.Bug
	for i := range bs {
		if bs[i].Title == "列表偶现空白" {
			merged = &bs[i]
		}
	}
	if merged == nil || merged.Severity != 3 || merged.Status != "open" {
		t.Fatalf("远端 bug 字段不符: %+v", merged)
	}
	if merged.FoundVersionID != vs[0].ID {
		t.Fatalf("发现版本未按名解析: %+v", merged)
	}
	if merged.AssigneeID == 0 {
		t.Fatalf("负责人未按名解析: %+v", merged)
	}
}

// —— bug：本地脏改 + 远端同改 → LWW 冲突登记、远端覆盖、sync_conflict 活动 ————————

func TestSyncBugConflictLWW(t *testing.T) {
	s, p, actor := syncEnv(t)
	b, err := s.CreateBug(model.Bug{ProjectID: p.ID, Title: "原始标题", Severity: 2, Status: "open"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// 本地脏改
	fixing := "fixing"
	if _, err := s.UpdateBug(b.ID, store.BugChanges{Status: &fixing}, actor, nil); err != nil {
		t.Fatal(err)
	}
	// 远端同记录也改
	remote := map[string]any{
		"标题": "远端改", "严重级": "2", "状态": "verified", "已废弃": false,
	}
	fake.searchByTable = searchScript("tblBug", taskRecord("rec1", 3000, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("Conflicts = %#v, want 1 (%+v)", res.Conflicts, res)
	}
	if !strings.Contains(res.Conflicts[0], fmt.Sprintf("bug#%d", b.ID)) ||
		!strings.Contains(res.Conflicts[0], "LWW") {
		t.Fatalf("冲突文案不符: %q", res.Conflicts[0])
	}
	after, _, err := s.GetBug(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Title != "远端改" || after.Status != "verified" {
		t.Fatalf("LWW 后本地值应等于远端值: %+v", after)
	}
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Action == "sync_conflict" && a.EntityType == "bug" && a.EntityID == b.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sync_conflict 活动未落库: %+v", acts)
	}
}

// —— 提测单：缺版本字段压住水位 → 补全后下轮重试合入（含提测人按名解析）——————————

func TestSyncSubmissionWatermarkCappedThenRetried(t *testing.T) {
	s, p, actor := syncEnv(t)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", Status: "in_dev"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()

	// 第一轮：recOK 正常合入（lmt 2500）；recBad 缺版本列（lmt 2450）告警跳过
	good := map[string]any{"版本": "v1.0", "状态": "submitted", "提测人": "tester", "已废弃": false}
	bad := map[string]any{"状态": "draft", "已废弃": false}
	fake.searchByTable = searchScript("tblSubmit",
		taskRecord("recOK", 2500, good), taskRecord("recBad", 2450, bad))
	res1, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("第一轮: %v", err)
	}
	if res1.Pulled != 1 || len(res1.Warnings) == 0 {
		t.Fatalf("第一轮应合入 1 条并告警 1 条: %+v", res1)
	}
	wm, found, err := GetSyncState(s, "pull_watermark:1:test_submissions")
	if err != nil || !found || wm != "2449" {
		t.Fatalf("提测表水位 = %q found=%v err=%v, want 2449（压在最早失败记录之前）", wm, found, err)
	}
	// 合入行：提测人按名解析、submitted_at 由 store 依状态补记
	subs, err := s.ListTestSubmissions(p.ID, 0)
	if err != nil || len(subs) != 1 {
		t.Fatalf("第一轮合入结果: %+v err=%v", subs, err)
	}
	if subs[0].SubmittedBy == 0 || subs[0].SubmittedAt == "" {
		t.Fatalf("提测人/提交时刻未落: %+v", subs[0])
	}

	// 第二轮：recBad 补全字段（lmt 不变），必须被重试合入
	fixed := map[string]any{"版本": "v1.0", "状态": "testing", "提测人": "tester", "已废弃": false}
	fake.searchByTable = searchScript("tblSubmit",
		taskRecord("recOK", 2500, good), taskRecord("recBad", 2450, fixed))
	res2, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("第二轮: %v", err)
	}
	if res2.Pulled != 1 {
		t.Fatalf("失败记录必须被下轮重试合入: %+v", res2)
	}
	subs, _ = s.ListTestSubmissions(p.ID, 0)
	if len(subs) != 2 {
		t.Fatalf("补全的记录未合入: %+v", subs)
	}
}

// —— 提测单：远端状态流转 → 本地经 UpdateTestSubmission 合入（派生时间戳不重置）——————

func TestSyncSubmissionRemoteStatusFlow(t *testing.T) {
	s, p, actor := syncEnv(t)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", Status: "in_dev"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.ListVersions(p.ID)
	sub, err := s.CreateTestSubmission(model.TestSubmission{
		ProjectID: p.ID, VersionID: vs[0].ID, Status: "submitted", Scope: "核心路径",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	subBefore, _, _ := s.GetTestSubmission(sub.ID)
	// 远端流转到 testing
	remote := SubmissionToFields(model.TestSubmission{
		VersionID: vs[0].ID, Status: "testing", SubmittedBy: actor.ID, Scope: "核心路径",
	}, map[int64]string{actor.ID: "tester"}, map[int64]string{vs[0].ID: "v1.0"})
	fake.searchByTable = searchScript("tblSubmit", taskRecord("rec1", time.Now().Unix()+10, remote))

	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端流转应合入: %+v", res)
	}
	after, _, err := s.GetTestSubmission(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != "testing" {
		t.Fatalf("状态未合入: %+v", after)
	}
	if after.SubmittedAt != subBefore.SubmittedAt || after.SubmittedAt == "" {
		t.Fatalf("submitted_at 派生值不应被远端合入重置: before=%q after=%q",
			subBefore.SubmittedAt, after.SubmittedAt)
	}
}

// —— 提测单：Create 成功但回填中断 → 重跑复用 pending 远端记录（不重复建行）————————

func TestSyncSubmissionReusesPendingRecord(t *testing.T) {
	s, p, actor := syncEnv(t)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", Status: "in_dev"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.ListVersions(p.ID)
	sub, err := s.CreateTestSubmission(model.TestSubmission{
		ProjectID: p.ID, VersionID: vs[0].ID, Status: "draft",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟中断态：远端记录已建（recX），本地 record_id 为空，仅 sync_state 有 pending id
	if err := SetSyncState(s, pendingRecordKey("test_submission", sub.ID), "recX"); err != nil {
		t.Fatal(err)
	}
	remote := SubmissionToFields(model.TestSubmission{
		VersionID: vs[0].ID, Status: "draft", SubmittedBy: actor.ID,
	}, map[int64]string{actor.ID: "tester"}, map[int64]string{vs[0].ID: "v1.0"})
	fake := &fakeAPI{searchByTable: searchScript("tblSubmit", taskRecord("recX", 3000, remote))}

	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	// 提测表上不得有 RecordCreate（版本表的那次建行属正常推送）
	for _, c := range fake.calls {
		if c.method == "RecordCreate" && c.args[1] == "tblSubmit" {
			t.Fatalf("重跑不得重复建行: %+v", fake.calls)
		}
	}
	if len(fake.updatedFields) != 1 {
		t.Fatalf("应复用 pending id 走更新: %+v", fake.updatedFields)
	}
	if res.SkippedEcho != 1 || res.Pulled != 0 {
		t.Fatalf("应自回声跳过: %+v", res)
	}
	after, _, _ := s.GetTestSubmission(sub.ID)
	if after.BitableRecordID != "recX" || len(after.BitableSyncedHash) != 16 {
		t.Fatalf("record_id/hash 未回填: %+v", after)
	}
}

// —— 评审：结论经 UpdateReviewConclusion 合入并落活动；回声与远端墓碑 ————————

func TestSyncReviewPushEchoAndConclusionMerge(t *testing.T) {
	s, p, actor := syncEnv(t)
	rv, err := s.CreateReview(model.Review{
		ProjectID: p.ID, Kind: "requirement", Conclusion: "pending", HeldAt: "2026-09-15 10:00:00",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	if len(fake.createdFields) != 1 || fake.createdFields[0]["评审类型"] != "requirement" ||
		fake.createdFields[0]["结论"] != "pending" {
		t.Fatalf("评审应被推送: %+v", fake.createdFields)
	}
	// 回声
	fake.searchByTable = searchScript("tblReview",
		taskRecord("rec1", time.Now().Unix(), fake.createdFields[0]))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.SkippedEcho != 1 {
		t.Fatalf("应自回声跳过: %+v", res)
	}
	// 远端给出结论 → 经 UpdateReviewConclusion 合入 + update 活动
	remote := map[string]any{
		"评审类型": "requirement", "结论": "passed_with_notes",
		"评审时间": "2026-09-15 10:00:00", "需求ID": "", "已废弃": false,
	}
	fake.searchByTable = searchScript("tblReview", taskRecord("rec1", time.Now().Unix()+10, remote))
	res, err = SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("三次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("结论应合入: %+v", res)
	}
	after, _, err := s.GetReview(rv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Conclusion != "passed_with_notes" || after.Kind != "requirement" {
		t.Fatalf("结论合入结果不符: %+v", after)
	}
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range acts {
		if a.Action == "update" && a.EntityType == "review" && a.EntityID == rv.ID &&
			strings.Contains(a.Detail, "conclusion") {
			found = true
		}
	}
	if !found {
		t.Fatalf("结论变更应落 update 活动: %+v", acts)
	}
	// 远端墓碑 → 归档
	tomb := map[string]any{
		"评审类型": "requirement", "结论": "passed_with_notes",
		"评审时间": "2026-09-15 10:00:00", "需求ID": "", "已废弃": true,
	}
	fake.searchByTable = searchScript("tblReview", taskRecord("rec1", time.Now().Unix()+20, tomb))
	res, err = SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ = s.GetReview(rv.ID)
	if !after.Archived || res.Deprecated != 1 {
		t.Fatalf("远端墓碑应归档评审: %+v res=%+v", after, res)
	}
}

// —— 评审：远端新行引用本地不存在的需求 → 置空关联并合入（外键安全）————————————————

func TestSyncReviewUnknownRequirementRefCleared(t *testing.T) {
	s, p, actor := syncEnv(t)
	fake := &fakeAPI{searchByTable: searchScript("tblReview", taskRecord("recR", 2000, map[string]any{
		"评审类型": "release", "结论": "passed", "评审时间": "2026-09-14 09:00:00", "需求ID": "99",
	}))}
	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端评审应合入: %+v", res)
	}
	rvs, err := s.ListReviews(p.ID, 0)
	if err != nil || len(rvs) != 1 {
		t.Fatalf("评审未合入: %+v err=%v", rvs, err)
	}
	if rvs[0].RequirementID != 0 {
		t.Fatalf("不存在的需求引用应置空: %+v", rvs[0])
	}
	foundWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "99") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("置空关联应告警: %#v", res.Warnings)
	}
}

// —— 会议：create + 回声 + 墓碑（只读实体：远端内容修改被吸收，不重复告警）——————————

func TestSyncMeetingCreateEchoTombstone(t *testing.T) {
	s, p, actor := syncEnv(t)
	m, err := s.CreateMeeting(model.Meeting{
		ProjectID: p.ID, Title: "迭代评审会", HeldAt: "2026-09-15 09:00:00",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	if len(fake.createdFields) != 1 || fake.createdFields[0]["会议标题"] != "迭代评审会" {
		t.Fatalf("会议应被推送: %+v", fake.createdFields)
	}
	// 回声
	fake.searchByTable = searchScript("tblMeeting",
		taskRecord("rec1", time.Now().Unix(), fake.createdFields[0]))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedEcho != 1 {
		t.Fatalf("应自回声跳过: %+v", res)
	}
	// 远端墓碑 → 归档（会议表已废弃列双向生效）
	tomb := map[string]any{"会议标题": "迭代评审会", "时间": "2026-09-15 09:00:00", "已废弃": true}
	fake.searchByTable = searchScript("tblMeeting", taskRecord("rec1", time.Now().Unix()+10, tomb))
	res, err = SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	after, _, _ := s.GetMeeting(m.ID)
	if !after.Archived || res.Deprecated != 1 {
		t.Fatalf("远端墓碑应归档会议: %+v res=%+v", after, res)
	}
}

// TestSyncMeetingRemoteEditAbsorbedQuietly：会议无更新路径——远端标题修改在合入侧
// 因"创建即定"语义收敛为本地内容（回声判定），静默吸收且每轮不再重复合入。
func TestSyncMeetingRemoteEditAbsorbedQuietly(t *testing.T) {
	s, p, actor := syncEnv(t)
	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "站会", HeldAt: "09:00"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// 远端（别人手改）标题 + lmt 推进
	remote := map[string]any{"会议标题": "站会（飞书改名）", "时间": "09:00", "已废弃": false}
	fake.searchByTable = searchScript("tblMeeting", taskRecord("rec1", time.Now().Unix()+10, remote))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	after, _, _ := s.GetMeeting(m.ID)
	if after.Title != "站会" {
		t.Fatalf("会议标题应保持本地值（无更新路径）: %+v", after)
	}
	// 同一远端内容再 sync：不再重复合入（收敛），也不产生重复行为
	n := len(fake.calls)
	res2, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pulled != 0 {
		t.Fatalf("收敛后不应重复合入: %+v", res2)
	}
	for _, c := range fake.calls[n:] {
		if c.method == "RecordUpdate" || c.method == "RecordCreate" {
			t.Fatalf("收敛后不应有写调用: %+v", fake.calls[n:])
		}
	}
}

// —— 发版记录：create + 回声 + 状态合入（released_at 派生）+ 墓碑 ———————————————————

func TestSyncReleaseLifecycle(t *testing.T) {
	s, p, actor := syncEnv(t)
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", Status: "in_dev"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	vs, _ := s.ListVersions(p.ID)
	rel, err := s.CreateRelease(model.Release{
		ProjectID: p.ID, VersionID: vs[0].ID, Status: "preparing", ReleaseManagerID: actor.ID,
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	// createdFields[0] 是版本、[1] 才是发版记录（push 先版本后实体）
	if len(fake.createdFields) != 2 || fake.createdFields[1]["版本"] != "v1.0" ||
		fake.createdFields[1]["状态"] != "preparing" || fake.createdFields[1]["发布负责人"] != "tester" {
		t.Fatalf("发版应被推送: %+v", fake.createdFields)
	}
	// 回声
	fake.searchByTable = searchScript("tblRelease",
		taskRecord("rec1", time.Now().Unix(), fake.createdFields[1]))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res.SkippedEcho != 1 {
		t.Fatalf("应自回声跳过: %+v", res)
	}
	// 远端流转 released → 本地合入（released_at 由 store 派生补记）
	remote := map[string]any{
		"版本": "v1.0", "状态": "released", "发布负责人": "tester",
		"发布时间": "", "备注": "灰度 10%", "已废弃": false,
	}
	fake.searchByTable = searchScript("tblRelease", taskRecord("rec1", time.Now().Unix()+10, remote))
	res, err = SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("三次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端状态应合入: %+v", res)
	}
	after, _, _ := s.GetRelease(rel.ID)
	if after.Status != "released" || after.ReleasedAt == "" || after.Notes != "灰度 10%" {
		t.Fatalf("发版合入结果不符: %+v", after)
	}
	// 远端墓碑 → 归档
	tomb := map[string]any{
		"版本": "v1.0", "状态": "released", "发布负责人": "tester",
		"发布时间": "", "备注": "灰度 10%", "已废弃": true,
	}
	fake.searchByTable = searchScript("tblRelease", taskRecord("rec1", time.Now().Unix()+20, tomb))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	after, _, _ = s.GetRelease(rel.ID)
	if !after.Archived {
		t.Fatalf("远端墓碑应归档发版: %+v", after)
	}
}

// —— 旧项目（feishu_tables_json 为空）：只同步任务与版本，六实体静默跳过、无错误 ———

func TestSyncLegacyProjectSkipsEntities(t *testing.T) {
	s, p := openStore(t)
	p.FeishuBitableAppToken = "appT"
	p.FeishuTaskTableID = "tblTask"
	p.FeishuVersionTableID = "tblVer"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	actor, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	// 脏的需求行 + 脏的任务行
	if _, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "本地需求"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	tk := mustCreateTask(t, s, p.ID, actor, "本地任务")
	fake := &fakeAPI{createIDs: map[string][]string{"tblTask": {"recT1"}}}

	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("旧项目 sync 不应报错: %v", err)
	}
	if res.Pushed != 1 {
		t.Fatalf("只应推送任务: %+v", res)
	}
	// 需求行保持未同步（六实体跳过）
	rs, _ := s.ListRequirements(p.ID, "")
	if len(rs) != 1 || rs[0].BitableRecordID != "" {
		t.Fatalf("需求不应被推送: %+v", rs)
	}
	wantTaskSynced(t, s, tk.ID, "recT1")
	// API 调用只涉及任务/版本两张表（fakeAPI 的 args[1] 是 tableID）
	for _, c := range fake.calls {
		if (c.method == "RecordSearch" || c.method == "RecordCreate" || c.method == "RecordUpdate") &&
			c.args[1] != "tblTask" && c.args[1] != "tblVer" {
			t.Fatalf("不应触及六实体表 %s: %+v", c.args[1], fake.calls)
		}
	}
}

// —— 搜索失败在六实体路径同样传播（幂等可重跑）——————————————————————————————————

func TestSyncEntitySearchErrorPropagates(t *testing.T) {
	s, p, actor := syncEnv(t)
	fake := &fakeAPI{searchErr: errors.New("网络断了")}
	if _, err := SyncProject(context.Background(), clientWith(fake), s, p, actor); err == nil {
		t.Fatal("六实体 pull 失败必须返回错误")
	}
}
