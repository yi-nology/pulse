package store

import (
	"strconv"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

func strptr(s string) *string   { return &s }
func i64ptr(i int64) *int64     { return &i }
func f64ptr(f float64) *float64 { return &f }

// seedProject 建一个演示项目（activity/tasks 外键都挂在 projects 上）。
func seedProject(t *testing.T, s *Store) model.Project {
	t.Helper()
	p, err := s.CreateProject("demo", "演示", "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func taskActor(t *testing.T, s *Store) model.Member {
	t.Helper()
	m, err := s.GetOrCreateMember("actor", "human")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// seedTask 按 mut 定制字段后创建任务（CreateTask 自身落 create 活动）。
func seedTask(t *testing.T, s *Store, projectID int64, actor model.Member, mut func(*model.Task)) model.Task {
	t.Helper()
	mt := model.Task{ProjectID: projectID, Title: "任务"}
	if mut != nil {
		mut(&mt)
	}
	tk, err := s.CreateTask(mt, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return tk
}

// recentActivities 取项目近 1 小时内的活动。
func recentActivities(t *testing.T, s *Store, projectID int64) []model.Activity {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return acts
}

// changeActivities 取近 1 小时内 task 实体的"变更类"活动（排除创建时的 create）。
func changeActivities(t *testing.T, s *Store, projectID int64) []model.Activity {
	t.Helper()
	var out []model.Activity
	for _, a := range recentActivities(t, s, projectID) {
		if a.EntityType == "task" && a.Action != "create" {
			out = append(out, a)
		}
	}
	return out
}

// resetTimestamps 把任务时间戳拨回 2000 年，便于断言“确被刷新/未被刷新”。
func resetTimestamps(t *testing.T, s *Store, id int64) {
	t.Helper()
	if _, err := s.db.Exec(
		`UPDATE tasks SET status_changed_at = '2000-01-01 00:00:00', updated_at = '2000-01-01 00:00:00' WHERE id = ?`,
		id); err != nil {
		t.Fatal(err)
	}
}

func TestCreateTaskDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}

	tk, err := s.CreateTask(model.Task{
		ProjectID: p.ID, Title: "写周报", AssigneeID: actor.ID,
	}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省值：status=todo、priority=3；时间戳均由 DB/代码填充
	if tk.ID == 0 || tk.Title != "写周报" || tk.Status != "todo" || tk.Priority != 3 {
		t.Fatalf("unexpected task: %+v", tk)
	}
	if tk.CreatedAt == "" || tk.UpdatedAt == "" || tk.StatusChangedAt == "" {
		t.Fatalf("timestamps must be set: %+v", tk)
	}
	got, found, err := s.GetTask(tk.ID)
	if err != nil || !found {
		t.Fatalf("GetTask: found=%v err=%v", found, err)
	}
	if got != tk {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, tk)
	}

	acts := recentActivities(t, s, p.ID)
	if len(acts) != 1 {
		t.Fatalf("create must log exactly 1 activity, got %d", len(acts))
	}
	a := acts[0]
	if a.Action != "create" || a.EntityType != "task" || a.EntityID != tk.ID ||
		a.ProjectID != p.ID || a.ActorID != actor.ID || a.OnBehalfOf != boss.ID {
		t.Fatalf("unexpected activity: %+v", a)
	}
}

func TestCreateTaskInvalidStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	if _, err := s.CreateTask(model.Task{ProjectID: p.ID, Title: "x", Status: "doing"}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
}

func TestUpdateTaskTodoToDone(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	tk := seedTask(t, s, p.ID, actor, nil)

	resetTimestamps(t, s, tk.ID)
	got, err := s.UpdateTask(tk.ID, TaskChanges{Status: strptr("done")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "done" {
		t.Fatalf("status must be done, got %q", got.Status)
	}
	if got.StatusChangedAt == "2000-01-01 00:00:00" || got.UpdatedAt == "2000-01-01 00:00:00" {
		t.Fatalf("status change must refresh both timestamps: %+v", got)
	}
	acts := changeActivities(t, s, p.ID)
	if len(acts) != 1 {
		t.Fatalf("must log exactly 1 activity, got %d", len(acts))
	}
	if acts[0].Action != "update_status" {
		t.Fatalf("todo→done must log update_status, got %q", acts[0].Action)
	}
	if want := `{"field":"status","from":"todo","to":"done"}`; acts[0].Detail != want {
		t.Fatalf("detail %q must be %q", acts[0].Detail, want)
	}
}

func TestUpdateTaskReopen(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	tk := seedTask(t, s, p.ID, actor, func(m *model.Task) { m.Status = "done" })

	resetTimestamps(t, s, tk.ID)
	got, err := s.UpdateTask(tk.ID, TaskChanges{Status: strptr("todo")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "todo" {
		t.Fatalf("status must be todo, got %q", got.Status)
	}
	if got.StatusChangedAt == "2000-01-01 00:00:00" {
		t.Fatal("reopen must refresh status_changed_at")
	}
	acts := changeActivities(t, s, p.ID)
	if len(acts) != 1 || acts[0].Action != "reopen" {
		t.Fatalf("done→todo must log exactly 1 reopen activity, got %+v", acts)
	}
	if want := `{"field":"status","from":"done","to":"todo"}`; acts[0].Detail != want {
		t.Fatalf("detail %q must be %q", acts[0].Detail, want)
	}
}

func TestUpdateTaskTitleRefreshesUpdatedAtOnly(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	tk := seedTask(t, s, p.ID, actor, nil)

	resetTimestamps(t, s, tk.ID)
	got, err := s.UpdateTask(tk.ID, TaskChanges{Title: strptr("新标题")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "新标题" {
		t.Fatalf("title not updated: %+v", got)
	}
	if got.UpdatedAt == "2000-01-01 00:00:00" {
		t.Fatal("any change must refresh updated_at")
	}
	if got.StatusChangedAt != "2000-01-01 00:00:00" {
		t.Fatal("non-status change must NOT refresh status_changed_at")
	}
	acts := changeActivities(t, s, p.ID)
	if len(acts) != 1 || acts[0].Action != "update" {
		t.Fatalf("title change must log exactly 1 update activity, got %+v", acts)
	}
	if want := `{"field":"title","from":"任务","to":"新标题"}`; acts[0].Detail != want {
		t.Fatalf("detail %q must be %q", acts[0].Detail, want)
	}
}

func TestUpdateTaskNoopKeepsTimestampsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	tk := seedTask(t, s, p.ID, actor, nil)

	resetTimestamps(t, s, tk.ID)
	same := tk.Status
	got, err := s.UpdateTask(tk.ID, TaskChanges{Status: &same}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.StatusChangedAt != "2000-01-01 00:00:00" || got.UpdatedAt != "2000-01-01 00:00:00" {
		t.Fatalf("no-change update must not refresh timestamps: %+v", got)
	}
	if acts := changeActivities(t, s, p.ID); len(acts) != 0 {
		t.Fatalf("no-change update must not log activity: %+v", acts)
	}
}

func TestUpdateTaskNotFound(t *testing.T) {
	s := openTest(t)
	actor := taskActor(t, s)
	if _, err := s.UpdateTask(999, TaskChanges{Title: strptr("x")}, actor, nil); err == nil {
		t.Fatal("updating nonexistent task must error")
	}
}

func TestUpdateTaskMixedFields(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	tk := seedTask(t, s, p.ID, actor, nil)
	alice, err := s.GetOrCreateMember("alice", "human")
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.UpdateTask(tk.ID, TaskChanges{
		AssigneeID:   i64ptr(alice.ID),
		Priority:     i64ptr(1),
		EstimateDays: f64ptr(2.5),
		Description:  strptr("说明"),
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.AssigneeID != alice.ID || got.Priority != 1 || got.EstimateDays != 2.5 || got.Description != "说明" {
		t.Fatalf("fields not updated: %+v", got)
	}

	// 清空负责人：AssigneeID 指向 0 → 落 NULL
	got, err = s.UpdateTask(tk.ID, TaskChanges{AssigneeID: i64ptr(0)}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.AssigneeID != 0 {
		t.Fatalf("assignee must be cleared, got %d", got.AssigneeID)
	}

	acts := changeActivities(t, s, p.ID)
	// description + assignee_id + priority + estimate_days + 清空 assignee = 5 条
	if len(acts) != 5 {
		t.Fatalf("expected 5 change activities, got %d: %+v", len(acts), acts)
	}
	// 变更按字段固定顺序落活动：description → assignee_id → priority → estimate_days → assignee 清空
	if acts[0].Detail != `{"field":"description","from":"","to":"说明"}` {
		t.Fatalf("unexpected first activity: %+v", acts[0])
	}
	if acts[1].Detail != `{"field":"assignee_id","from":0,"to":`+strconv.FormatInt(alice.ID, 10)+`}` {
		t.Fatalf("unexpected second activity: %+v", acts[1])
	}
	if acts[4].Detail != `{"field":"assignee_id","from":`+strconv.FormatInt(alice.ID, 10)+`,"to":0}` {
		t.Fatalf("unexpected clear-assignee activity: %+v", acts[4])
	}
}

func TestListTasksFilters(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	alice, err := s.GetOrCreateMember("alice", "human")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.GetOrCreateMember("bob", "human")
	if err != nil {
		t.Fatal(err)
	}

	// 版本：直接植入一行，供 VersionID 过滤与按名解析用
	res, err := s.db.Exec(`INSERT INTO versions (project_id, name) VALUES (?, 'v1')`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	vid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	tAlice := seedTask(t, s, p.ID, actor, func(m *model.Task) { m.AssigneeID = alice.ID; m.VersionID = vid })
	tDone := seedTask(t, s, p.ID, actor, func(m *model.Task) { m.AssigneeID = bob.ID; m.Status = "done" })

	// assignee 过滤
	got, err := s.ListTasks(p.ID, TaskFilter{AssigneeID: alice.ID})
	if err != nil || len(got) != 1 || got[0].ID != tAlice.ID {
		t.Fatalf("assignee filter: got %+v err=%v", got, err)
	}
	// status 过滤
	got, err = s.ListTasks(p.ID, TaskFilter{Status: "done"})
	if err != nil || len(got) != 1 || got[0].ID != tDone.ID {
		t.Fatalf("status filter: got %+v err=%v", got, err)
	}
	// version 过滤
	got, err = s.ListTasks(p.ID, TaskFilter{VersionID: vid})
	if err != nil || len(got) != 1 || got[0].ID != tAlice.ID {
		t.Fatalf("version filter: got %+v err=%v", got, err)
	}
	// 无过滤：全量
	got, err = s.ListTasks(p.ID, TaskFilter{})
	if err != nil || len(got) != 2 {
		t.Fatalf("no filter: got %+v err=%v", got, err)
	}
}

func TestListTasksOverdueOnly(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	tomorrow := time.Now().UTC().AddDate(0, 0, 1).Format("2006-01-02")

	overdueOpen := seedTask(t, s, p.ID, actor, func(m *model.Task) { m.DueDate = yesterday })
	seedTask(t, s, p.ID, actor, func(m *model.Task) { m.DueDate = yesterday; m.Status = "done" }) // 已完成不算逾期
	seedTask(t, s, p.ID, actor, func(m *model.Task) { m.DueDate = tomorrow })                     // 未到期
	seedTask(t, s, p.ID, actor, func(m *model.Task) {})                                           // 无截止日

	got, err := s.ListTasks(p.ID, TaskFilter{OverdueOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != overdueOpen.ID {
		t.Fatalf("overdue filter must keep only open overdue task, got %+v", got)
	}
}

func TestSoftDeleteHidesFromDefaultList(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	t1 := seedTask(t, s, p.ID, actor, nil)
	t2 := seedTask(t, s, p.ID, actor, nil)

	if err := s.SoftDeleteTask(t1.ID, actor, nil); err != nil {
		t.Fatal(err)
	}
	// 默认列表不可见
	got, err := s.ListTasks(p.ID, TaskFilter{})
	if err != nil || len(got) != 1 || got[0].ID != t2.ID {
		t.Fatalf("default list must hide archived: got %+v err=%v", got, err)
	}
	// IncludeArchived 可见，且行保留（未物理删除）
	got, err = s.ListTasks(p.ID, TaskFilter{IncludeArchived: true})
	if err != nil || len(got) != 2 {
		t.Fatalf("include-archived list: got %+v err=%v", got, err)
	}
	for _, tk := range got {
		if tk.ID == t1.ID && !tk.Archived {
			t.Fatalf("t1 must be archived: %+v", tk)
		}
	}
	if _, found, err := s.GetTask(t1.ID); err != nil || !found {
		t.Fatalf("soft-deleted row must remain: found=%v err=%v", found, err)
	}
	// 落 archive 活动
	acts := changeActivities(t, s, p.ID)
	var archives []model.Activity
	for _, a := range acts {
		if a.Action == "archive" {
			archives = append(archives, a)
		}
	}
	if len(archives) != 1 || archives[0].EntityID != t1.ID {
		t.Fatalf("soft delete must log exactly 1 archive activity: %+v", acts)
	}
	if want := `{"field":"archived","from":false,"to":true}`; archives[0].Detail != want {
		t.Fatalf("detail %q must be %q", archives[0].Detail, want)
	}
	// 重复软删幂等（不再落活动）；不存在的任务报错
	if err := s.SoftDeleteTask(t1.ID, actor, nil); err != nil {
		t.Fatalf("re-archive must be idempotent: %v", err)
	}
	if err := s.SoftDeleteTask(999, actor, nil); err == nil {
		t.Fatal("soft-deleting nonexistent task must error")
	}
}

func TestResolveVersionID(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	other, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.db.Exec(`INSERT INTO versions (project_id, name) VALUES (?, 'v1')`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	vid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	// 按名解析
	got, err := s.ResolveVersionID(p.ID, "v1")
	if err != nil || got != vid {
		t.Fatalf("resolve by name: got %d err=%v", got, err)
	}
	// 按 ID 解析（须属于本项目）
	got, err = s.ResolveVersionID(p.ID, strconv.FormatInt(vid, 10))
	if err != nil || got != vid {
		t.Fatalf("resolve by id: got %d err=%v", got, err)
	}
	// 空引用 = 不设版本
	got, err = s.ResolveVersionID(p.ID, "")
	if err != nil || got != 0 {
		t.Fatalf("empty ref: got %d err=%v", got, err)
	}
	// 名不存在（空表/未创建）
	if _, err := s.ResolveVersionID(p.ID, "v2"); err == nil {
		t.Fatal("unknown version name must error")
	}
	// 数字 ID 不存在
	if _, err := s.ResolveVersionID(p.ID, "999"); err == nil {
		t.Fatal("unknown version id must error")
	}
	// 版本属于别的项目
	if _, err := s.ResolveVersionID(other.ID, "v1"); err == nil {
		t.Fatal("version of another project must error")
	}
}

func TestVersionNamesByProject(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	if _, err := s.db.Exec(`INSERT INTO versions (project_id, name) VALUES (?, 'v1'), (?, 'v2')`, p.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	names, err := s.VersionNamesByProject(p.ID)
	if err != nil || len(names) != 2 {
		t.Fatalf("names: %v err=%v", names, err)
	}
	if names[1] != "v1" || names[2] != "v2" {
		t.Fatalf("unexpected names: %v", names)
	}
}
