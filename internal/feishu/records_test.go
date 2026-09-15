package feishu

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// —— 断言辅助 ————————————————————————————————————————————

// blockSummary 把块序列压成 "block_type:文本" 摘要，便于整序断言（4=heading2、2=text、
// 12=bullet、17=todo）。
func blockSummary(t *testing.T, blocks []map[string]any) []string {
	t.Helper()
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, fmt.Sprintf("%d:%s", blockNum(t, b), blockText(t, b)))
	}
	return out
}

// wantSummary 断言块序列摘要逐项相等。
func wantSummary(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("块序列:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// docCalls 断言飞书调用序列恰为 DocCreate→BlockAppend（且写入的是新建文档），
// 返回 (文档标题, 模板块序列)。
func docCalls(t *testing.T, fake *fakeAPI) (string, []map[string]any) {
	t.Helper()
	if len(fake.calls) != 2 || fake.calls[0].method != "DocCreate" || fake.calls[1].method != "BlockAppend" {
		t.Fatalf("调用序列应为 DocCreate→BlockAppend, got %+v", fake.calls)
	}
	if fake.calls[1].args[0] != "docT" {
		t.Fatalf("BlockAppend 应写入新建文档 docT, got %+v", fake.calls[1].args)
	}
	return fake.calls[0].args[1], fake.blockBlocks[0]
}

// recordActor 创建测试操作者 tester。
func recordActor(t *testing.T, s *store.Store) model.Member {
	t.Helper()
	m, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// wantRecordDocActivity 断言项目内恰好一条指向该实体的 feishu_record_doc 活动。
func wantRecordDocActivity(t *testing.T, s *store.Store, projectID, entityID int64, entityType string) {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Action == "feishu_record_doc" && a.EntityType == entityType && a.EntityID == entityID {
			return
		}
	}
	t.Fatalf("no feishu_record_doc activity for %s#%d: %+v", entityType, entityID, acts)
}

// —— 模板断言 ————————————————————————————————————————————

// TestEnsureRequirementDocTemplate：需求文档标题、节序、字段行渲染自实体当前数据，
// 并回写 token + 落 feishu_record_doc 活动。
func TestEnsureRequirementDocTemplate(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	alice, err := s.GetOrCreateMember("alice", "human")
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateRequirement(model.Requirement{
		ProjectID: p.ID, Title: "支持导出", Description: "导出为 Excel",
		OwnerID: alice.ID, Status: "in_dev", Priority: 2,
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAPI{}
	token, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "requirement", r.ID, "tester")
	if err != nil {
		t.Fatalf("EnsureRecordDoc: %v", err)
	}
	if token != "docT" {
		t.Fatalf("token = %q, want docT", token)
	}
	title, blocks := docCalls(t, fake)
	if title != "需求 · 支持导出" {
		t.Fatalf("标题 = %q, want 需求 · 支持导出", title)
	}
	wantSummary(t, blockSummary(t, blocks), []string{
		"4:背景与描述",
		"12:导出为 Excel",
		"4:状态与负责人",
		"12:状态：in_dev",
		"12:负责人：alice", // owner 经成员表换名
		"12:优先级：2",
		"4:关联任务提示",
		"12:行动项请用 pulse task add --requirement <id> 跟踪",
	})

	saved, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未回写: %+v", saved)
	}
	wantRecordDocActivity(t, s, p.ID, r.ID, "requirement")
}

// TestEnsureReviewDocTemplate：评审纪要标题含 kind 与关联需求（无关联显示 -），
// 结论节为当前值占位，行动项为 todo 块。
func TestEnsureReviewDocTemplate(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "R"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.CreateReview(model.Review{
		ProjectID: p.ID, RequirementID: r.ID, Kind: "requirement",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAPI{}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "review", v.ID, "tester"); err != nil {
		t.Fatalf("EnsureRecordDoc: %v", err)
	}
	title, blocks := docCalls(t, fake)
	if want := "评审记录 · requirement · 需求#" + strconv.FormatInt(r.ID, 10); title != want {
		t.Fatalf("标题 = %q, want %q", title, want)
	}
	wantSummary(t, blockSummary(t, blocks), []string{
		"4:参与人与时间",
		"12:时间：" + v.HeldAt,
		"12:参与人：（协作填写）",
		"4:结论",
		"12:结论：pending", // 结论当前值占位
		"4:行动项",
		"17:（协作填写：评审行动项；需跟踪时可用 pulse task add 建任务）",
	})
	saved, _, err := s.GetReview(v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未回写: %+v", saved)
	}
	wantRecordDocActivity(t, s, p.ID, v.ID, "review")

	// 未关联需求：标题的 需求#<id|-> 显示 -
	v2, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "test"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake2 := &fakeAPI{}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake2), s, "review", v2.ID, "tester"); err != nil {
		t.Fatalf("EnsureRecordDoc(v2): %v", err)
	}
	title2, _ := docCalls(t, fake2)
	if title2 != "评审记录 · test · 需求#-" {
		t.Fatalf("标题 = %q, want 评审记录 · test · 需求#-", title2)
	}
}

// TestEnsureMeetingDocTemplate：会议纪要标题、时间/参会人占位、讨论要点 bullet 占位、
// 行动项 todo 块。
func TestEnsureMeetingDocTemplate(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "迭代评审会"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAPI{}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "meeting", m.ID, "tester"); err != nil {
		t.Fatalf("EnsureRecordDoc: %v", err)
	}
	title, blocks := docCalls(t, fake)
	if title != "会议纪要 · 迭代评审会" {
		t.Fatalf("标题 = %q, want 会议纪要 · 迭代评审会", title)
	}
	wantSummary(t, blockSummary(t, blocks), []string{
		"4:时间/参会人",
		"12:时间：" + m.HeldAt,
		"12:参会人：（协作填写）",
		"4:讨论要点",
		"12:（协作填写：讨论要点）",
		"4:行动项",
		"17:（协作填写：会议行动项；需跟踪时可用 pulse task add 建任务）",
	})
	saved, _, err := s.GetMeeting(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未回写: %+v", saved)
	}
	wantRecordDocActivity(t, s, p.ID, m.ID, "meeting")
}

