package cli

import (
	"strings"
	"testing"
)

func TestReleaseNewListUpdate(t *testing.T) {
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

	out, errOut, err := runCLI(t, "release", "new", "--project", "demo", "--version", "v1.0", "--manager", "alice")
	if err != nil {
		t.Fatalf("release new failed: %v stderr=%s", err, errOut)
	}
	if want := "发版记录已创建 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	rs, err := s.ListReleases(p.ID)
	if err != nil || len(rs) != 1 {
		t.Fatalf("release not persisted: n=%d err=%v", len(rs), err)
	}
	if rs[0].Status != "preparing" || rs[0].VersionID != 1 || rs[0].ReleasedAt != "" {
		t.Fatalf("unexpected release: %+v", rs[0])
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
	if rs[0].ReleaseManagerID != aliceID || aliceID == 0 {
		t.Fatalf("manager alice not bound: %+v", rs[0])
	}
	requireEntityActivity(t, s, p.ID, "release", "create", "tester")

	out, _, err = runCLI(t, "release", "list", "--project", "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID\t版本\t状态\t发布负责人\t发布时间", "v1.0", "preparing", "alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}

	// update：进入 released 补记 released_at，落 update_status 活动
	if _, errOut, err = runCLI(t, "release", "update", "1", "--status", "released"); err != nil {
		t.Fatalf("release update failed: %v stderr=%s", err, errOut)
	}
	rs, _ = s.ListReleases(p.ID)
	if rs[0].Status != "released" || rs[0].ReleasedAt == "" {
		t.Fatalf("status/released_at not updated: %+v", rs[0])
	}
	requireEntityActivity(t, s, p.ID, "release", "update_status", "tester")
}

func TestReleaseReferenceErrors(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "release", "new", "--project", "demo", "--version", "nope")
	if err == nil || !strings.Contains(errOut, "版本不存在: nope") {
		t.Fatalf("want 版本不存在 error, err=%v stderr=%s", err, errOut)
	}
	_, errOut, err = runCLI(t, "release", "update", "999", "--status", "released")
	if err == nil || !strings.Contains(errOut, "发版不存在: id=999") {
		t.Fatalf("want 发版不存在 error, err=%v stderr=%s", err, errOut)
	}
}
