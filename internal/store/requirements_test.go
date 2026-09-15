package store

import (
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

func TestCreateRequirementDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}

	r, err := s.CreateRequirement(model.Requirement{
		ProjectID: p.ID, Title: "导出报表", Source: "客户反馈",
	}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省值：status=proposed、priority=3；时间戳由 DB 填充
	if r.ID == 0 || r.Title != "导出报表" || r.Status != "proposed" || r.Priority != 3 {
		t.Fatalf("unexpected requirement: %+v", r)
	}
	if r.CreatedAt == "" || r.UpdatedAt == "" {
		t.Fatalf("timestamps must be set: %+v", r)
	}
	got, found, err := s.GetRequirement(r.ID)
	if err != nil || !found {
		t.Fatalf("GetRequirement: found=%v err=%v", found, err)
	}
	if got != r {
		t.Fatalf("round-trip mismatch:\n got %+v\nwant %+v", got, r)
	}

	acts := recentActivities(t, s, p.ID)
	if len(acts) != 1 {
		t.Fatalf("create must log exactly 1 activity, got %d", len(acts))
	}
	a := acts[0]
	if a.Action != "create" || a.EntityType != "requirement" || a.EntityID != r.ID ||
		a.ProjectID != p.ID || a.ActorID != actor.ID || a.OnBehalfOf != boss.ID {
		t.Fatalf("unexpected activity: %+v", a)
	}
}

func TestCreateRequirementInvalidStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	if _, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "x", Status: "doing"}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
}

func TestGetRequirementMissing(t *testing.T) {
	s := openTest(t)
	if _, found, err := s.GetRequirement(999); err != nil || found {
		t.Fatalf("missing requirement: found=%v err=%v", found, err)
	}
}

func TestUpdateRequirementFields(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	alice, err := s.GetOrCreateMember("alice", "human")
	if err != nil {
		t.Fatal(err)
	}
	r := seedRequirement(t, s, p.ID, actor, nil)

	got, err := s.UpdateRequirement(r.ID, RequirementChanges{
		Title:          strptr("新标题"),
		Description:    strptr("说明"),
		OwnerID:        i64ptr(alice.ID),
		Priority:       i64ptr(1),
		Source:         strptr("评审"),
		FeishuDocToken: strptr("doccnTok123"),
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "新标题" || got.Description != "说明" || got.OwnerID != alice.ID ||
		got.Priority != 1 || got.Source != "评审" || got.FeishuDocToken != "doccnTok123" {
		t.Fatalf("fields not updated: %+v", got)
	}

	// 清空 owner：指向 0 → 落 NULL
	got, err = s.UpdateRequirement(r.ID, RequirementChanges{OwnerID: i64ptr(0)}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerID != 0 {
		t.Fatalf("owner must be cleared, got %d", got.OwnerID)
	}

	acts := entityActs(t, s, p.ID, "requirement")
	// title + description + owner + priority + source + doc token + 清空 owner = 7 条 update
	if len(acts) != 7 {
		t.Fatalf("expected 7 update activities, got %d: %+v", len(acts), acts)
	}
	if acts[0].Action != "update" || acts[0].Detail != `{"field":"title","from":"需求","to":"新标题"}` {
		t.Fatalf("unexpected first activity: %+v", acts[0])
	}
	if acts[6].Detail == "" || acts[6].Action != "update" {
		t.Fatalf("unexpected clear-owner activity: %+v", acts[6])
	}
}

func TestUpdateRequirementStatusTransition(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r := seedRequirement(t, s, p.ID, actor, nil)

	got, err := s.UpdateRequirement(r.ID, RequirementChanges{Status: strptr("accepted")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "accepted" {
		t.Fatalf("status must be accepted, got %q", got.Status)
	}
	acts := entityActs(t, s, p.ID, "requirement")
	if len(acts) != 1 || acts[0].Action != "update_status" {
		t.Fatalf("status change must log exactly 1 update_status activity, got %+v", acts)
	}
	if want := `{"field":"status","from":"proposed","to":"accepted"}`; acts[0].Detail != want {
		t.Fatalf("detail %q must be %q", acts[0].Detail, want)
	}

	// 非法状态拒绝
	if _, err := s.UpdateRequirement(r.ID, RequirementChanges{Status: strptr("doing")}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
}

func TestUpdateRequirementNoopAndMissing(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r := seedRequirement(t, s, p.ID, actor, nil)

	same := r.Status
	if _, err := s.UpdateRequirement(r.ID, RequirementChanges{Status: &same}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if acts := entityActs(t, s, p.ID, "requirement"); len(acts) != 0 {
		t.Fatalf("no-change update must not log activity: %+v", acts)
	}
	if _, err := s.UpdateRequirement(999, RequirementChanges{Title: strptr("x")}, actor, nil); err == nil {
		t.Fatal("updating nonexistent requirement must error")
	}
}

func TestListRequirementsFilterStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	acc := seedRequirement(t, s, p.ID, actor, func(m *model.Requirement) { m.Status = "accepted" })
	seedRequirement(t, s, p.ID, actor, nil)

	got, err := s.ListRequirements(p.ID, "accepted")
	if err != nil || len(got) != 1 || got[0].ID != acc.ID {
		t.Fatalf("status filter: got %+v err=%v", got, err)
	}
	got, err = s.ListRequirements(p.ID, "")
	if err != nil || len(got) != 2 {
		t.Fatalf("no filter: got %+v err=%v", got, err)
	}
}

// seedRequirement 按 mut 定制字段后创建需求。
func seedRequirement(t *testing.T, s *Store, projectID int64, actor model.Member, mut func(*model.Requirement)) model.Requirement {
	t.Helper()
	mr := model.Requirement{ProjectID: projectID, Title: "需求"}
	if mut != nil {
		mut(&mr)
	}
	r, err := s.CreateRequirement(mr, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// entityActs 取近 1 小时内指定实体类型的"变更类"活动（排除 create）。
func entityActs(t *testing.T, s *Store, projectID int64, entityType string) []model.Activity {
	t.Helper()
	var out []model.Activity
	for _, a := range recentActivities(t, s, projectID) {
		if a.EntityType == entityType && a.Action != "create" {
			out = append(out, a)
		}
	}
	return out
}
