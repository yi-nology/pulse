package mcpserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// testEnv 把 PULSE_HOME 重定向到临时目录并打开独立 store。
// 与 internal/cli 的 testEnv 同模式，但刻意复制而非 import，保持包独立。
func testEnv(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PULSE_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("default_actor: tester\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "pulse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// connect 用 in-memory 传输起一端 server 并返回已握手的 client session。
func connect(t *testing.T, s *store.Store, agentName string) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	srv, err := New(s, agentName) // New 内部从 PULSE_HOME 读 default_actor
	if err != nil {
		t.Fatal(err)
	}
	ct, st := mcp.NewInMemoryTransports()
	done := make(chan struct{})
	go func() {
		_ = srv.Run(ctx, st)
		close(done)
	}()
	t.Cleanup(cancel) // LIFO：先走下面注册的清理
	client := mcp.NewClient(&mcp.Implementation{Name: "pulse-test"}, nil)
	sc, err := client.Connect(ctx, ct, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = sc.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("server.Run did not return after session close")
		}
	})
	return sc
}

// callTool 调用工具并断言成功，返回 JSON 文本。
func callTool(t *testing.T, sc *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := sc.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("call %s returned isError: %s", name, resultText(res))
	}
	return resultText(res)
}

// callToolErr 断言工具以 isError 结果失败（而非协议错误），返回错误文本。
func callToolErr(t *testing.T, sc *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := sc.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: protocol error: %v", name, err)
	}
	if !res.IsError {
		t.Fatalf("call %s must fail with isError, got: %s", name, resultText(res))
	}
	return resultText(res)
}

func resultText(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

// requireProject 建一个测试项目并返回。
func requireProject(t *testing.T, s *store.Store, key string) model.Project {
	t.Helper()
	p, err := s.CreateProject(key, key+"名", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// memberByName 从 store 里按名找成员。
func memberByName(t *testing.T, s *store.Store, name string) model.Member {
	t.Helper()
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == name {
			return m
		}
	}
	t.Fatalf("member %q not found", name)
	return model.Member{}
}

// activities 返回项目近期全部活动。
func activities(t *testing.T, s *store.Store, projectID int64) []model.Activity {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return acts
}

// decode 把工具 JSON 结果解到 map。
func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("tool result is not JSON object: %v\n%s", err, text)
	}
	return v
}

// decodeList 把工具 JSON 结果解到数组。
func decodeList(t *testing.T, text string) []any {
	t.Helper()
	var v []any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("tool result is not JSON array: %v\n%s", err, text)
	}
	return v
}

// TestAddAndListTasks 简报 Step 1 的核心回路：add_task → list_tasks 可见；
// PULSE_ACTOR 对应 agent 成员自动创建；update_task 改 done 落 update_status 活动。
func TestAddAndListTasks(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	out := callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "t1"})
	created := decode(t, out)
	id, ok := created["ID"].(float64)
	if !ok || id < 1 {
		t.Fatalf("add_task result must carry numeric ID: %s", out)
	}

	list := decodeList(t, callTool(t, sc, "list_tasks", map[string]any{"project": "demo"}))
	if len(list) != 1 {
		t.Fatalf("list_tasks must see exactly 1 task, got %d: %s", len(list), strings.Join([]string{fmtList(list)}, ","))
	}
	if got, _ := list[0].(map[string]any)["Title"].(string); got != "t1" {
		t.Fatalf("list_tasks[0].Title = %q, want t1", got)
	}

	// agent 成员自动创建（type=agent）
	agent := memberByName(t, s, "claude")
	if agent.Type != "agent" {
		t.Fatalf("claude must be an agent member, got %q", agent.Type)
	}

	// update → done：activity 有 update_status，且执行者是 agent
	updated := decode(t, callTool(t, sc, "update_task", map[string]any{"id": id, "status": "done"}))
	if updated["Status"] != "done" {
		t.Fatalf("update_task status = %v, want done", updated["Status"])
	}
	acts := activities(t, s, 1)
	var sawCreate, sawUpdateStatus bool
	for _, a := range acts {
		if a.Action == "create" {
			sawCreate = true
		}
		if a.Action == "update_status" && a.ActorID == agent.ID && a.ActorType == "agent" {
			sawUpdateStatus = true
		}
	}
	if !sawCreate || !sawUpdateStatus {
		t.Fatalf("activities must contain create + update_status by agent: %+v", acts)
	}

	// done → in_progress 自动记 reopen
	decode(t, callTool(t, sc, "update_task", map[string]any{"id": id, "status": "in_progress"}))
	var sawReopen bool
	for _, a := range activities(t, s, 1) {
		if a.Action == "reopen" {
			sawReopen = true
		}
	}
	if !sawReopen {
		t.Fatalf("done → in_progress must log reopen activity: %+v", activities(t, s, 1))
	}
}

