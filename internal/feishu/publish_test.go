package feishu

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// —— 断言辅助 ————————————————————————————————————————————

// blockNum 取块的 block_type 编号（官方 docx 枚举：2 text、4 heading2、12 bullet、17 todo）。
func blockNum(t *testing.T, b map[string]any) int {
	t.Helper()
	n, ok := b["block_type"].(int)
	if !ok {
		t.Fatalf("块缺少 block_type(int): %v", b)
	}
	return n
}

// blockText 提取块载荷内全部 text_run 内容的拼接（publish 只产文本块，测试足够）。
func blockText(t *testing.T, b map[string]any) string {
	t.Helper()
	for _, key := range []string{"text", "heading2", "bullet", "todo"} {
		payload, ok := b[key].(map[string]any)
		if !ok {
			continue
		}
		elements, ok := payload["elements"].([]map[string]any)
		if !ok {
			t.Fatalf("块 %q 载荷缺少 elements: %v", key, b)
		}
		var sb strings.Builder
		for _, el := range elements {
			run, _ := el["text_run"].(map[string]any)
			content, _ := run["content"].(string)
			sb.WriteString(content)
		}
		return sb.String()
	}
	t.Fatalf("块不含可识别文本载荷: %v", b)
	return ""
}

// allText 拼接块序列的全部文本（跨块断言用）。
func allText(t *testing.T, blocks []map[string]any) string {
	t.Helper()
	var sb strings.Builder
	for _, b := range blocks {
		sb.WriteString(blockText(t, b))
		sb.WriteString("\n")
	}
	return sb.String()
}

// hasBlockType 判断块序列里是否出现指定 block_type。
func hasBlockType(t *testing.T, blocks []map[string]any, typ int) bool {
	t.Helper()
	for _, b := range blocks {
		if blockNum(t, b) == typ {
			return true
		}
	}
	return false
}

// publishEnv 准备发布环境：已绑定 base 的项目（syncEnv）+ 可选的文档绑定。
func publishEnv(t *testing.T, withDoc bool) (*store.Store, model.Project) {
	t.Helper()
	s, p, _ := syncEnv(t)
	if withDoc {
		p.FeishuDocToken = "docT"
		if err := s.SaveProject(p); err != nil {
			t.Fatal(err)
		}
	}
	return s, p
}

// TestPublishWeeklyOrderAttribution：publish weekly → 先静默同步（有 RecordSearch），
// 再向绑定文档追加一次块；首块为防混淆标题（heading2：周号 + 生成时间 + 触发人），
// 尾块为落款（text："由 pulse 导出 · 触发人 tester"），正文含周报小节与 bullet。
func TestPublishWeeklyOrderAttribution(t *testing.T) {
	s, p := publishEnv(t, true)
	fake := &fakeAPI{}
	warn := captureWarn(t)

	if err := PublishReport(context.Background(), clientWith(fake), s, p, "weekly", "tester"); err != nil {
		t.Fatalf("PublishReport: %v", err)
	}

	sawSearch := false
	for _, c := range fake.calls {
		if c.method == "RecordSearch" {
			sawSearch = true
		}
		if c.method == "BlockAppend" && c.args[0] != "docT" {
			t.Fatalf("BlockAppend 文档 = %q, want 绑定的 docT", c.args[0])
		}
	}
	if !sawSearch {
		t.Fatal("publish 前必须执行静默同步（出现 RecordSearch 调用）")
	}
	if len(fake.blockBlocks) != 1 {
		t.Fatalf("BlockAppend 次数 = %d, want 1", len(fake.blockBlocks))
	}
	blocks := fake.blockBlocks[0]

	// 首块 = heading2 防混淆标题
	if blockNum(t, blocks[0]) != 4 {
		t.Fatalf("首块 block_type = %d, want 4(heading2)", blockNum(t, blocks[0]))
	}
	header := blockText(t, blocks[0])
	for _, want := range []string{"周报 2026-W", "生成于", "由 tester 触发"} {
		if !strings.Contains(header, want) {
			t.Fatalf("标题块 %q 必须含 %q", header, want)
		}
	}
	// 尾块 = text 落款
	last := blocks[len(blocks)-1]
	if blockNum(t, last) != 2 {
		t.Fatalf("尾块 block_type = %d, want 2(text)", blockNum(t, last))
	}
	if got := blockText(t, last); got != "由 pulse 导出 · 触发人 tester" {
		t.Fatalf("落款 = %q, want 由 pulse 导出 · 触发人 tester", got)
	}
	// 正文含周报固定小节，且 "- " 列表转为 bullet
	texts := allText(t, blocks)
	for _, want := range []string{"本周完成", "风险清单", "下周计划"} {
		if !strings.Contains(texts, want) {
			t.Fatalf("正文必须含小节 %q: %q", want, texts)
		}
	}
	if !hasBlockType(t, blocks, 12) {
		t.Fatal("weekly 的 '- ' 行必须转为 bullet(12) 块")
	}
	if warn.Len() != 0 {
		t.Fatalf("正常路径不应有警告: %q", warn.String())
	}
}

