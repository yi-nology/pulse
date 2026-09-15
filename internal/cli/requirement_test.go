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

	// doc：未配置飞书时降级为未绑定提示（不报错，退出码 0）
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

// TestRequirementDocEnsureAndDegrade：doc 命令的 get-or-create 接线——已绑定 token 时
// 原样显示；未绑定且未配置飞书时降级为未绑定提示。add 缺省触发文档创建并降级提示，
// --no-doc 跳过。
func TestRequirementDocEnsureAndDegrade(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	// add：未配置飞书 → 记录照常保存，stderr 降级提示
	out, errOut, err := runCLI(t, "requirement", "add", "需求甲", "--project", "demo")
	if err != nil {
		t.Fatalf("requirement add failed: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "需求已创建: 需求甲 (id=1)") {
		t.Fatalf("stdout %q must contain 需求已创建", out)
	}
	if !strings.Contains(errOut, "已保存记录（无文档）") {
		t.Fatalf("stderr %q must contain 已保存记录（无文档）", errOut)
	}
	r, found, err := s.GetRequirement(1)
	if err != nil || !found || r.FeishuDocToken != "" {
		t.Fatalf("降级路径不得写回 token: found=%v r=%+v err=%v", found, r, err)
	}
	// --no-doc：跳过文档创建，无降级提示
	if _, errOut, err = runCLI(t, "requirement", "add", "需求乙", "--project", "demo", "--no-doc"); err != nil {
		t.Fatalf("requirement add --no-doc failed: %v stderr=%s", err, errOut)
	}
	if strings.Contains(errOut, "已保存记录（无文档）") {
		t.Fatalf("--no-doc 不应有文档降级提示: %q", errOut)
	}
	// doc：已绑定 → 原样显示 token（不经 feishu 包，直接预置）
	actor, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordDocToken("requirement", 1, "docXYZ", actor, nil); err != nil {
		t.Fatal(err)
	}
	if out, _, err = runCLI(t, "requirement", "doc", "1"); err != nil {
		t.Fatalf("requirement doc failed: %v", err)
	}
	if !strings.Contains(out, "协作文档: docXYZ") {
		t.Fatalf("stdout %q must contain 协作文档: docXYZ", out)
	}
}

// TestRecordCommandsDocDegrade：五条 create/record 命令在未配置飞书时均降级为
// "已保存记录（无文档）"提示——记录照常保存，命令成功。
func TestRecordCommandsDocDegrade(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "version", "add", "v1.0", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	cases := [][]string{
		{"requirement", "add", "需求", "--project", "demo"},
		{"review", "record", "--project", "demo", "--kind", "requirement"},
		{"meeting", "record", "周会", "--project", "demo"},
		{"submit", "create", "--project", "demo", "--version", "v1.0"},
		{"release", "new", "--project", "demo", "--version", "v1.0"},
	}
	for _, args := range cases {
		out, errOut, err := runCLI(t, args...)
		if err != nil {
			t.Fatalf("%v failed: %v stderr=%s", args, err, errOut)
		}
		if !strings.Contains(errOut, "已保存记录（无文档）") {
			t.Fatalf("%v stderr %q must contain 降级提示", args, errOut)
		}
		if !strings.Contains(out, "已创建") && !strings.Contains(out, "已记录") {
			t.Fatalf("%v stdout %q 应包含保存成功提示", args, out)
		}
	}
}