func fmtList(list []any) string {
	var sb strings.Builder
	for i, v := range list {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(strings.TrimSpace(string(mustJSON(v))))
	}
	return sb.String()
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// TestAssigneeMe "me" 过滤解析为 agent 自己；具名成员过滤生效；未知成员报错。
func TestAssigneeMe(t *testing.T) {
	s := testEnv(t)
	p := requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	// 先经 add_task 让 agent 成员落库，再直接造一条 agent 自己的任务
	callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "human-task", "assignee": "alice"})
	agent := memberByName(t, s, "claude")
	if _, err := s.CreateTask(model.Task{ProjectID: p.ID, Title: "agent-task", AssigneeID: agent.ID}, agent, nil); err != nil {
		t.Fatal(err)
	}

	me := decodeList(t, callTool(t, sc, "list_tasks", map[string]any{"project": "demo", "assignee": "me"}))
	if len(me) != 1 || me[0].(map[string]any)["Title"] != "agent-task" {
		t.Fatalf(`assignee="me" must return exactly agent-task: %s`, string(mustJSON(me)))
	}
	alice := decodeList(t, callTool(t, sc, "list_tasks", map[string]any{"project": "demo", "assignee": "alice"}))
	if len(alice) != 1 || alice[0].(map[string]any)["Title"] != "human-task" {
		t.Fatalf(`assignee="alice" must return exactly human-task: %s`, string(mustJSON(alice)))
	}
	if msg := callToolErr(t, sc, "list_tasks", map[string]any{"project": "demo", "assignee": "ghost"}); !strings.Contains(msg, "ghost") {
		t.Fatalf("unknown assignee must name the member: %s", msg)
	}
}

// TestAddTaskAssigneeMe 写路径 assignee="me" 解析为 agent 自己（type=agent），
// 不得创建名为 "me" 的人类成员。
func TestAddTaskAssigneeMe(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	created := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t1", "assignee": "me"}))

	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	var agent model.Member
	for _, m := range ms {
		if m.Name == "me" {
			t.Fatalf(`assignee="me" must not create a member named "me": %+v`, ms)
		}
		if m.Name == "claude" {
			agent = m
		}
	}
	if agent.ID == 0 || agent.Type != "agent" {
		t.Fatalf("claude must be the agent member: %+v", agent)
	}
	if created["AssigneeID"] != float64(agent.ID) || created["assignee_name"] != "claude" {
		t.Fatalf(`add_task assignee="me" must resolve to agent %d: %s`, agent.ID, string(mustJSON(created)))
	}
}

// TestUpdateTaskAssigneeMe update_task 的 assignee="me" 把任务改派给 agent 自己。
func TestUpdateTaskAssigneeMe(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	created := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t1", "assignee": "alice"}))
	updated := decode(t, callTool(t, sc, "update_task",
		map[string]any{"id": created["ID"], "assignee": "me"}))

	agent := memberByName(t, s, "claude")
	if updated["AssigneeID"] != float64(agent.ID) || updated["assignee_name"] != "claude" {
		t.Fatalf(`update_task assignee="me" must reassign to agent %d: %s`, agent.ID, string(mustJSON(updated)))
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "me" {
			t.Fatalf(`no member named "me" must be created: %+v`, ms)
		}
	}
}

// TestDelegatedBy delegated_by 把人类成员记为 on_behalf_of，执行者仍是 agent。
func TestDelegatedBy(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	created := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t1", "delegated_by": "zhang"}))
	agent := memberByName(t, s, "claude")
	zhang := memberByName(t, s, "zhang")
	if zhang.Type != "human" {
		t.Fatalf("delegated member must be human, got %q", zhang.Type)
	}
	var act model.Activity
	for _, a := range activities(t, s, 1) {
		if a.Action == "create" && int64(created["ID"].(float64)) == a.EntityID {
			act = a
		}
	}
	if act.ActorID != agent.ID || act.OnBehalfOf != zhang.ID {
		t.Fatalf("activity actor=%d behalf=%d, want agent %d on behalf of %d",
			act.ActorID, act.OnBehalfOf, agent.ID, zhang.ID)
	}
}

