package cli

import (
	"strings"
	"testing"
)

func TestSubmitCreateListUpdate(t *testing.T) {
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

	out, errOut, err := runCLI(t, "submit", "create", "--project", "demo", "--version", "v1.0",
		"--requirement", "1", "--test-owner", "alice")
	if err != nil {
		t.Fatalf("submit create failed: %v stderr=%s", err, errOut)
	}
	if want := "提测单已创建 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	ts, err := s.ListTestSubmissions(p.ID, 0)
	if err != nil || len(ts) != 1 {
		t.Fatalf("submission not persisted: n=%d err=%v", len(ts), err)
	}
	if ts[0].Status != "draft" || ts[0].VersionID != 1 || ts[0].RequirementID != 1 || ts[0].SubmittedAt != "" {
		t.Fatalf("unexpected submission: %+v", ts[0])
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
	if ts[0].TestOwnerID != aliceID || aliceID == 0 {
		t.Fatalf("test-owner alice not bound: %+v", ts[0])
	}
	requireEntityActivity(t, s, p.ID, "test_submission", "create", "tester")

	// list：全量与按版本过滤
	out, _, err = runCLI(t, "submit", "list", "--project", "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID\t版本\t状态\t提测人\t测试负责人\t提交时间", "v1.0", "draft", "tester"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
	if _, _, err = runCLI(t, "submit", "list", "--project", "demo", "--version", "v1.0"); err != nil {
		t.Fatal(err)
	}

	// update：流转补 submitted_at，落 update_status 活动
	if _, errOut, err = runCLI(t, "submit", "update", "1", "--status", "submitted"); err != nil {
		t.Fatalf("submit update failed: %v stderr=%s", err, errOut)
	}
	ts, _ = s.ListTestSubmissions(p.ID, 0)
	if ts[0].Status != "submitted" || ts[0].SubmittedAt == "" {
		t.Fatalf("status/submitted_at not updated: %+v", ts[0])
	}
	requireEntityActivity(t, s, p.ID, "test_submission", "update_status", "tester")
}

func TestSubmitReferenceErrors(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "version", "add", "v1.0", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "submit", "create", "--project", "demo", "--version", "nope")
	if err == nil || !strings.Contains(errOut, "版本不存在: nope") {
		t.Fatalf("want 版本不存在 error, err=%v stderr=%s", err, errOut)
	}
	_, errOut, err = runCLI(t, "submit", "create", "--project", "demo", "--version", "v1.0", "--requirement", "999")
	if err == nil || !strings.Contains(errOut, "需求不存在: id=999") {
		t.Fatalf("want 需求不存在 error, err=%v stderr=%s", err, errOut)
	}
	_, errOut, err = runCLI(t, "submit", "update", "999", "--status", "passed")
	if err == nil || !strings.Contains(errOut, "提测单不存在: id=999") {
		t.Fatalf("want 提测单不存在 error, err=%v stderr=%s", err, errOut)
	}
	// update 目标状态按 plan 不含 draft
	if _, _, err = runCLI(t, "submit", "update", "999", "--status", "draft"); err == nil {
		t.Fatal("submit update --status draft must fail")
	}
}
