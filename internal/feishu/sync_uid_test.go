package feishu

// sync_uid_test.go：需求全局身份（UID）同步链路的 TDD（v1.2）。
// 核心场景：双机各自建需求后共享同一 base，评审/bug/提测 的 需求ID 列按 UID 解析，
// 不再被本地恰好同号的无关需求错链；旧库行（uid=''）在推送前自动回填。

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

var wantUIDFormat = regexp.MustCompile(`^[0-9a-f]{32}$`)

// TestSyncRequirementPushesUID：需求推送字段含 需求UID（32 位十六进制），hash 随之稳定。
func TestSyncRequirementPushesUID(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "导出报表")
	fake := &fakeAPI{createIDs: map[string][]string{"tblReq": {"recR1"}}}

	if _, err := SyncProject(context.Background(), clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	r = wantRequirementSynced(t, s, r.ID, "recR1")
	if !wantUIDFormat.MatchString(r.UID) {
		t.Fatalf("创建应生成 32 位十六进制 UID: %q", r.UID)
	}
	uid := r.UID
	if got := fake.createdFields[0]["需求UID"]; got != uid {
		t.Fatalf("推送字段应含 需求UID: got %v want %q", got, uid)
	}
	// hash 按含 UID 的字段计算（与落库 hash 一致，回声判定成立）
	want := ContentHash(RequirementToFields(model.Requirement{
		Title: "导出报表", Description: "支持 CSV 导出", Status: "proposed",
		Priority: 2, OwnerID: r.OwnerID, UID: uid,
	}, map[int64]string{r.OwnerID: "tester"}))
	if r.BitableSyncedHash != want {
		t.Fatalf("synced_hash 应含 需求UID: got %s want %s", r.BitableSyncedHash, want)
	}
}

// TestSyncLegacyRequirementRowBackfillsUIDBeforePush：旧库行（uid=”）在同步路径
// 首次经过 list 时回填 UID 并随 push 落到远端列（幂等：重跑不再生成新值）。
func TestSyncLegacyRequirementRowBackfillsUIDBeforePush(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "旧库需求")
	// 模拟旧库行：清空 uid（UpdateRequirement 的 UID 通道写空串）
	empty := ""
	if _, err := s.UpdateRequirement(r.ID, store.RequirementChanges{UID: &empty}, actor, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{createIDs: map[string][]string{"tblReq": {"recR1"}}}
	ctx := context.Background()

	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	after, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !wantUIDFormat.MatchString(after.UID) {
		t.Fatalf("同步应回填 UID: %q", after.UID)
	}
	if got := fake.createdFields[0]["需求UID"]; got != after.UID {
		t.Fatalf("推送字段应带回填的 需求UID: got %v want %q", got, after.UID)
	}
	// 幂等：二次 sync 不换新值、无重复推送
	n := len(fake.calls)
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatal(err)
	}
	final, _, _ := s.GetRequirement(r.ID)
	if final.UID != after.UID {
		t.Fatalf("UID 回填必须幂等: %q vs %q", final.UID, after.UID)
	}
	for _, c := range fake.calls[n:] {
		if c.method == "RecordCreate" || c.method == "RecordUpdate" {
			t.Fatalf("回填收敛后不应再推送: %+v", fake.calls[n:])
		}
	}
}