// TestPublishUnboundDocAutoCreates：未绑定文档时先 DocCreate（标题含项目名）并
// SaveProject 回写 token，块落在新建文档上。
func TestPublishUnboundDocAutoCreates(t *testing.T) {
	s, p := publishEnv(t, false) // 已绑 base、未绑文档
	fake := &fakeAPI{}
	captureWarn(t)

	if err := PublishReport(context.Background(), clientWith(fake), s, p, "versions", "tester"); err != nil {
		t.Fatalf("PublishReport: %v", err)
	}
	var docTitle string
	for _, c := range fake.calls {
		if c.method == "DocCreate" {
			docTitle = c.args[1]
		}
	}
	if !strings.Contains(docTitle, "演示项目") {
		t.Fatalf("DocCreate 标题 = %q, want 含项目名", docTitle)
	}
	if len(fake.blockBlocks) != 1 {
		t.Fatalf("BlockAppend 次数 = %d, want 1", len(fake.blockBlocks))
	}
	saved, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("reload project: found=%v err=%v", found, err)
	}
	if saved.FeishuDocToken != "docT" {
		t.Fatalf("新文档 token 未写回项目: %+v", saved)
	}
}

// TestPublishSyncFailureWarnsContinues：publish 前的静默同步失败 → 警告后基于本地
// 数据继续发布（离线可发布），不向上返回错误。
func TestPublishSyncFailureWarnsContinues(t *testing.T) {
	s, p := publishEnv(t, true)
	fake := &fakeAPI{searchErr: errors.New("网络不可用")}
	warn := captureWarn(t)

	if err := PublishReport(context.Background(), clientWith(fake), s, p, "weekly", "tester"); err != nil {
		t.Fatalf("同步失败必须警告继续而非报错: %v", err)
	}
	if !strings.Contains(warn.String(), "同步") {
		t.Fatalf("警告必须提及同步失败: %q", warn.String())
	}
	if len(fake.blockBlocks) != 1 {
		t.Fatalf("同步失败后仍应追加报表块, got %d 次 BlockAppend", len(fake.blockBlocks))
	}
}

// TestPublishAllTwoSections：--report all = weekly + versions 两次独立 publish，
// 各自带防混淆标题与落款（时间线性质，重跑产生新段落是文档化行为）。
func TestPublishAllTwoSections(t *testing.T) {
	s, p := publishEnv(t, true)
	fake := &fakeAPI{}
	captureWarn(t)

	if err := PublishReport(context.Background(), clientWith(fake), s, p, "all", "tester"); err != nil {
		t.Fatalf("PublishReport(all): %v", err)
	}
	if len(fake.blockBlocks) != 2 {
		t.Fatalf("BlockAppend 次数 = %d, want 2（weekly + versions）", len(fake.blockBlocks))
	}
	h1 := blockText(t, fake.blockBlocks[0][0])
	h2 := blockText(t, fake.blockBlocks[1][0])
	if !strings.Contains(h1, "周报") || !strings.Contains(h2, "版本规划") {
		t.Fatalf("两段标题 = %q / %q, want 周报在前、版本规划在后", h1, h2)
	}
	for i, blocks := range fake.blockBlocks {
		last := blocks[len(blocks)-1]
		if blockNum(t, last) != 2 || blockText(t, last) != "由 pulse 导出 · 触发人 tester" {
			t.Fatalf("第 %d 段缺落款块: %v", i+1, last)
		}
	}
}

