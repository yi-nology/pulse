package store

import (
	"testing"

	"github.com/zhangyi/pulse/internal/model"
)

// 会议实体无 status/update/get（仅 create + list），核心断言：create 落活动、
// held_at/created_by 缺省、列表按 id 升序。
func TestCreateMeetingAndList(t *testing.T) {
	s := openTest(t)
	p := seedProject(t, s)
	actor := taskActor(t, s)
	boss, err := s.GetOrCreateMember("boss", "human")
	if err != nil {
		t.Fatal(err)
	}

	m, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "迭代站会"}, actor, &boss)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID == 0 || m.Title != "迭代站会" {
		t.Fatalf("unexpected meeting: %+v", m)
	}
	// held_at 留空补当前时刻；created_by 补操作者
	if m.HeldAt == "" || m.CreatedBy != actor.ID || m.CreatedAt == "" || m.UpdatedAt == "" {
		t.Fatalf("defaults missing: %+v", m)
	}

	// 显式 held_at 保留原值
	m2, err := s.CreateMeeting(model.Meeting{ProjectID: p.ID, Title: "评审会", HeldAt: "2026-09-01 10:00:00"}, actor, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m2.HeldAt != "2026-09-01 10:00:00" {
		t.Fatalf("explicit held_at must be kept: %+v", m2)
	}

	// 列表按 id 升序
	got, err := s.ListMeetings(p.ID)
	if err != nil || len(got) != 2 || got[0].ID != m.ID || got[1].ID != m2.ID {
		t.Fatalf("list meetings: got %+v err=%v", got, err)
	}

	// create 活动：归因（含 behalf）
	var meetActs []model.Activity
	for _, a := range recentActivities(t, s, p.ID) {
		if a.EntityType == "meeting" {
			meetActs = append(meetActs, a)
		}
	}
	if len(meetActs) != 2 {
		t.Fatalf("each meeting must log exactly 1 create activity, got %+v", meetActs)
	}
	if meetActs[0].Action != "create" || meetActs[0].EntityID != m.ID ||
		meetActs[0].ActorID != actor.ID || meetActs[0].OnBehalfOf != boss.ID {
		t.Fatalf("unexpected activity: %+v", meetActs[0])
	}

	// 项目隔离：其他项目看不到
	other, err := s.CreateProject("other", "其他", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.ListMeetings(other.ID); err != nil || len(got) != 0 {
		t.Fatalf("project isolation: got %+v err=%v", got, err)
	}
}
