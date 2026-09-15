package store

import (
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

func TestCreateBugDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}

	b, err := s.CreateBug(model.Bug{ProjectID: p.ID, Title: "崩溃"}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省值：status=open、severity=3（P2）、reporter 归操作者
	if b.ID == 0 || b.Title != "崩溃" || b.Status != "open" || b.Severity != 3 {
		t.Fatalf("unexpected bug: %+v", b)
	}
	if b.ReporterID != actor.ID || b.CreatedAt == "" || b.UpdatedAt == "" {
		t.Fatalf("defaults missing: %+v", b)
	}
	got, found, err := s.GetBug(b.ID)
	if err != nil || !found {
		t.Fatalf("GetBug: found=%v err=%v", found, err)
	}
	if got != b {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, b)
	}

	creates := 0
	for _, a := range recentActivities(t, s, p.ID) {
		if a.EntityType == "bug" {
			creates++
			if a.Action != "create" || a.EntityID != b.ID || a.OnBehalfOf != boss.ID ||
				a.ActorID != actor.ID || a.ProjectID != p.ID {
				t.Fatalf("unexpected bug activity: %+v", a)
			}
		}
	}
	if creates != 1 {
		t.Fatalf("create must log exactly 1 bug activity, got %d", creates)
	}
}

func TestCreateBugInvalidStatusOrSeverity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	if _, err := s.CreateBug(model.Bug{ProjectID: p.ID, Title: "x", Status: "resolving"}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
	if _, err := s.CreateBug(model.Bug{ProjectID: p.ID, Title: "x", Severity: 7}, actor, nil); err == nil {
		t.Fatal("severity out of 1..4 must error")
	}
	if _, err := s.CreateBug(model.Bug{ProjectID: p.ID, Title: "x", Severity: -1}, actor, nil); err == nil {
		t.Fatal("negative severity must error")
	}
}

func TestGetBugMissing(t *testing.T) {
	s := openTest(t)
	if _, found, err := s.GetBug(999); err != nil || found {
		t.Fatalf("missing bug: found=%v err=%v", found, err)
	}
}

func TestUpdateBugFieldsAndStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	alice, err := s.GetOrCreateMember("alice", "human")
	if err != nil {
		t.Fatal(err)
	}
	req := seedRequirement(t, s, p.ID, actor, nil)
	res, err := s.db.Exec(`INSERT INTO versions (project_id, name) VALUES (?, 'v1')`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	vid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	tk := seedTask(t, s, p.ID, actor, nil)

	b := seedBug(t, s, p.ID, actor, nil)

	got, err := s.UpdateBug(b.ID, BugChanges{
		Title:          strptr("首页崩溃"),
		Description:    strptr("点击即崩"),
		Status:         strptr("fixing"),
		Severity:       i64ptr(1),
		AssigneeID:     i64ptr(alice.ID),
		RequirementID:  i64ptr(req.ID),
		FoundVersionID: i64ptr(vid),
		FixTaskID:      i64ptr(tk.ID),
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "首页崩溃" || got.Description != "点击即崩" || got.Status != "fixing" ||
		got.Severity != 1 || got.AssigneeID != alice.ID || got.RequirementID != req.ID ||
		got.FoundVersionID != vid || got.FixTaskID != tk.ID {
		t.Fatalf("fields not updated: %+v", got)
	}

	// 清空引用：指向 0 → 落 NULL
	got, err = s.UpdateBug(b.ID, BugChanges{AssigneeID: i64ptr(0), RequirementID: i64ptr(0)}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.AssigneeID != 0 || got.RequirementID != 0 {
		t.Fatalf("refs must be cleared: %+v", got)
	}

	acts := entityActs(t, s, p.ID, "bug")
	// title+desc+status+severity+assignee+requirement+found_version+fix_task + 清空两项 = 10 条
	if len(acts) != 10 {
		t.Fatalf("expected 10 change activities, got %d: %+v", len(acts), acts)
	}
	var statusAct model.Activity
	for _, a := range acts {
		if a.Action == "update_status" {
			statusAct = a
		}
	}
	if statusAct.ID == 0 {
		t.Fatalf("status change must log update_status: %+v", acts)
	}
	if want := `{"field":"status","from":"open","to":"fixing"}`; statusAct.Detail != want {
		t.Fatalf("detail %q must be %q", statusAct.Detail, want)
	}
}

func TestUpdateBugInvalidAndNoopAndMissing(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	b := seedBug(t, s, p.ID, actor, nil)

	// 非法状态/严重级拒绝
	if _, err := s.UpdateBug(b.ID, BugChanges{Status: strptr("resolving")}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
	if _, err := s.UpdateBug(b.ID, BugChanges{Severity: i64ptr(9)}, actor, nil); err == nil {
		t.Fatal("invalid severity must error")
	}

	// 同值 no-op：不落活动
	same := b.Status
	if _, err := s.UpdateBug(b.ID, BugChanges{Status: &same}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if acts := entityActs(t, s, p.ID, "bug"); len(acts) != 0 {
		t.Fatalf("no-change update must not log activity: %+v", acts)
	}

	// 不存在的 bug 报错
	if _, err := s.UpdateBug(999, BugChanges{Title: strptr("x")}, actor, nil); err == nil {
		t.Fatal("updating nonexistent bug must error")
	}
}

// TestUpdateBugFixedToOpenNoReopenRule bug 的 fixed→非 fixed 不特殊化：
// 与其他状态流转一样记 update_status（无任务那样的 reopen 规则）。
func TestUpdateBugFixedToOpenNoReopenRule(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	b := seedBug(t, s, p.ID, actor, func(m *model.Bug) { m.Status = "fixed" })

	got, err := s.UpdateBug(b.ID, BugChanges{Status: strptr("open")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "open" {
		t.Fatalf("status must be open, got %q", got.Status)
	}
	acts := entityActs(t, s, p.ID, "bug")
	if len(acts) != 1 || acts[0].Action != "update_status" {
		t.Fatalf("fixed→open must log plain update_status, got %+v", acts)
	}
}

func TestListBugsFilters(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	alice, err := s.GetOrCreateMember("alice", "human")
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

	seedBug(t, s, p.ID, actor, func(m *model.Bug) { m.Severity = 1 })
	bAssigned := seedBug(t, s, p.ID, actor, func(m *model.Bug) { m.AssigneeID = alice.ID; m.Status = "fixing" })
	bFound := seedBug(t, s, p.ID, actor, func(m *model.Bug) { m.FoundVersionID = vid; m.Severity = 1 })

	// status 过滤
	got, err := s.ListBugs(p.ID, BugFilter{Status: "fixing"})
	if err != nil || len(got) != 1 || got[0].ID != bAssigned.ID {
		t.Fatalf("status filter: got %+v err=%v", got, err)
	}
	// severity 过滤
	got, err = s.ListBugs(p.ID, BugFilter{Severity: 1})
	if err != nil || len(got) != 2 {
		t.Fatalf("severity filter: got %+v err=%v", got, err)
	}
	// assignee 过滤
	got, err = s.ListBugs(p.ID, BugFilter{AssigneeID: alice.ID})
	if err != nil || len(got) != 1 || got[0].ID != bAssigned.ID {
		t.Fatalf("assignee filter: got %+v err=%v", got, err)
	}
	// found-version 过滤
	got, err = s.ListBugs(p.ID, BugFilter{VersionID: vid})
	if err != nil || len(got) != 1 || got[0].ID != bFound.ID {
		t.Fatalf("version filter: got %+v err=%v", got, err)
	}
	// 无过滤：全量
	got, err = s.ListBugs(p.ID, BugFilter{})
	if err != nil || len(got) != 3 {
		t.Fatalf("no filter: got %+v err=%v", got, err)
	}
}

// seedBug 按 mut 定制字段后创建 bug。
func seedBug(t *testing.T, s *Store, projectID int64, actor model.Member, mut func(*model.Bug)) model.Bug {
	t.Helper()
	mb := model.Bug{ProjectID: projectID, Title: "bug"}
	if mut != nil {
		mut(&mb)
	}
	b, err := s.CreateBug(mb, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
