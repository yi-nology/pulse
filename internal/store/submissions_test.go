package store

import (
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

func seedVersionRow(t *testing.T, s *Store, projectID int64, name string) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO versions (project_id, name) VALUES (?, ?)`, projectID, name)
	if err != nil {
		t.Fatal(err)
	}
	vid, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return vid
}

func TestCreateTestSubmissionDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}
	vid := seedVersionRow(t, s, p.ID, "v1")

	sub, err := s.CreateTestSubmission(model.TestSubmission{
		ProjectID: p.ID, VersionID: vid, Scope: "核心模块",
	}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省值：status=draft；draft 状态 submitted_at/concluded_at 为空；submitted_by 归操作者
	if sub.ID == 0 || sub.Status != "draft" || sub.Scope != "核心模块" {
		t.Fatalf("unexpected submission: %+v", sub)
	}
	if sub.SubmittedAt != "" || sub.ConcludedAt != "" {
		t.Fatalf("draft must not carry timestamps: %+v", sub)
	}
	if sub.SubmittedBy != actor.ID || sub.CreatedAt == "" || sub.UpdatedAt == "" {
		t.Fatalf("defaults missing: %+v", sub)
	}

	// 直接以 submitted 创建：submitted_at 立即补记
	sub2, err := s.CreateTestSubmission(model.TestSubmission{
		ProjectID: p.ID, VersionID: vid, Status: "submitted",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sub2.Status != "submitted" || sub2.SubmittedAt == "" {
		t.Fatalf("submitted creation must stamp submitted_at: %+v", sub2)
	}

	creates := 0
	for _, a := range recentActivities(t, s, p.ID) {
		if a.EntityType == "test_submission" {
			creates++
			if a.EntityID != sub.ID {
				continue // sub2（无 behalf）的活动另行存在，仅计数
			}
			if a.Action != "create" || a.OnBehalfOf != boss.ID ||
				a.ActorID != actor.ID || a.ProjectID != p.ID {
				t.Fatalf("unexpected activity: %+v", a)
			}
		}
	}
	if creates != 2 {
		t.Fatalf("each submission must log exactly 1 create activity, got %d", creates)
	}
}

func TestCreateTestSubmissionInvalidStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	vid := seedVersionRow(t, s, p.ID, "v1")
	if _, err := s.CreateTestSubmission(model.TestSubmission{ProjectID: p.ID, VersionID: vid, Status: "done"}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
}

func TestUpdateTestSubmissionStatusAndTimestamps(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	vid := seedVersionRow(t, s, p.ID, "v1")
	alice, err := s.GetOrCreateMember("alice", "human")
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateTestSubmission(model.TestSubmission{ProjectID: p.ID, VersionID: vid}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	// draft → submitted：submitted_at 补记
	got, err := s.UpdateTestSubmission(sub.ID, SubmissionChanges{
		Status: strptr("submitted"), Scope: strptr("全量回归"), TestOwnerID: i64ptr(alice.ID),
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "submitted" || got.SubmittedAt == "" || got.ConcludedAt != "" {
		t.Fatalf("submitted must stamp submitted_at only: %+v", got)
	}
	if got.Scope != "全量回归" || got.TestOwnerID != alice.ID {
		t.Fatalf("fields not updated: %+v", got)
	}

	// submitted → passed：concluded_at 补记；detail 记 update_status
	got, err = s.UpdateTestSubmission(sub.ID, SubmissionChanges{Status: strptr("passed")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" || got.ConcludedAt == "" {
		t.Fatalf("passed must stamp concluded_at: %+v", got)
	}
	acts := entityActs(t, s, p.ID, "test_submission")
	// submitted + scope + test_owner + passed = 4 条
	if len(acts) != 4 {
		t.Fatalf("expected 4 change activities, got %d: %+v", len(acts), acts)
	}
	if acts[0].Action != "update_status" ||
		acts[0].Detail != `{"field":"status","from":"draft","to":"submitted"}` {
		t.Fatalf("unexpected first activity: %+v", acts[0])
	}
	if acts[3].Detail != `{"field":"status","from":"submitted","to":"passed"}` {
		t.Fatalf("unexpected last activity: %+v", acts[3])
	}

	// 非法状态拒绝
	if _, err := s.UpdateTestSubmission(sub.ID, SubmissionChanges{Status: strptr("done")}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
	// 同值 no-op
	same := got.Status
	if _, err := s.UpdateTestSubmission(sub.ID, SubmissionChanges{Status: &same}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if acts := entityActs(t, s, p.ID, "test_submission"); len(acts) != 4 {
		t.Fatalf("no-change update must not log activity: %+v", acts)
	}
	// 不存在的提测单报错
	if _, err := s.UpdateTestSubmission(999, SubmissionChanges{Scope: strptr("x")}, actor, nil); err == nil {
		t.Fatal("updating nonexistent submission must error")
	}
}

// TestUpdateTestSubmissionConcludedAtStable concluded_at 仅在真实流转进入 passed/failed
// 时补记一次：对已定论提测单做 scope 或 doc-token 写回（Task 3 通道）等无关更新
// 不得重置结论时刻；submitted_at 仅填空，天然安全。
func TestUpdateTestSubmissionConcludedAtStable(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	vid := seedVersionRow(t, s, p.ID, "v1")
	sub, err := s.CreateTestSubmission(model.TestSubmission{ProjectID: p.ID, VersionID: vid}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 草稿阶段的 scope-only 更新：不产生任何时间戳
	got, err := s.UpdateTestSubmission(sub.ID, SubmissionChanges{Scope: strptr("范围")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.SubmittedAt != "" || got.ConcludedAt != "" {
		t.Fatalf("draft scope-only update must not stamp: %+v", got)
	}

	// 流转进入 failed：concluded_at 补记一次
	got, err = s.UpdateTestSubmission(sub.ID, SubmissionChanges{Status: strptr("failed")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConcludedAt == "" || got.SubmittedAt == "" {
		t.Fatalf("transition into failed must stamp both timestamps: %+v", got)
	}
	// 把时刻拨回 2000 年：now 为秒级精度，同秒内的错误重置无法察觉，先归零再触发
	if _, err := s.db.Exec(`UPDATE test_submissions SET concluded_at = '2000-01-01 00:00:00' WHERE id = ?`, sub.ID); err != nil {
		t.Fatal(err)
	}

	// doc-token 写回：concluded_at / submitted_at 均不变
	got, err = s.UpdateTestSubmission(sub.ID, SubmissionChanges{FeishuDocToken: strptr("doc")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConcludedAt != "2000-01-01 00:00:00" {
		t.Fatalf("doc-token write-back must keep concluded_at, got %q", got.ConcludedAt)
	}
	if got.SubmittedAt == "" {
		t.Fatal("submitted_at must remain stamped")
	}
}

func TestListTestSubmissionsFilterVersion(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	v1 := seedVersionRow(t, s, p.ID, "v1")
	v2 := seedVersionRow(t, s, p.ID, "v2")

	a, err := s.CreateTestSubmission(model.TestSubmission{ProjectID: p.ID, VersionID: v1}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTestSubmission(model.TestSubmission{ProjectID: p.ID, VersionID: v2}, actor, nil); err != nil {
		t.Fatal(err)
	}

	got, err := s.ListTestSubmissions(p.ID, v1)
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("version filter: got %+v err=%v", got, err)
	}
	got, err = s.ListTestSubmissions(p.ID, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("no filter: got %+v err=%v", got, err)
	}
}