// TestVersionsAndDependency 版本与依赖工具（同 CLI 语义）。
func TestVersionsAndDependency(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	v := decode(t, callTool(t, sc, "add_version",
		map[string]any{"project": "demo", "name": "v1.0", "target": "2026-10-01"}))
	if v["Status"] != "planned" {
		t.Fatalf("version default status must be planned: %s", string(mustJSON(v)))
	}
	vid := v["ID"].(float64)

	task := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t1", "version": "v1.0"}))
	task2 := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t2"}))
	if task["version_name"] != "v1.0" {
		t.Fatalf("task version_name must resolve to v1.0: %s", string(mustJSON(task)))
	}

	vs := decodeList(t, callTool(t, sc, "list_versions", map[string]any{"project": "demo"}))
	if len(vs) != 1 {
		t.Fatalf("list_versions must see 1 version: %s", string(mustJSON(vs)))
	}
	updated := decode(t, callTool(t, sc, "update_version",
		map[string]any{"id": vid, "status": "in_dev"}))
	if updated["Status"] != "in_dev" {
		t.Fatalf("update_version status: %s", string(mustJSON(updated)))
	}

	callTool(t, sc, "add_dependency", map[string]any{
		"task_id": task2["ID"], "depends_on_task_id": task["ID"]})
	deps, err := s.ListDependencies(pID(t, s, "demo"))
	if err != nil || len(deps) != 1 || deps[0].TaskID != int64(task2["ID"].(float64)) {
		t.Fatalf("dependency not persisted: %+v err=%v", deps, err)
	}
	// 重复依赖报错
	if msg := callToolErr(t, sc, "add_dependency", map[string]any{
		"task_id": task2["ID"], "depends_on_task_id": task["ID"]}); !strings.Contains(msg, "依赖") {
		t.Fatalf("duplicate dependency must be rejected: %s", msg)
	}
}

func pID(t *testing.T, s *store.Store, key string) int64 {
	t.Helper()
	p, found, err := s.GetProjectByKey(key)
	if err != nil || !found {
		t.Fatalf("project %s: found=%v err=%v", key, found, err)
	}
	return p.ID
}

// TestGetProjectStatus get_project_status 返回项目元数据 + 未完成数 + 风险摘要。
func TestGetProjectStatus(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "done-任务"})
	decode(t, callTool(t, sc, "update_task", map[string]any{"id": 1, "status": "done"}))
	callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "逾期任务", "due": "2026-01-01"})

	st := decode(t, callTool(t, sc, "get_project_status", map[string]any{"project": "demo"}))
	if st["open_tasks"] != float64(1) {
		t.Fatalf("open_tasks = %v, want 1", st["open_tasks"])
	}
	proj, _ := st["project"].(map[string]any)
	if proj == nil || proj["Key"] != "demo" {
		t.Fatalf("project metadata missing: %s", string(mustJSON(st)))
	}
	risks, _ := st["risks"].([]any)
	var overdue bool
	for _, r := range risks {
		if r.(map[string]any)["kind"] == "overdue" {
			overdue = true
		}
	}
	if !overdue {
		t.Fatalf("risks must contain overdue: %s", string(mustJSON(st)))
	}
}

// TestGetWorkload get_workload 返回项目成员负载。
func TestGetWorkload(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	callTool(t, sc, "add_task", map[string]any{
		"project": "demo", "title": "t1", "assignee": "alice",
		"estimate_days": 8.0, "due": time.Now().UTC().Format("2006-01-02")})

	ws := decodeList(t, callTool(t, sc, "get_workload", map[string]any{"project": "demo"}))
	if len(ws) != 1 {
		t.Fatalf("get_workload must return alice: %s", string(mustJSON(ws)))
	}
	w := ws[0].(map[string]any)
	if w["member_name"] != "alice" || w["load_days"] != float64(8) {
		t.Fatalf("unexpected workload: %s", string(mustJSON(w)))
	}
}