// TestSyncDualMachineReviewLinksByUID（双机正确性核心）：
// A 机建 requirement(uid=X) + review(引用 X) 并推送；B 机 fresh store 里先自建一条
// 需求（本地 id 恰好同为 1，但 uid 不同），再 pull A 的两条记录（需求在前）——
// B 的评审必须指向 B 本地那条 uid=X 的需求，而不是本地恰好同 id 的其他需求。
func TestSyncDualMachineReviewLinksByUID(t *testing.T) {
	sA, pA, actorA := syncEnv(t)
	sB, pB, actorB := syncEnv(t)
	ctx := context.Background()

	// A 机：建需求 + 关联评审，推送到共享表
	rA := mustCreateRequirement(t, sA, pA.ID, actorA, "共享需求")
	if _, err := sA.CreateReview(model.Review{
		ProjectID: pA.ID, RequirementID: rA.ID, Kind: "requirement", Conclusion: "pending",
	}, actorA, nil); err != nil {
		t.Fatal(err)
	}
	fakeA := &fakeAPI{}
	if _, err := SyncProject(ctx, clientWith(fakeA), sA, pA, actorA); err != nil {
		t.Fatalf("A sync: %v", err)
	}
	if len(fakeA.createdFields) < 2 {
		t.Fatalf("A 应推送需求+评审: %+v", fakeA.createdFields)
	}
	uidX := rA.UID
	if got := fakeA.createdFields[0]["需求UID"]; got != uidX {
		t.Fatalf("A 需求记录应带 需求UID: %v", got)
	}
	if got := fakeA.createdFields[1]["需求ID"]; got != uidX {
		t.Fatalf("A 评审记录的 需求ID 应为 UID: %v", got)
	}

	// B 机：先自建一条需求（本地 id=1，与 A 的需求 id 撞号，uid 不同）
	mine := mustCreateRequirement(t, sB, pB.ID, actorB, "B 自己的需求")
	if mine.ID != rA.ID {
		t.Fatalf("前置条件：两机需求本地 id 应撞号（%d vs %d）", mine.ID, rA.ID)
	}
	if mine.UID == uidX {
		t.Fatal("前置条件：两机需求 UID 必须不同")
	}
	// B pull：同一 base 的搜索脚本返回 A 的两条记录（需求记录在前）
	fakeB := &fakeAPI{searchByTable: map[string][]Record{
		"tblReq":    {taskRecord("recReqA", 2000, fakeA.createdFields[0])},
		"tblReview": {taskRecord("recRevA", 2001, fakeA.createdFields[1])},
	}}
	res, err := SyncProject(ctx, clientWith(fakeB), sB, pB, actorB)
	if err != nil {
		t.Fatalf("B sync: %v", err)
	}
	if res.Pulled != 2 {
		t.Fatalf("B 应合入需求+评审: %+v", res)
	}
	// B 本地那条 uid=X 的需求（pulled 行，而非本地恰好同 id 的 mine）
	pulled, found, err := sB.GetRequirementIDByUID(pB.ID, uidX)
	if err != nil || !found {
		t.Fatalf("B 应有 uid=X 的需求: found=%v err=%v", found, err)
	}
	pulledReq, _, _ := sB.GetRequirement(pulled)
	if pulledReq.Title != "共享需求" {
		t.Fatalf("uid=X 应指向拉入的共享需求: %+v", pulledReq)
	}
	rvs, err := sB.ListReviews(pB.ID, 0)
	if err != nil || len(rvs) != 1 {
		t.Fatalf("B 评审未合入: %+v err=%v", rvs, err)
	}
	if rvs[0].RequirementID != pulled {
		t.Fatalf("B 评审应按 UID 指向共享需求 #%d, got #%d（本地撞号需求 #%d）",
			pulled, rvs[0].RequirementID, mine.ID)
	}
}

// TestSyncLegacyRequirementIDNumericResolvedWithWarning：旧 base 的 需求ID 数字串
// （旧本地 id）在读取侧容忍解析一次并告警提示迁移；push 后以 UID 覆盖收敛。
func TestSyncLegacyRequirementIDNumericResolvedWithWarning(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "本地需求")
	fake := &fakeAPI{searchByTable: searchScript("tblReview", taskRecord("recRev", 2000, map[string]any{
		"评审类型": "requirement", "结论": "pending", "评审时间": "2026-09-14 09:00:00",
		"需求ID": r.ID, // 旧版本 pulse 写入的本地 id 数字串
	}))}
	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端评审应合入: %+v", res)
	}
	rvs, _ := s.ListReviews(p.ID, 0)
	if len(rvs) != 1 || rvs[0].RequirementID != r.ID {
		t.Fatalf("旧数字串应按本地 id 解析: %+v", rvs)
	}
	foundWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "旧本地 id") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("legacy 解析应告警提示迁移: %#v", res.Warnings)
	}
}

// TestSyncReviewUnknownUIDKeptLocalAndWarned：远端评审引用本机尚未拉到的需求 UID
// （如需求表合入失败被水位压住）→ 保留空关联并告警，待需求合入后下轮重试补链。
func TestSyncReviewUnknownUIDKeptLocalAndWarned(t *testing.T) {
	s, p, actor := syncEnv(t)
	fake := &fakeAPI{searchByTable: searchScript("tblReview", taskRecord("recRev", 2000, map[string]any{
		"评审类型": "requirement", "结论": "pending",
		"需求ID": "ffffffffffffffffffffffffffffffff",
	}))}
	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端评审应合入（引用置空）: %+v", res)
	}
	rvs, _ := s.ListReviews(p.ID, 0)
	if len(rvs) != 1 || rvs[0].RequirementID != 0 {
		t.Fatalf("未知 UID 应置空关联合入: %+v", rvs)
	}
	foundWarn := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "ffffffff") {
			foundWarn = true
		}
	}
	if !foundWarn {
		t.Fatalf("未知 UID 应告警: %#v", res.Warnings)
	}
}

