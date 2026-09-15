package cli

import (
	"strings"
	"testing"
)

func TestReviewRecordAndConclude(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}

	out, errOut, err := runCLI(t, "review", "record", "--project", "demo", "--kind", "requirement")
	if err != nil {
		t.Fatalf("review record failed: %v stderr=%s", err, errOut)
	}
	if want := "评审已记录: requirement (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	vs, err := s.ListReviews(p.ID, 0)
	if err != nil || len(vs) != 1 {
		t.Fatalf("review not persisted: n=%d err=%v", len(vs), err)
	}
	if vs[0].Conclusion != "pending" {
		t.Fatalf("conclusion must default to pending: %+v", vs[0])
	}
	requireEntityActivity(t, s, p.ID, "review", "create", "tester")

	// conclude：结论变更落 update 活动（conclusion 非 status 字段）
	out, errOut, err = runCLI(t, "review", "conclude", "1", "--conclusion", "passed_with_notes")
	if err != nil {
		t.Fatalf("review conclude failed: %v stderr=%s", err, errOut)
	}
	if want := "评审结论已更新: passed_with_notes (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	vs, _ = s.ListReviews(p.ID, 0)
	if vs[0].Conclusion != "passed_with_notes" {
		t.Fatalf("conclusion not updated: %+v", vs[0])
	}
	requireEntityActivity(t, s, p.ID, "review", "update", "tester")

	// 引用不存在：需求 ID 与评审 ID
	if _, errOut, err = runCLI(t, "review", "record", "--project", "demo", "--kind", "test", "--requirement", "999"); err == nil ||
		!strings.Contains(errOut, "需求不存在: id=999") {
		t.Fatalf("want 需求不存在 error, err=%v stderr=%s", err, errOut)
	}
	if _, errOut, err = runCLI(t, "review", "conclude", "999", "--conclusion", "passed"); err == nil ||
		!strings.Contains(errOut, "评审不存在: id=999") {
		t.Fatalf("want 评审不存在 error, err=%v stderr=%s", err, errOut)
	}
	// conclude 不允许回退 pending；kind 非法拒绝
	if _, _, err = runCLI(t, "review", "conclude", "1", "--conclusion", "pending"); err == nil {
		t.Fatal("conclude back to pending must fail")
	}
	if _, _, err = runCLI(t, "review", "record", "--project", "demo", "--kind", "design"); err == nil {
		t.Fatal("invalid kind must fail")
	}
}
