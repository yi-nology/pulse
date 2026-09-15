package store

import (
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/model"
)

func TestGetOrCreateMemberIdempotent(t *testing.T) {
	s := openTest(t)
	m1, err := s.GetOrCreateMember("zhangyi", "human")
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.GetOrCreateMember("zhangyi", "human")
	if err != nil || m1.ID != m2.ID {
		t.Fatalf("must reuse same member: %v %v", m1, m2)
	}
	if _, err := s.GetOrCreateMember("zhangyi", "agent"); err == nil {
		t.Fatal("type conflict must error")
	}
}

func TestGetMemberByName(t *testing.T) {
	s := openTest(t)
	if _, found, err := s.GetMemberByName("ghost"); err != nil || found {
		t.Fatalf("missing member: found=%v err=%v", found, err)
	}
	m, err := s.GetOrCreateMember("codex", "agent")
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := s.GetMemberByName("codex")
	if err != nil || !found {
		t.Fatalf("existing member: found=%v err=%v", found, err)
	}
	if got.ID != m.ID || got.Type != "agent" {
		t.Fatalf("got %+v, want id=%d type=agent", got, m.ID)
	}
}

func TestSetMemberCapacity(t *testing.T) {
	s := openTest(t)
	m, err := s.GetOrCreateMember("alice", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMemberCapacity(m.ID, 3); err != nil {
		t.Fatal(err)
	}
	ms, err := s.ListMembers()
	if err != nil || len(ms) != 1 || ms[0].Capacity != 3 {
		t.Fatalf("capacity not updated: %v %v", ms, err)
	}
	if err := s.SetMemberCapacity(999, 3); err == nil {
		t.Fatal("nonexistent member must error")
	}
}

func TestActivityWindow(t *testing.T) {
	s := openTest(t)
	// activity.project_id 外键引用 projects(id)（Open 开启了 foreign_keys），
	// 先植入 id=1 的项目以满足外键约束。
	if _, err := s.db.Exec(`INSERT INTO projects (key) VALUES ('p1')`); err != nil {
		t.Fatal(err)
	}
	actor, _ := s.GetOrCreateMember("a", "human")
	if err := s.LogActivity(model.Activity{ProjectID: 1, ActorID: actor.ID, ActorType: "human",
		Action: "create", EntityType: "task", EntityID: 1}); err != nil {
		t.Fatal(err)
	}
	acts, err := s.ActivitiesInWindow(1, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil || len(acts) != 1 {
		t.Fatalf("got %v, %v", acts, err)
	}
}