// TestEnsureSubmissionDocTemplate：提测单标题含版本名（store 换名）与单号，
// 自检清单为三个固定 todo。
func TestEnsureSubmissionDocTemplate(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	qa, err := s.GetOrCreateMember("qa", "human")
	if err != nil {
		t.Fatal(err)
	}
	ver, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateTestSubmission(model.TestSubmission{
		ProjectID: p.ID, VersionID: ver.ID, TestOwnerID: qa.ID, Scope: "登录与导出",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAPI{}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "test_submission", sub.ID, "tester"); err != nil {
		t.Fatalf("EnsureRecordDoc: %v", err)
	}
	title, blocks := docCalls(t, fake)
	if want := fmt.Sprintf("提测单 · v1.0 · #%d", sub.ID); title != want {
		t.Fatalf("标题 = %q, want %q", title, want)
	}
	wantSummary(t, blockSummary(t, blocks), []string{
		"4:提测人/测试负责人",
		"12:提测人：tester",
		"12:测试负责人：qa",
		"4:范围",
		"12:登录与导出",
		"4:自检清单",
		"17:冒烟通过",
		"17:核心路径回归",
		"17:数据兼容",
		"4:风险",
		"12:（协作填写：风险与注意事项）",
	})
	saved, _, err := s.GetTestSubmission(sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未回写: %+v", saved)
	}
	wantRecordDocActivity(t, s, p.ID, sub.ID, "test_submission")
}

// TestEnsureReleaseDocTemplate：发版记录标题含版本名，检查清单为三个固定 todo，
// 状态与时间渲染当前值。
func TestEnsureReleaseDocTemplate(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	ver, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: ver.ID, Status: "testing"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAPI{}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "release", rel.ID, "tester"); err != nil {
		t.Fatalf("EnsureRecordDoc: %v", err)
	}
	title, blocks := docCalls(t, fake)
	if title != "发版记录 · v1.0" {
		t.Fatalf("标题 = %q, want 发版记录 · v1.0", title)
	}
	wantSummary(t, blockSummary(t, blocks), []string{
		"4:发布负责人",
		"12:发布负责人：tester", // 未显式指定负责人时归操作者
		"4:检查清单",
		"17:测试通过",
		"17:回滚方案确认",
		"17:发布公告",
		"4:状态与时间",
		"12:状态：testing",
		"12:发布时间：未发布",
	})
	saved, _, err := s.GetRelease(rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未回写: %+v", saved)
	}
	wantRecordDocActivity(t, s, p.ID, rel.ID, "release")
}

// —— 幂等 / 降级 / 错误路径 ——————————————————————————————————