// TestPublishVersionsStructuredBlocks：publish versions 生成结构化文本块——
// 版本名/目标日期/状态标题 + 进度 bullet + 风险条目 bullet，不复用 HTML 字符串。
func TestPublishVersionsStructuredBlocks(t *testing.T) {
	s, p, m := syncEnv(t)
	p.FeishuDocToken = "docT"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	actor, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	_ = m
	v, err := s.CreateVersion(model.Version{
		ProjectID: p.ID, Name: "v1.0", TargetDate: "2026-10-01", Status: "planned",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(model.Task{
		ProjectID: p.ID, Title: "已完成任务", Status: "done", AssigneeID: actor.ID, VersionID: v.ID,
	}, actor, nil); err != nil {
		t.Fatal(err)
	}
	// 报表口径的"今天"是 UTC 日期（见 reports 包说明），逾期日必须以 UTC 推算
	overdue := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	tk, err := s.CreateTask(model.Task{
		ProjectID: p.ID, Title: "逾期任务", Status: "todo", AssigneeID: actor.ID,
		DueDate: overdue, VersionID: v.ID,
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	captureWarn(t)

	if err := PublishReport(context.Background(), clientWith(fake), s, p, "versions", "tester"); err != nil {
		t.Fatalf("PublishReport: %v", err)
	}
	if len(fake.blockBlocks) != 1 {
		t.Fatalf("BlockAppend 次数 = %d, want 1", len(fake.blockBlocks))
	}
	blocks := fake.blockBlocks[0]
	if strings.Contains(allText(t, blocks), "<html") {
		t.Fatal("versions 发布必须是结构化文本块，不得复用 HTML")
	}
	// 版本标题：版本名 + 目标日期 + 状态
	var titleFound, progressFound, riskFound bool
	for _, b := range blocks {
		text := blockText(t, b)
		if blockNum(t, b) == 4 && strings.Contains(text, "v1.0") &&
			strings.Contains(text, "2026-10-01") && strings.Contains(text, "planned") {
			titleFound = true
		}
		if blockNum(t, b) == 12 && strings.Contains(text, "进度: 1/2（50%）") {
			progressFound = true
		}
		if blockNum(t, b) == 12 && strings.Contains(text, tk.Title) {
			riskFound = true // 风险条目以任务标题落到 bullet
		}
	}
	if !titleFound {
		t.Fatalf("缺版本标题块（含 v1.0/2026-10-01/planned）: %q", allText(t, blocks))
	}
	if !progressFound {
		t.Fatalf("缺进度 bullet（应为 进度: 1/2（50%%））: %q", allText(t, blocks))
	}
	if !riskFound {
		t.Fatalf("逾期任务的风险条目未出现在 bullet: %q", allText(t, blocks))
	}
}

// TestMarkdownToBlocks：简易 md→blocks 转换的各分支——`## `/`# `→heading2、
// `- [ ] `→todo、`- `→bullet、普通行→text、空行丢弃。
func TestMarkdownToBlocks(t *testing.T) {
	md := "## 小节标题\n普通文本行\n- 列表项\n- [ ] 待办项\n\n# 文档标题\n"
	blocks := markdownToBlocks(md)
	if len(blocks) != 5 {
		t.Fatalf("块数 = %d, want 5（空行丢弃）: %+v", len(blocks), blocks)
	}
	want := []struct {
		typ  int
		text string
	}{
		{4, "小节标题"}, {2, "普通文本行"}, {12, "列表项"}, {17, "待办项"}, {4, "文档标题"},
	}
	for i, w := range want {
		if got := blockNum(t, blocks[i]); got != w.typ {
			t.Fatalf("块[%d] block_type = %d, want %d", i, got, w.typ)
		}
		if got := blockText(t, blocks[i]); got != w.text {
			t.Fatalf("块[%d] 文本 = %q, want %q", i, got, w.text)
		}
	}
}

// TestPublishRejectsUnknownReport：非法 report 名立即报错，不产生任何飞书调用。
func TestPublishRejectsUnknownReport(t *testing.T) {
	s, p := publishEnv(t, true)
	fake := &fakeAPI{}
	captureWarn(t)

	err := PublishReport(context.Background(), clientWith(fake), s, p, "daily", "tester")
	if err == nil || !strings.Contains(err.Error(), "weekly|versions|all") {
		t.Fatalf("非法 report 必须报引导错误, got %v", err)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("非法 report 不应产生任何调用: %+v", fake.calls)
	}
}
