package cli

import (
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/store"
)

func TestBugAddListUpdate(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}
	if _, _, err = runCLI(t, "version", "add", "v1.0", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runCLI(t, "requirement", "add", "需求A", "--project", "demo"); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runCLI(t, "bug", "add", "空指针崩溃", "--project", "demo",
		"--severity", "1", "--assignee", "alice", "--requirement", "1", "--found-version", "v1.0")
	if err != nil {
		t.Fatalf("bug add failed: %v stderr=%s", err, errOut)
	}
	if want := "Bug 已创建: 空指针崩溃 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	b, found, err := s.GetBug(1)
	if err != nil || !found {
		t.Fatalf("bug not persisted: found=%v err=%v", found, err)
	}
	if b.Severity != 1 || b.Status != "open" || b.RequirementID != 1 || b.FoundVersionID != 1 {
		t.Fatalf("unexpected bug: %+v", b)
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	aliceID := int64(0)
	for _, m := range ms {
		if m.Name == "alice" {
			aliceID = m.ID
		}
	}
	if b.AssigneeID != aliceID || aliceID == 0 {
		t.Fatalf("assignee alice not bound: bug=%+v members=%+v", b, ms)
	}
	requireEntityActivity(t, s, p.ID, "bug", "create", "tester")

	// list：过滤 + 列头 + 行内容
	out, _, err = runCLI(t, "bug", "list", "--project", "demo", "--status", "open", "--severity", "1")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID\t标题\t严重级\t状态\t负责人\t发现版本", "空指针崩溃", "alice", "v1.0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}

	// update：状态流转落 update_status 活动
	if _, errOut, err = runCLI(t, "bug", "update", "1", "--status", "fixed"); err != nil {
		t.Fatalf("bug update failed: %v stderr=%s", err, errOut)
	}
	b, _, _ = s.GetBug(1)
	if b.Status != "fixed" {
		t.Fatalf("status not updated: %+v", b)
	}
	requireEntityActivity(t, s, p.ID, "bug", "update_status", "tester")

	if _, _, err = runCLI(t, "bug", "update", "1", "--severity", "9"); err == nil {
		t.Fatal("severity 9 must fail")
	}
}

// TestBugReferenceCrossProjectRefused：跨项目需求引用必须按"不存在"拒绝（守卫
// ProjectID，不泄露其他项目内该 id 的存在性）。
func TestBugReferenceCrossProjectRefused(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "init", "other"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "requirement", "add", "需求A", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "bug", "add", "x", "--project", "other", "--requirement", "1")
	if err == nil || !strings.Contains(errOut, "需求不存在: id=1") {
		t.Fatalf("跨项目需求引用必须报 需求不存在, err=%v stderr=%s", err, errOut)
	}
	other, found, err := s.GetProjectByKey("other")
	if err != nil || !found {
		t.Fatal(err)
	}
	bs, err := s.ListBugs(other.ID, store.BugFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 0 {
		t.Fatalf("失败路径不得创建 bug: %+v", bs)
	}
}

func TestBugReferenceErrors(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "bug", "add", "x", "--project", "demo", "--requirement", "999")
	if err == nil || !strings.Contains(errOut, "需求不存在: id=999") {
		t.Fatalf("want 需求不存在 error, err=%v stderr=%s", err, errOut)
	}
	_, errOut, err = runCLI(t, "bug", "add", "x", "--project", "demo", "--found-version", "999")
	if err == nil || !strings.Contains(errOut, "版本不存在: 999") {
		t.Fatalf("want 版本不存在 error, err=%v stderr=%s", err, errOut)
	}
}
