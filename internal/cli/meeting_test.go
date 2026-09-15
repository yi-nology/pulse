package cli

import (
	"strings"
	"testing"
)

func TestMeetingRecordAndList(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}

	out, errOut, err := runCLI(t, "meeting", "record", "迭代评审会", "--project", "demo")
	if err != nil {
		t.Fatalf("meeting record failed: %v stderr=%s", err, errOut)
	}
	if want := "会议已记录: 迭代评审会 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	ms, err := s.ListMeetings(p.ID)
	if err != nil || len(ms) != 1 {
		t.Fatalf("meeting not persisted: n=%d err=%v", len(ms), err)
	}
	if ms[0].CreatedBy == 0 {
		t.Fatalf("created_by must default to actor: %+v", ms[0])
	}
	requireEntityActivity(t, s, p.ID, "meeting", "create", "tester")

	out, _, err = runCLI(t, "meeting", "list", "--project", "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID\t标题\t时间\t创建人", "迭代评审会", "tester"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}

	if _, errOut, err = runCLI(t, "meeting", "record", "x", "--project", "nope"); err == nil ||
		!strings.Contains(errOut, "项目不存在: nope") {
		t.Fatalf("want 项目不存在 error, err=%v stderr=%s", err, errOut)
	}
}