// TestSyncRequirementUIDAdoptedOnPull：双机各自生成 UID 后共享同一条远端记录时，
// 本地行随 pull 采纳远端的全局身份（否则引用解析与唯一性永不分叉）。
func TestSyncRequirementUIDAdoptedOnPull(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "共同需求")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	localUID := r.UID
	remoteUID := "aabbccdd00112233aabbccdd00112233"
	if remoteUID == localUID {
		t.Fatal("前置条件：远端 UID 必须不同")
	}
	// 远端（另一台机器赢下 LWW）记录携带它的 UID
	remote := map[string]any{
		"需求名": "共同需求", "状态": "in_dev", "负责人": "tester",
		"优先级": "2", "描述": "支持 CSV 导出", "已废弃": false, "需求UID": remoteUID,
	}
	fake.searchByTable = searchScript("tblReq", taskRecord("rec1", time.Now().Unix()+10, remote))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.Pulled != 1 {
		t.Fatalf("远端应覆盖本地: %+v", res)
	}
	after, _, _ := s.GetRequirement(r.ID)
	if after.UID != remoteUID {
		t.Fatalf("本地应采纳远端全局身份: got %q want %q", after.UID, remoteUID)
	}
	// 收敛：同一远端内容再 sync 全回声（UID 采纳不得造成每轮重复合入）
	res2, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pulled != 0 || res2.SkippedEcho != 1 {
		t.Fatalf("采纳后应回声收敛: %+v", res2)
	}
}

// TestSyncUIDAdoptionRefreshesMapsForSameRoundReviews（审查修复回归）：
// requirementDef.applyRemote 采纳远端 UID 后必须刷新会话映射——同一轮 pull 中
// 后续评审记录引用该（新）UID 才能解析；被取代的旧 UID 保留为会话内别名，
// 同轮仍带旧 UID 的引用也照常解析。修复前：评审建行即空关联且永不补链
// （applyRemote 只写结论，fromFields 已解析出的引用被丢弃）。
func TestSyncUIDAdoptionRefreshesMapsForSameRoundReviews(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "迁移期的需求")
	fake := &fakeAPI{}
	ctx := context.Background()
	// 首轮：推送建共享记录（需求UID = 本机生成的 uidOld）
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首轮 sync: %v", err)
	}
	uidOld := r.UID
	uidNew := "aabbccdd00112233aabbccdd00112233"
	if uidNew == uidOld {
		t.Fatal("前置条件：远端 UID 必须不同")
	}
	// 模拟迁移期双机分叉收敛：远端记录被另一台机器 LWW 赢下并携带它的 UID uidNew；
	// 同一轮里还有两条评审——一条引用 uidNew、一条仍引用被取代的 uidOld。
	remoteReq := map[string]any{
		"需求名": "迁移期的需求", "状态": "in_dev", "负责人": "tester",
		"优先级": "2", "描述": "支持 CSV 导出", "已废弃": false, "需求UID": uidNew,
	}
	revNew := map[string]any{
		"评审类型": "requirement", "结论": "pending", "需求ID": uidNew, "已废弃": false,
	}
	revOld := map[string]any{
		"评审类型": "requirement", "结论": "pending", "需求ID": uidOld, "已废弃": false,
	}
	fake.searchByTable = map[string][]Record{
		"tblReq":    {taskRecord("rec1", time.Now().Unix()+10, remoteReq)},
		"tblReview": {taskRecord("recRevNew", 2001, revNew), taskRecord("recRevOld", 2002, revOld)},
	}
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("同轮 sync: %v", err)
	}
	after, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.UID != uidNew {
		t.Fatalf("需求应采纳远端全局身份: got %q want %q", after.UID, uidNew)
	}
	rvs, err := s.ListReviews(p.ID, 0)
	if err != nil || len(rvs) != 2 {
		t.Fatalf("两条评审都应合入: %+v err=%v", rvs, err)
	}
	for _, v := range rvs {
		if v.RequirementID != r.ID {
			t.Fatalf("同轮评审应解析到采纳后的需求 #%d, got #%d（记录 %s）", r.ID, v.RequirementID, v.BitableRecordID)
		}
	}
	for _, w := range res.Warnings {
		if strings.Contains(w, uidNew) || strings.Contains(w, "不在本地需求表") {
			t.Fatalf("同轮引用不应告警: %#v", res.Warnings)
		}
	}
}
