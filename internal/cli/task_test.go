package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// storeActivitiesInWindow 经测试持有的 store 读取项目近 1 小时内的活动。
func storeActivitiesInWindow(s *store.Store, projectID int64) ([]model.Activity, error) {
	return s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
}

// memberNameByID 把活动 actor_id 解析回成员名。
func memberNameByID(t *testing.T, s *store.Store, id int64) string {
	t.Helper()
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == id {
			return m.Name
		}
	}
	return ""
}

// requireLastTaskActivity 断言项目内最后一条 task 实体活动的 action 与 detail。
func requireLastTaskActivity(t *testing.T, s *store.Store, projectID int64, action, detailSub string) {
	t.Helper()
	acts, err := storeActivitiesInWindow(s, projectID)
	if err != nil {
		t.Fatal(err)
	}
	var matched int
	for _, a := range acts {
		if a.EntityType == "task" {
			matched++
		}
	}
	if matched == 0 {
		t.Fatalf("no task activity found: %+v", acts)
	}
	// 找最后一条 task 实体活动
	var last struct{ action, detail string }
	for _, a := range acts {
		if a.EntityType == "task" {
			last.action, last.detail = a.Action, a.Detail
		}
	}
	if last.action != action || !strings.Contains(last.detail, detailSub) {
		t.Fatalf("last task activity = %q / %q, want %q / contains %q", last.action, last.detail, action, detailSub)
	}
}

