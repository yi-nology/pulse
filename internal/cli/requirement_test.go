package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/store"
)

// requireEntityActivity 断言项目内恰好有一条指定实体+动作的活动，且 actor 为指定成员。
// 与 cli_test.go 的 requireActivity 同型，但按实体类型过滤——项目 init 本身会落一条
// project 活动，六实体命令组的测试不能按活动总数断言。
func requireEntityActivity(t *testing.T, s *store.Store, projectID int64, entityType, action, actorName string) {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	n, actorID := 0, int64(0)
	for _, a := range acts {
		if a.EntityType == entityType && a.Action == action {
			n++
			actorID = a.ActorID
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 %s/%s activity, got %d: %+v", entityType, action, n, acts)
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == actorID && m.Name == actorName {
			return
		}
	}
	t.Fatalf("activity actor %d not resolved to member %q", actorID, actorName)
}

func TestRequirementAddListUpdateDoc(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}

	out, errOut, err := runCLI(t, "requirement", "add", "支持导出", "--project", "demo",
		"--owner", "alice", "--priority", "2", "--status", "in_dev")
	if err != nil {
		t.Fatalf("requirement add failed: %v stderr=%s", err, errOut)
	}
	if want := "需求已创建: 支持导出 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	r, found, err := s.GetRequirement(1)
	if err != nil || !found {
		t.Fatalf("requirement not persisted: found=%v err=%v", found, err)
	}
	if r.Status != "in_dev" || r.Priority != 2 {
		t.Fatalf("unexpected requirement: %+v", r)
	}
	// --owner 与 --assignee 同语义：已有成员直接用，不存在按 human 创建
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	var aliceType string
	for _, m := range ms {
		if m.Name == "alice" {
			aliceType = m.Type
		}
	}
	if aliceType != "human" {
		t.Fatalf("alice must be created as human: %+v", ms)
	}
	requireEntityActivity(t, s, p.ID, "requirement", "create", "tester")

	// list：列头 + 行内容（负责人显示成员名）
	out, _, err = runCLI(t, "requirement", "list", "--project", "demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID\t标题\t状态\t负责人\t优先级", "支持导出", "in_dev", "alice"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}

	// update：状态流转落 update_status 活动
	if _, errOut, err = runCLI(t, "requirement", "update", "1", "--status", "accepted"); err != nil {
		t.Fatalf("requirement update failed: %v stderr=%s", err, errOut)
	}
	r, _, _ = s.GetRequirement(1)
	if r.Status != "accepted" {
		t.Fatalf("status not updated: %+v", r)
	}
	requireEntityActivity(t, s, p.ID, "requirement", "update_status", "tester")

	// doc：Task 3 前为占位输出
	out, _, err = runCLI(t, "requirement", "doc", "1")
	if err != nil {
		t.Fatalf("requirement doc failed: %v", err)
	}
	if want := "文档未绑定"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
}

func TestRequirementReferenceErrors(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "requirement", "update", "999", "--status", "accepted")
	if err == nil || !strings.Contains(errOut, "需求不存在: id=999") {
		t.Fatalf("want 需求不存在 error, err=%v stderr=%s", err, errOut)
	}
	_, errOut, err = runCLI(t, "requirement", "doc", "999")
	if err == nil || !strings.Contains(errOut, "需求不存在: id=999") {
		t.Fatalf("want 需求不存在 error, err=%v stderr=%s", err, errOut)
	}
	if _, _, err = runCLI(t, "requirement", "add", "x", "--project", "demo", "--status", "bogus"); err == nil {
		t.Fatal("invalid status must fail")
	}
}
