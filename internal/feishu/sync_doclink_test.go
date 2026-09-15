package feishu

// sync_doclink_test.go：六实体"协作文档"超链接列的同步链路 TDD（v1.2）。
//   - push：token 非空 → RecordCreate/Update 携带链接列；
//   - pull 采纳语义：本地 feishu_doc_token 为空且远端有链接 → 提取 token 经
//     SetRecordDocToken 回写（单人协作文档跨机共享同一篇）；本地已有 token 忽略远端
//     （防覆盖）；采纳后二次 sync 全回声（无 flapping）；
//   - 会议 create-only：pull 建行时带上；既有行不采纳（远端差异按创建即定吸收）。

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

func docLinkField(token string) map[string]any {
	return map[string]any{"text": "打开文档", "link": "https://www.feishu.cn/docx/" + token}
}

// TestSyncDocLinkPushed：本地需求/评审带协作文档 token → 推送字段含链接列；
// 二次 sync（远端回读同一内容）全回声。
func TestSyncDocLinkPushed(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "有文档的需求")
	if err := s.SetRecordDocToken("requirement", r.ID, "docTOK", actor, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{createIDs: map[string][]string{"tblReq": {"recR1"}}}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	if got := fake.createdFields[0]["协作文档"]; !reflect.DeepEqual(got, docLinkField("docTOK")) {
		t.Fatalf("推送字段应含 协作文档 链接: %#v", got)
	}
	// 远端回读刚推送的内容 → 自回声（token 恒定，hash 稳定）
	fake.searchByTable = searchScript("tblReq",
		taskRecord("recR1", time.Now().Unix(), fake.createdFields[0]))
	res, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if res.SkippedEcho != 1 || res.Pulled != 0 {
		t.Fatalf("带链接列的内容应自回声: %+v", res)
	}
}

// TestSyncDocLinkAdoptedOnPull：本地链接行 token 为空、远端有链接 → 采纳远端文档
// （SetRecordDocToken 回写 + feishu_record_doc 活动）；二次 sync 全回声、无重复活动。
func TestSyncDocLinkAdoptedOnPull(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "远端建文档的需求")
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync（无 token 推送）: %v", err)
	}
	// 远端（另一台机器建了协作文档并把链接写到共享记录上）
	remote := map[string]any{
		"需求名": "远端建文档的需求", "状态": "proposed", "负责人": "tester",
		"优先级": "2", "描述": "支持 CSV 导出", "已废弃": false,
		"协作文档": docLinkField("docRemote"),
	}
	fake.searchByTable = searchScript("tblReq", taskRecord("rec1", time.Now().Unix()+10, remote))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	after, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.FeishuDocToken != "docRemote" {
		t.Fatalf("应采纳远端协作文档 token: %+v", after)
	}
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	adopts := 0
	for _, a := range acts {
		if a.Action == "feishu_record_doc" && a.EntityType == "requirement" && a.EntityID == r.ID {
			adopts++
		}
	}
	if adopts != 1 {
		t.Fatalf("采纳应恰好落一条 feishu_record_doc 活动: %d", adopts)
	}
	// 收敛：同一远端内容再 sync 全回声，不重复采纳（无 flapping）
	res2, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pulled != 0 || res2.SkippedEcho != 1 {
		t.Fatalf("采纳后应回声收敛: %+v", res2)
	}
	acts, _ = s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	adopts = 0
	for _, a := range acts {
		if a.Action == "feishu_record_doc" && a.EntityType == "requirement" && a.EntityID == r.ID {
			adopts++
		}
	}
	if adopts != 1 {
		t.Fatalf("二次 sync 不得重复采纳: %d", adopts)
	}
}