// TestEnsureRecordDocIdempotent：已有 token 时直接原样返回，零飞书调用、不追加活动。
func TestEnsureRecordDocIdempotent(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "周会"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	c := clientWith(fake)
	tok1, err := EnsureRecordDoc(context.Background(), c, s, "meeting", m.ID, "tester")
	if err != nil {
		t.Fatalf("首次 EnsureRecordDoc: %v", err)
	}
	n := len(fake.calls)

	tok2, err := EnsureRecordDoc(context.Background(), c, s, "meeting", m.ID, "tester")
	if err != nil {
		t.Fatalf("二次 EnsureRecordDoc: %v", err)
	}
	if tok2 != tok1 {
		t.Fatalf("二次返回 token = %q, want 原样 %q", tok2, tok1)
	}
	if len(fake.calls) != n {
		t.Fatalf("幂等路径产生了新调用: %+v", fake.calls[n:])
	}
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, a := range acts {
		if a.Action == "feishu_record_doc" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("feishu_record_doc 活动 = %d, want 1: %+v", count, acts)
	}
}

// TestEnsureRecordDocUnconfigured：c 为 nil（未配置飞书）时返回引导性错误——
// 零调用、不写 token、不落活动，由调用方降级。
func TestEnsureRecordDocUnconfigured(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "R"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	_ = fake // nil client 下 fake 无法注入；零调用由"无 client"结构保证
	_, err = EnsureRecordDoc(context.Background(), nil, s, "requirement", r.ID, "tester")
	if err == nil || !strings.Contains(err.Error(), "未配置飞书") {
		t.Fatalf("want 未配置飞书引导错误, got %v", err)
	}
	saved, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "" {
		t.Fatalf("未配置时不得写回 token: %+v", saved)
	}
	acts, err := s.ActivitiesInWindow(p.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Action == "feishu_record_doc" {
			t.Fatalf("未配置时不得落 feishu_record_doc 活动: %+v", acts)
		}
	}
}

// TestEnsureRecordDocBlockAppendFailsStillBindsToken：DocCreate 成功但 BlockAppend
// 失败时仍回写 token（宁可指向半成品文档也不要孤儿增殖——token 未写回时重试会再建
// 一个新文档），错误文案带上下文提示"可能不完整"与文档 id。
func TestEnsureRecordDocBlockAppendFailsStillBindsToken(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "R"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{blockErr: errors.New("块校验失败")}
	_, err = EnsureRecordDoc(context.Background(), clientWith(fake), s, "requirement", r.ID, "tester")
	if err == nil || !strings.Contains(err.Error(), "可能不完整") {
		t.Fatalf("want 含「可能不完整」的错误, got %v", err)
	}
	if !strings.Contains(err.Error(), "docT") {
		t.Fatalf("错误应带文档 id 上下文, got %v", err)
	}
	saved, _, err := s.GetRequirement(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("token 未回写（重试将再建孤儿文档）: %+v", saved)
	}
	// 已关联：幂等重入直接返回既有 token，不再产生新的 DocCreate
	fake2 := &fakeAPI{}
	tok, err := EnsureRecordDoc(context.Background(), clientWith(fake2), s, "requirement", r.ID, "tester")
	if err != nil || tok != "docT" {
		t.Fatalf("重入应幂等返回 docT, got %q err=%v", tok, err)
	}
	if len(fake2.calls) != 0 {
		t.Fatalf("已绑定路径不应有飞书调用: %+v", fake2.calls)
	}
}

// TestEnsureRecordDocErrors：实体不存在、未知实体类型均报错且零飞书调用。
func TestEnsureRecordDocErrors(t *testing.T) {
	s, _ := openStore(t)
	recordActor(t, s)
	fake := &fakeAPI{}

	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "requirement", 999, "tester"); err == nil ||
		!strings.Contains(err.Error(), "需求不存在") {
		t.Fatalf("want 需求不存在 error, got %v", err)
	}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "meeting", 999, "tester"); err == nil ||
		!strings.Contains(err.Error(), "会议不存在") {
		t.Fatalf("want 会议不存在 error, got %v", err)
	}
	if _, err := EnsureRecordDoc(context.Background(), clientWith(fake), s, "bug", 1, "tester"); err == nil ||
		!strings.Contains(err.Error(), "未知实体类型") {
		t.Fatalf("want 未知实体类型 error, got %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("错误路径不应有飞书调用: %+v", fake.calls)
	}
}

// TestVersionDisplayName：版本名换名与占位（0=未指定，查不到=版本#<id>）。
func TestVersionDisplayName(t *testing.T) {
	s, p := openStore(t)
	actor := recordActor(t, s)
	ver, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v2.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := versionDisplayName(s, ver.ID); err != nil || got != "v2.0" {
		t.Fatalf("versionDisplayName(%d) = %q, %v", ver.ID, got, err)
	}
	if got, err := versionDisplayName(s, 0); err != nil || got != "未指定" {
		t.Fatalf("versionDisplayName(0) = %q, %v", got, err)
	}
	if got, err := versionDisplayName(s, 999); err != nil || got != "版本#999" {
		t.Fatalf("versionDisplayName(999) = %q, %v", got, err)
	}
}
