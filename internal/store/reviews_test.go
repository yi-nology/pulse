package store

import (
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

func TestCreateReviewDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}
	req := seedRequirement(t, s, p.ID, actor, nil)

	v, err := s.CreateReview(model.Review{
		ProjectID: p.ID, RequirementID: req.ID, Kind: "requirement",
	}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省值：conclusion=pending、held_at 补当前时刻
	if v.ID == 0 || v.Conclusion != "pending" || v.Kind != "requirement" {
		t.Fatalf("unexpected review: %+v", v)
	}
	if v.HeldAt == "" || v.CreatedAt == "" || v.UpdatedAt == "" {
		t.Fatalf("timestamps must be set: %+v", v)
	}
	if v.CreatedBy != actor.ID {
		t.Fatalf("created_by must default to actor: %+v", v)
	}

	var acts []model.Activity
	for _, a := range recentActivities(t, s, p.ID) {
		if a.EntityType == "review" {
			acts = append(acts, a)
		}
	}
	if len(acts) != 1 {
		t.Fatalf("create must log exactly 1 review activity, got %d", len(acts))
	}
	a := acts[0]
	if a.Action != "create" || a.EntityType != "review" || a.EntityID != v.ID ||
		a.ProjectID != p.ID || a.ActorID != actor.ID || a.OnBehalfOf != boss.ID {
		t.Fatalf("unexpected activity: %+v", a)
	}
}

func TestCreateReviewInvalidKindOrConclusion(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	// kind 留空/非法报错
	if _, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: ""}, actor, nil); err == nil {
		t.Fatal("empty kind must error")
	}
	if _, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "design"}, actor, nil); err == nil {
		t.Fatal("invalid kind must error")
	}
	// conclusion 非法报错
	if _, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "test", Conclusion: "ok"}, actor, nil); err == nil {
		t.Fatal("invalid conclusion must error")
	}
}

func TestUpdateReviewConclusion(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "requirement"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.UpdateReviewConclusion(v.ID, "passed_with_notes", actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	if got.Conclusion != "passed_with_notes" {
		t.Fatalf("conclusion not updated: %+v", got)
	}
	acts := entityActs(t, s, p.ID, "review")
	if len(acts) != 1 || acts[0].Action != "update" {
		t.Fatalf("conclusion change must log exactly 1 update activity, got %+v", acts)
	}
	if want := `{"field":"conclusion","from":"pending","to":"passed_with_notes"}`; acts[0].Detail != want {
		t.Fatalf("detail %q must be %q", acts[0].Detail, want)
	}
	if acts[0].OnBehalfOf != boss.ID {
		t.Fatalf("behalf attribution missing: %+v", acts[0])
	}

	// 同值 no-op：不落活动
	if _, err := s.UpdateReviewConclusion(v.ID, "passed_with_notes", actor, nil); err != nil {
		t.Fatal(err)
	}
	if acts := entityActs(t, s, p.ID, "review"); len(acts) != 1 {
		t.Fatalf("same-value conclude must not log activity: %+v", acts)
	}
	// 非法 conclusion 报错（含空值）
	if _, err := s.UpdateReviewConclusion(v.ID, "ok", actor, nil); err == nil {
		t.Fatal("invalid conclusion must error")
	}
	// 不存在的评审报错
	if _, err := s.UpdateReviewConclusion(999, "passed", actor, nil); err == nil {
		t.Fatal("concluding nonexistent review must error")
	}
}

func TestListReviewsFilterByRequirement(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	r1 := seedRequirement(t, s, p.ID, actor, nil)
	r2 := seedRequirement(t, s, p.ID, actor, nil)

	v1, err := s.CreateReview(model.Review{ProjectID: p.ID, RequirementID: r1.ID, Kind: "requirement"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReview(model.Review{ProjectID: p.ID, RequirementID: r2.ID, Kind: "release"}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "test"}, actor, nil); err != nil {
		t.Fatal(err)
	}

	// requirementID=0 → 全部
	got, err := s.ListReviews(p.ID, 0)
	if err != nil || len(got) != 3 {
		t.Fatalf("no filter: got %+v err=%v", got, err)
	}
	// 按需求过滤
	got, err = s.ListReviews(p.ID, r1.ID)
	if err != nil || len(got) != 1 || got[0].ID != v1.ID {
		t.Fatalf("requirement filter: got %+v err=%v", got, err)
	}
}
