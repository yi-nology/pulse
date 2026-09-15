package store

import (
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

func TestCreateReleaseDefaultsAndActivity(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}
	vid := seedVersionRow(t, s, p.ID, "v1")

	rel, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: vid}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	// 缺省值：status=preparing、released_at 为空、发布负责人归操作者
	if rel.ID == 0 || rel.Status != "preparing" || rel.ReleasedAt != "" {
		t.Fatalf("unexpected release: %+v", rel)
	}
	if rel.ReleaseManagerID != actor.ID || rel.VersionID != vid ||
		rel.CreatedAt == "" || rel.UpdatedAt == "" {
		t.Fatalf("defaults missing: %+v", rel)
	}

	// 直接以 released 创建：released_at 立即补记
	rel2, err := s.CreateRelease(model.Release{
		ProjectID: p.ID, VersionID: vid, Status: "released",
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel2.Status != "released" || rel2.ReleasedAt == "" {
		t.Fatalf("released creation must stamp released_at: %+v", rel2)
	}

	creates := 0
	for _, a := range recentActivities(t, s, p.ID) {
		if a.EntityType == "release" {
			creates++
			if a.Action != "create" {
				t.Fatalf("unexpected action: %+v", a)
			}
			if a.EntityID == rel.ID && (a.OnBehalfOf != boss.ID || a.ActorID != actor.ID || a.ProjectID != p.ID) {
				t.Fatalf("unexpected behalf attribution: %+v", a)
			}
		}
	}
	if creates != 2 {
		t.Fatalf("each release must log exactly 1 create activity, got %d", creates)
	}
}

func TestCreateReleaseInvalidStatus(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	vid := seedVersionRow(t, s, p.ID, "v1")
	if _, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: vid, Status: "done"}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
}

func TestUpdateReleaseStatusAndTimestamps(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	vid := seedVersionRow(t, s, p.ID, "v1")
	rel, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: vid}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	// preparing → released：released_at 补记
	got, err := s.UpdateRelease(rel.ID, ReleaseChanges{
		Status: strptr("released"), Notes: strptr("灰度 10%"),
	}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "released" || got.ReleasedAt == "" || got.Notes != "灰度 10%" {
		t.Fatalf("release not updated: %+v", got)
	}
	acts := entityActs(t, s, p.ID, "release")
	if len(acts) != 2 {
		t.Fatalf("expected 2 change activities, got %d: %+v", len(acts), acts)
	}
	if acts[0].Action != "update_status" ||
		acts[0].Detail != `{"field":"status","from":"preparing","to":"released"}` {
		t.Fatalf("unexpected first activity: %+v", acts[0])
	}
	if acts[1].Action != "update" || acts[1].Detail != `{"field":"notes","from":"","to":"灰度 10%"}` {
		t.Fatalf("unexpected notes activity: %+v", acts[1])
	}

	// released → rolled_back：正常流转（无 reopen 规则）
	got, err = s.UpdateRelease(rel.ID, ReleaseChanges{Status: strptr("rolled_back")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "rolled_back" {
		t.Fatalf("status must be rolled_back, got %q", got.Status)
	}

	// 非法状态拒绝
	if _, err := s.UpdateRelease(rel.ID, ReleaseChanges{Status: strptr("done")}, actor, nil); err == nil {
		t.Fatal("invalid status must error")
	}
	// 同值 no-op
	same := got.Status
	if _, err := s.UpdateRelease(rel.ID, ReleaseChanges{Status: &same}, actor, nil); err != nil {
		t.Fatal(err)
	}
	if acts := entityActs(t, s, p.ID, "release"); len(acts) != 3 {
		t.Fatalf("no-change update must not log activity: %+v", acts)
	}
	// 不存在的发版报错
	if _, err := s.UpdateRelease(999, ReleaseChanges{Notes: strptr("x")}, actor, nil); err == nil {
		t.Fatal("updating nonexistent release must error")
	}
}

// TestUpdateReleaseUnrelatedFieldsKeepReleasedAt released_at 仅在真实流转进入 released
// 时补记：已 released 的发版单做 notes 或 doc-token 写回（Task 3 通道）等无关字段
// 更新时，该时刻不得被重置。
func TestUpdateReleaseUnrelatedFieldsKeepReleasedAt(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	vid := seedVersionRow(t, s, p.ID, "v1")
	rel, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: vid}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}

	rel, err = s.UpdateRelease(rel.ID, ReleaseChanges{Status: strptr("released")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rel.ReleasedAt == "" {
		t.Fatal("transition into released must stamp released_at")
	}
	// 把时刻拨回 2000 年：now 为秒级精度，同秒内的错误重置无法察觉，先归零再触发
	if _, err := s.db.Exec(`UPDATE releases SET released_at = '2000-01-01 00:00:00' WHERE id = ?`, rel.ID); err != nil {
		t.Fatal(err)
	}

	// notes-only 更新：released_at 不变
	got, err := s.UpdateRelease(rel.ID, ReleaseChanges{Notes: strptr("补记")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReleasedAt != "2000-01-01 00:00:00" {
		t.Fatalf("notes-only update must keep released_at, got %q", got.ReleasedAt)
	}
	// doc-token 写回：released_at 不变
	got, err = s.UpdateRelease(rel.ID, ReleaseChanges{FeishuDocToken: strptr("doc")}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReleasedAt != "2000-01-01 00:00:00" {
		t.Fatalf("doc-token write-back must keep released_at, got %q", got.ReleasedAt)
	}
}

func TestListReleases(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	v1 := seedVersionRow(t, s, p.ID, "v1")
	v2 := seedVersionRow(t, s, p.ID, "v2")

	r1, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: v1}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := s.CreateRelease(model.Release{ProjectID: p.ID, VersionID: v2}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ListReleases(p.ID)
	if err != nil || len(got) != 2 || got[0].ID != r1.ID || got[1].ID != r2.ID {
		t.Fatalf("list releases: got %+v err=%v", got, err)
	}
	// 项目隔离
	other, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListReleases(other.ID); err != nil || len(got) != 0 {
		t.Fatalf("project isolation: got %+v err=%v", got, err)
	}
}