// TestListProjectsAndMembers 只读工具：list_projects / list_members。
func TestListProjectsAndMembers(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	ps := decodeList(t, callTool(t, sc, "list_projects", nil))
	if len(ps) != 1 || ps[0].(map[string]any)["Key"] != "demo" {
		t.Fatalf("list_projects: %s", string(mustJSON(ps)))
	}
	// 只读工具不产生成员副作用；经一次写操作（add_task → actor.Resolve）确保成员
	callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "t1"})
	ms := decodeList(t, callTool(t, sc, "list_members", nil))
	var names []string
	for _, m := range ms {
		names = append(names, m.(map[string]any)["Name"].(string))
	}
	// tester（default_actor）与 claude（agent）都被确保存在
	for _, want := range []string{"tester", "claude"} {
		if !contains(names, want) {
			t.Fatalf("members must contain %s: %v", want, names)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestPublishFeishuNotConfigured 未配置飞书时返回引导错误文本（Task 13 前的唯一路径）。
func TestPublishFeishuNotConfigured(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	for _, report := range []string{"weekly", "versions", "all"} {
		msg := callToolErr(t, sc, "publish_feishu",
			map[string]any{"project": "demo", "report": report})
		if !strings.Contains(msg, "未配置飞书") {
			t.Fatalf("publish_feishu(%s) must guide unconfigured feishu: %s", report, msg)
		}
	}
	if msg := callToolErr(t, sc, "publish_feishu",
		map[string]any{"project": "demo", "report": "daily"}); !strings.Contains(msg, "report") {
		t.Fatalf("invalid report must be rejected: %s", msg)
	}
}

// TestPublishFeishuWiredHook 装配 PublishReportFunc 后 publish_feishu 把钩子返回值
// 作为 JSON 结果回给代理（Task 13 在 cli/mcp.go 注入 feishu.PublishReport 桥接，
// 此处用假钩子断言装配协议与结果透传）。
func TestPublishFeishuWiredHook(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	old := PublishReportFunc
	PublishReportFunc = func(st *store.Store, projectKey, report string) (any, error) {
		return map[string]any{"ok": true, "project": projectKey, "report": report, "doc": "docD"}, nil
	}
	t.Cleanup(func() { PublishReportFunc = old })

	sc := connect(t, s, "claude")
	out := callTool(t, sc, "publish_feishu", map[string]any{"project": "demo", "report": "weekly"})
	v := decode(t, out)
	if v["doc"] != "docD" || v["report"] != "weekly" || v["project"] != "demo" || v["ok"] != true {
		t.Fatalf("wired hook 结果未透传: %s", out)
	}
}

// TestToolDescriptions 全部工具注册在案，且 description 含规定句。
func TestToolDescriptions(t *testing.T) {
	s := testEnv(t)
	sc := connect(t, s, "claude")

	lt, err := sc.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"list_projects": false, "get_project_status": false, "list_tasks": false,
		"add_task": false, "update_task": false, "add_dependency": false,
		"list_versions": false, "add_version": false, "update_version": false,
		"list_members": false, "get_workload": false, "publish_feishu": false,
		// v1.1 研发交付闭环（tools_v11.go 注册的 15 个）
		"create_requirement": false, "update_requirement": false, "list_requirements": false,
		"create_bug": false, "update_bug": false, "list_bugs": false,
		"create_test_submission": false, "update_test_submission": false, "list_test_submissions": false,
		"create_release": false, "update_release": false, "list_releases": false,
		"create_review": false, "list_reviews": false, "list_meetings": false,
	}
	for _, tl := range lt.Tools {
		seen, known := want[tl.Name]
		if !known {
			t.Errorf("unexpected tool %q", tl.Name)
			continue
		}
		if seen {
			t.Errorf("tool %q registered twice", tl.Name)
		}
		want[tl.Name] = true
		if !strings.Contains(tl.Description, "更新任务前先 list_tasks 确认 id") {
			t.Errorf("tool %q description must contain required sentence: %q", tl.Name, tl.Description)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("tool %q missing", name)
		}
	}
}

// TestUpdateArchivedTaskRejected 已软删任务对 update_task 只读：工具必须以 isError
// 报「任务已删除」，不得给持有过期 id 的代理返回虚假成功（否则编辑永远不进共享表）。
func TestUpdateArchivedTaskRejected(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	sc := connect(t, s, "claude")

	created := decode(t, callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "t1"}))
	id := int64(created["ID"].(float64))
	agent := memberByName(t, s, "claude")
	if err := s.SoftDeleteTask(id, agent, nil); err != nil {
		t.Fatal(err)
	}

	msg := callToolErr(t, sc, "update_task", map[string]any{"id": id, "status": "done"})
	if !strings.Contains(msg, "任务已删除") {
		t.Fatalf("update_task on archived id must be rejected with 任务已删除: %s", msg)
	}
}

// TestMissingRequired 缺必填参数走 isError（SDK schema 校验或本包校验），不炸协议。
func TestMissingRequired(t *testing.T) {
	s := testEnv(t)
	sc := connect(t, s, "claude")

	if msg := callToolErr(t, sc, "add_task", map[string]any{"title": "t"}); msg == "" {
		t.Fatal("add_task without project must fail with message")
	}
	if msg := callToolErr(t, sc, "get_project_status", map[string]any{"project": "ghost"}); !strings.Contains(msg, "项目不存在") {
		t.Fatalf("unknown project must be reported: %s", msg)
	}
	if msg := callToolErr(t, sc, "update_task", map[string]any{"id": 999, "status": "done"}); !strings.Contains(msg, "任务不存在") {
		t.Fatalf("unknown task must be reported: %s", msg)
	}
	if msg := callToolErr(t, sc, "update_task", map[string]any{"id": 1, "status": "doing"}); !strings.Contains(msg, "status") {
		t.Fatalf("invalid status must be rejected: %s", msg)
	}
}
