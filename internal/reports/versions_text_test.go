package reports

import (
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// TestVersionsText：文本形态与 VersionsHTML 同数据源——版本名/目标日期/状态/
// 进度/风险条目齐全，输出为 Markdown 子集（`## ` 标题 + `- ` 列表），供
// feishu publish 转块使用；不得复用 HTML 字符串。
func TestVersionsText(t *testing.T) {
	s := newStore(t)
	p := projectOf(t, s)
	actor := actorOf(t, s)
	v, err := s.CreateVersion(model.Version{
		ProjectID: p.ID, Name: "v1.0", TargetDate: "2026-10-01", Status: "planned",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	taskOf(t, s, p.ID, func(x *model.Task) {
		x.Title = "做一半"
		x.Status = "done"
		x.VersionID = v.ID
		x.AssigneeID = actor.ID
	})
	taskOf(t, s, p.ID, func(x *model.Task) {
		x.Title = "快逾期"
		x.Status = "todo"
		x.VersionID = v.ID
		x.AssigneeID = actor.ID
		x.DueDate = now.AddDate(0, 0, -1).Format("2006-01-02")
	})

	b, err := VersionsText(s, p.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	md := string(b)
	if strings.Contains(md, "<html") || strings.Contains(md, "<div") {
		t.Fatalf("VersionsText 必须是纯文本 Markdown，不得含 HTML: %q", md)
	}
	for _, want := range []string{
		"# 版本规划 · demo",
		"## v1.0（状态 planned，目标 2026-10-01）",
		"进度: 1/2（50%）",
		"快逾期", // 逾期风险条目以任务标题出现
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("VersionsText %q 必须含 %q", md, want)
		}
	}
}

// TestVersionsTextEmpty：无版本时输出占位小节而非空文档。
func TestVersionsTextEmpty(t *testing.T) {
	s := newStore(t)
	p := projectOf(t, s)
	b, err := VersionsText(s, p.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); !strings.Contains(got, "# 版本规划 · demo") || !strings.Contains(got, "- 无") {
		t.Fatalf("空项目应输出标题 + 无版本占位: %q", got)
	}
}
