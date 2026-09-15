package store

import (
	"strings"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// docTokenEnv 准备六实体的 doc-token 写回测试环境：库 + 项目 + tester 操作者。
func docTokenEnv(t *testing.T) (*Store, model.Project, model.Member) {
	t.Helper()
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	return s, p, actor
}

// wantDocTokenActivity 断言项目内恰好有一条 action="feishu_record_doc" 且指向该实体的活动。
func wantDocTokenActivity(t *testing.T, s *Store, projectID, entityID int64, entityType, actorName string) {
	t.Helper()
	acts := recentActivities(t, s, projectID)
	n := 0
	for _, a := range acts {
		if a.Action == "feishu_record_doc" && a.EntityType == entityType && a.EntityID == entityID {
			n++
			if a.ActorID == 0 {
				t.Fatalf("feishu_record_doc activity must carry actor: %+v", a)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want exactly 1 %s feishu_record_doc activity, got %d: %+v", entityType, n, acts)
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == actorIDOf(t, s, projectID, entityType, entityID) && m.Name == actorName {
			return
		}
	}
	t.Fatalf("activity actor not resolved to %q", actorName)
}

// actorIDOf 取该实体 feishu_record_doc 活动的 actor_id。
func actorIDOf(t *testing.T, s *Store, projectID int64, entityType string, entityID int64) int64 {
	t.Helper()
	acts := recentActivities(t, s, projectID)
	for _, a := range acts {
		if a.Action == "feishu_record_doc" && a.EntityType == entityType && a.EntityID == entityID {
			return a.ActorID
		}
	}
	t.Fatalf("no feishu_record_doc activity for %s#%d", entityType, entityID)
	return 0
}

// TestSetRecordDocTokenAllEntities：五实体经 SetRecordDocToken 回写 token 后可读回，
// 且各落一条 feishu_record_doc 活动（entity_type 与实体对应）。
func TestSetRecordDocTokenAllEntities(t *testing.T) {
	s, p, actor := docTokenEnv(t)

	r, err := s.CreateRequirement(model.Requirement{ProjectID: p.ID, Title: "R"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "requirement"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "M"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateTestSubmission(model.TestSubmission{ProjectID: p.ID, VersionID: ver.ID}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: ver.ID}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		entity string
		id     int64
		read   func() string
	}{
		{"requirement", r.ID, func() string { got, _, _ := s.GetRequirement(r.ID); return got.FeishuDocToken }},
		{"review", v.ID, func() string { got, _, _ := s.GetReview(v.ID); return got.FeishuDocToken }},
		{"meeting", m.ID, func() string { got, _, _ := s.GetMeeting(m.ID); return got.FeishuDocToken }},
		{"test_submission", sub.ID, func() string { got, _, _ := s.GetTestSubmission(sub.ID); return got.FeishuDocToken }},
		{"release", rel.ID, func() string { got, _, _ := s.GetRelease(rel.ID); return got.FeishuDocToken }},
	}
	for _, tc := range cases {
		if err := s.SetRecordDocToken(tc.entity, tc.id, "docT", actor, nil); err != nil {
			t.Fatalf("SetRecordDocToken(%s): %v", tc.entity, err)
		}
		if got := tc.read(); got != "docT" {
			t.Fatalf("%s token = %q, want docT", tc.entity, got)
		}
		wantDocTokenActivity(t, s, p.ID, tc.id, tc.entity, "actor")
	}
}

// TestSetReviewMeetingDocTokenNamedWrappers：命名封装与泛型入口同语义（评审/会议无
// Update* token 通道，飞书 adapter 专用）。
func TestSetReviewMeetingDocTokenNamedWrappers(t *testing.T) {
	s, p, actor := docTokenEnv(t)
	v, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "test"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "站会"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetReviewDocToken(v.ID, "docT", actor, nil); err != nil {
		t.Fatalf("SetReviewDocToken: %v", err)
	}
	if err := s.SetMeetingDocToken(m.ID, "docT", actor, nil); err != nil {
		t.Fatalf("SetMeetingDocToken: %v", err)
	}
	if got, _, _ := s.GetReview(v.ID); got.FeishuDocToken != "docT" {
		t.Fatalf("review token = %q", got.FeishuDocToken)
	}
	if got, _, _ := s.GetMeeting(m.ID); got.FeishuDocToken != "docT" {
		t.Fatalf("meeting token = %q", got.FeishuDocToken)
	}
	wantDocTokenActivity(t, s, p.ID, v.ID, "review", "actor")
	wantDocTokenActivity(t, s, p.ID, m.ID, "meeting", "actor")
}

// TestSetRecordDocTokenIdempotent：同值写入为 no-op——不刷新 updated_at、不追加活动。
func TestSetRecordDocTokenIdempotent(t *testing.T) {
	s, p, actor := docTokenEnv(t)
	v, err := s.CreateReview(model.Review{ProjectID: p.ID, Kind: "release"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetReviewDocToken(v.ID, "docT", actor, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ := s.GetReview(v.ID)
	if err := s.SetRecordDocToken("review", v.ID, "docT", actor, nil); err != nil {
		t.Fatalf("same-value write must be no-op, got %v", err)
	}
	again, _, _ := s.GetReview(v.ID)
	if again.UpdatedAt != got.UpdatedAt {
		t.Fatalf("same-value write refreshed updated_at: %s -> %s", got.UpdatedAt, again.UpdatedAt)
	}
	acts := recentActivities(t, s, p.ID)
	if n := len(acts); n != 2 { // create + 首次 feishu_record_doc
		t.Fatalf("same-value write must not log activity, activities = %d: %+v", n, acts)
	}
}

// TestSetRecordDocTokenErrors：实体不存在与未知实体名都要报错。
func TestSetRecordDocTokenErrors(t *testing.T) {
	s, _, actor := docTokenEnv(t)
	if err := s.SetRecordDocToken("requirement", 999, "docT", actor, nil); err == nil ||
		!strings.Contains(err.Error(), "需求不存在") {
		t.Fatalf("want 需求不存在 error, got %v", err)
	}
	if err := s.SetRecordDocToken("meeting", 999, "docT", actor, nil); err == nil ||
		!strings.Contains(err.Error(), "会议不存在") {
		t.Fatalf("want 会议不存在 error, got %v", err)
	}
	if err := s.SetRecordDocToken("bug", 1, "docT", actor, nil); err == nil ||
		!strings.Contains(err.Error(), "未知实体") {
		t.Fatalf("want 未知实体 error, got %v", err)
	}
}