func TestTaskAddAndList(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project: found=%v err=%v", found, err)
	}

	out, errOut, err := runCLI(t, "task", "add", "写周报", "--project", "demo",
		"--assignee", "alice", "--due", "2026-09-20", "--estimate", "2", "--priority", "2")
	if err != nil {
		t.Fatalf("task add failed: %v stderr=%s", err, errOut)
	}
	if want := "任务已创建: 写周报 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	// --assignee 按名 get-or-create human
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	var alice *string
	for _, m := range ms {
		if m.Name == "alice" {
			alice = &m.Type
		}
	}
	if alice == nil || *alice != "human" {
		t.Fatalf("alice must be created as human: %+v", ms)
	}
	// create 活动归到 default_actor tester
	acts, err := storeActivitiesInWindow(s, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var creates int
	var actorName string
	for _, a := range acts {
		if a.EntityType == "task" && a.Action == "create" {
			creates++
			actorName = memberNameByID(t, s, a.ActorID)
		}
	}
	if creates != 1 || actorName != "tester" {
		t.Fatalf("task add must log 1 create activity by tester, got %d by %q", creates, actorName)
	}

	// list：列头 + 行内容
	out, errOut, err = runCLI(t, "task", "list", "--project", "demo")
	if err != nil {
		t.Fatalf("task list failed: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"ID\t标题\t状态\t负责人\t截止\t版本", "写周报", "alice", "2026-09-20"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
	// --status / --overdue 过滤不误伤
	out, _, err = runCLI(t, "task", "list", "--project", "demo", "--status", "todo")
	if err != nil || !strings.Contains(out, "写周报") {
		t.Fatalf("status filter list: out=%q err=%v", out, err)
	}
	out, _, err = runCLI(t, "task", "list", "--project", "demo", "--overdue")
	if err != nil || strings.Contains(out, "写周报") {
		t.Fatalf("overdue filter must exclude future-due task: out=%q err=%v", out, err)
	}
}

func TestTaskUpdateAndReopenViaCLI(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "task", "add", "任务甲", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	p, _, _ := s.GetProjectByKey("demo")

	out, _, err := runCLI(t, "task", "update", "1", "--status", "done")
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if want := "任务已更新: 任务甲 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	requireLastTaskActivity(t, s, p.ID, "update_status", `"from":"todo","to":"done"`)

	// done → todo 走 reopen
	if _, _, err := runCLI(t, "task", "update", "1", "--status", "todo"); err != nil {
		t.Fatal(err)
	}
	requireLastTaskActivity(t, s, p.ID, "reopen", `"from":"done","to":"todo"`)

	// 非 status 字段走 update
	if _, _, err := runCLI(t, "task", "update", "1", "--priority", "1"); err != nil {
		t.Fatal(err)
	}
	requireLastTaskActivity(t, s, p.ID, "update", `"field":"priority","from":3,"to":1`)

	// 状态刷新进 list
	out, _, err = runCLI(t, "task", "list", "--project", "demo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "todo") {
		t.Fatalf("list must show reopened status: %q", out)
	}
}

func TestTaskListMine(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	// 不带 --assignee 时任务无负责人，--mine 不应看到
	if _, _, err := runCLI(t, "task", "add", "公共任务", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	// 指派给 tester（即 default_actor / "我"）
	if _, _, err := runCLI(t, "task", "add", "我的任务", "--project", "demo", "--assignee", "tester"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "task", "list", "--project", "demo", "--mine")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "我的任务") || strings.Contains(out, "公共任务") {
		t.Fatalf("--mine must show only own tasks: %q", out)
	}
}

func TestTaskRmArchives(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "task", "add", "待删任务", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "task", "rm", "1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "任务已删除: 待删任务 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	// 默认列表不可见
	out, _, err = runCLI(t, "task", "list", "--project", "demo")
	if err != nil || strings.Contains(out, "待删任务") {
		t.Fatalf("default list must hide removed task: out=%q err=%v", out, err)
	}
	// store 层：IncludeArchived 仍可见（行保留 + archived=1）
	tasks, err := s.ListTasks(1, store.TaskFilter{IncludeArchived: true})
	if err != nil || len(tasks) != 1 || !tasks[0].Archived {
		t.Fatalf("archived task must remain: %+v err=%v", tasks, err)
	}
}

// TestTaskAssigneeResolvesExistingMember --assignee 先按名查成员表，命中即用
// （不论 human/agent），未命中再按 human 创建（E2E-2："人给 agent 派活"）。
func TestTaskAssigneeResolvesExistingMember(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	codex, err := s.GetOrCreateMember("codex", "agent")
	if err != nil {
		t.Fatal(err)
	}

	// (a) task add --assignee codex：codex 已是 agent → 直接指派，不报类型冲突
	out, errOut, err := runCLI(t, "task", "add", "agent任务", "--project", "demo", "--assignee", "codex")
	if err != nil {
		t.Fatalf("task add --assignee codex failed: %v stderr=%s", err, errOut)
	}
	if want := "任务已创建: agent任务 (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	tk, found, err := s.GetTask(1)
	if err != nil || !found {
		t.Fatalf("task 1: found=%v err=%v", found, err)
	}
	if tk.AssigneeID != codex.ID {
		t.Fatalf("task assignee = %d, want agent codex id %d", tk.AssigneeID, codex.ID)
	}

	// (a2) task update --assignee codex：同样命中现有 agent
	if _, _, err := runCLI(t, "task", "add", "无主任务", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runCLI(t, "task", "update", "2", "--assignee", "codex"); err != nil {
		t.Fatalf("task update --assignee codex failed: %v", err)
	}
	tk, _, err = s.GetTask(2)
	if err != nil {
		t.Fatal(err)
	}
	if tk.AssigneeID != codex.ID {
		t.Fatalf("updated task assignee = %d, want agent codex id %d", tk.AssigneeID, codex.ID)
	}

	// (b) 不存在的名字 → 仍按 human 创建（保留既有行为）
	if _, _, err := runCLI(t, "task", "add", "human任务", "--project", "demo", "--assignee", "newbie"); err != nil {
		t.Fatalf("task add --assignee newbie failed: %v", err)
	}
	tk, _, err = s.GetTask(3)
	if err != nil {
		t.Fatal(err)
	}
	nb, found, err := s.GetMemberByName("newbie")
	if err != nil || !found {
		t.Fatalf("newbie member: found=%v err=%v", found, err)
	}
	if tk.AssigneeID != nb.ID || nb.Type != "human" {
		t.Fatalf("unknown assignee must be created as human: task=%d member=%+v", tk.AssigneeID, nb)
	}

	// codex 必须仍是唯一的 agent 成员（未被二次创建成 human）
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	var codexN, agentN int
	for _, m := range ms {
		if m.Name == "codex" {
			codexN++
			if m.Type != "agent" {
				t.Fatalf("codex type = %q, want agent (unchanged)", m.Type)
			}
		}
		if m.Type == "agent" {
			agentN++
		}
	}
	if codexN != 1 || agentN != 1 {
		t.Fatalf("codex must stay a single agent member: codexN=%d agentN=%d members=%+v", codexN, agentN, ms)
	}
}

func TestTaskCommandErrors(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"缺 --project", []string{"task", "add", "x"}, "必须提供 --project"},
		{"项目不存在", []string{"task", "add", "x", "--project", "nope"}, "项目不存在: nope"},
		{"非法状态", []string{"task", "add", "x", "--project", "demo", "--status", "doing"}, "status 必须为"},
		{"非法日期", []string{"task", "add", "x", "--project", "demo", "--due", "明天"}, "YYYY-MM-DD"},
		{"版本名未创建", []string{"task", "add", "x", "--project", "demo", "--version", "v1"}, "版本不存在: v1"},
		{"任务不存在", []string{"task", "update", "99", "--title", "y"}, "任务不存在: id=99"},
		{"删除不存在", []string{"task", "rm", "99"}, "任务不存在: id=99"},
	}
	for _, c := range cases {
		_, errOut, err := runCLI(t, c.args...)
		if err == nil {
			t.Fatalf("%s: must fail", c.name)
		}
		if !strings.Contains(errOut, c.want) {
			t.Fatalf("%s: stderr %q must contain %q", c.name, errOut, c.want)
		}
	}
}