// TestSyncDocLinkLocalTokenWins：本地已有 token 时忽略远端链接（防覆盖、两份文档不互相
// 抢写）；远端其余字段照常合入，二次 sync 全回声。
func TestSyncDocLinkLocalTokenWins(t *testing.T) {
	s, p, actor := syncEnv(t)
	r := mustCreateRequirement(t, s, p.ID, actor, "本地已有文档")
	if err := s.SetRecordDocToken("requirement", r.ID, "docLocal", actor, nil); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	remote := map[string]any{
		"需求名": "本地已有文档（远端改）", "状态": "in_dev", "负责人": "tester",
		"优先级": "2", "描述": "支持 CSV 导出", "已废弃": false,
		"协作文档": docLinkField("docRemote"),
	}
	fake.searchByTable = searchScript("tblReq", taskRecord("rec1", time.Now().Unix()+10, remote))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	after, _, _ := s.GetRequirement(r.ID)
	if after.FeishuDocToken != "docLocal" {
		t.Fatalf("本地 token 不得被远端覆盖: %+v", after)
	}
	if after.Title != "本地已有文档（远端改）" || after.Status != "in_dev" {
		t.Fatalf("其余字段应照常合入: %+v", after)
	}
	// 收敛：同一远端内容再 sync 全回声
	res2, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pulled != 0 || res2.SkippedEcho != 1 {
		t.Fatalf("本地 token 胜出后应回声收敛: %+v", res2)
	}
}

// TestSyncDocLinkAdoptedOnPullCreate：全新拉入的六实体行在 create 时即带上远端协作文档
// （Create* 不含 token 列，经 SetRecordDocToken 补写）。以评审与会议（create-only）为代表。
func TestSyncDocLinkAdoptedOnPullCreate(t *testing.T) {
	s, p, actor := syncEnv(t)
	rev := map[string]any{
		"评审类型": "requirement", "结论": "pending", "评审时间": "2026-09-14 09:00:00",
		"需求ID": "", "已废弃": false, "协作文档": docLinkField("docRev"),
	}
	meet := map[string]any{
		"会议标题": "迭代评审会", "时间": "2026-09-14 10:00:00", "已废弃": false,
		"协作文档": docLinkField("docMeet"),
	}
	fake := &fakeAPI{searchByTable: map[string][]Record{
		"tblReview":  {taskRecord("recRev", 2000, rev)},
		"tblMeeting": {taskRecord("recMeet", 2001, meet)},
	}}
	res, err := SyncProject(context.Background(), clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Pulled != 2 {
		t.Fatalf("评审+会议应合入: %+v", res)
	}
	rvs, _ := s.ListReviews(p.ID, 0)
	ms, _ := s.ListMeetings(p.ID)
	if len(rvs) != 1 || rvs[0].FeishuDocToken != "docRev" {
		t.Fatalf("评审拉入行应采纳协作文档: %+v", rvs)
	}
	if len(ms) != 1 || ms[0].FeishuDocToken != "docMeet" {
		t.Fatalf("会议拉入行应采纳协作文档: %+v", ms)
	}
}

// TestSyncMeetingRemoteDocLinkAbsorbedQuietly：会议既有行（token 为空）+ 远端出现链接：
// 会议无更新路径，链接不采纳，差异按创建即定语义吸收——回声静默、无每轮噪音。
func TestSyncMeetingRemoteDocLinkAbsorbedQuietly(t *testing.T) {
	s, p, actor := syncEnv(t)
	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "站会"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ctx := context.Background()
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("首次 sync: %v", err)
	}
	remote := map[string]any{
		"会议标题": "站会", "时间": "", "已废弃": false,
		"协作文档": docLinkField("docElsewhere"),
	}
	fake.searchByTable = searchScript("tblMeeting", taskRecord("rec1", time.Now().Unix()+10, remote))
	if _, err := SyncProject(ctx, clientWith(fake), s, p, actor); err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	after, _, _ := s.GetMeeting(m.ID)
	if after.FeishuDocToken != "" {
		t.Fatalf("会议既有行不采纳远端链接（create-only）: %+v", after)
	}
	// 同一远端内容再 sync：回声静默（Pulled=0），无写调用
	n := len(fake.calls)
	res2, err := SyncProject(ctx, clientWith(fake), s, p, actor)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Pulled != 0 {
		t.Fatalf("应收敛不再重复合入: %+v", res2)
	}
	for _, w := range res2.Warnings {
		if strings.Contains(w, "站会") {
			t.Fatalf("收敛后不应有噪音告警: %#v", res2.Warnings)
		}
	}
	for _, c := range fake.calls[n:] {
		if c.method == "RecordUpdate" || c.method == "RecordCreate" {
			t.Fatalf("收敛后不应有写调用: %+v", fake.calls[n:])
		}
	}
}
