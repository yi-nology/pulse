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
