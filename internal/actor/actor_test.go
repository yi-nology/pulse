package actor

import (
	"testing"

	"github.com/zhangyi/pulse/internal/store"
)

func TestResolveHumanDefault(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	a, behalf, err := Resolve(s, "zhangyi", "", "")
	if err != nil || a.Name != "zhangyi" || a.Type != "human" || behalf != nil {
		t.Fatalf("got %v %v %v", a, behalf, err)
	}
}

func TestResolveAgentWithDelegatedBy(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	a, behalf, err := Resolve(s, "zhangyi", "codex", "zhangyi")
	if err != nil || a.Type != "agent" || a.Name != "codex" || behalf == nil || behalf.Name != "zhangyi" {
		t.Fatalf("got %v %v %v", a, behalf, err)
	}
}

func TestResolveNoDefaultErrors(t *testing.T) {
	s, _ := store.Open(":memory:")
	defer s.Close()
	if _, _, err := Resolve(s, "", "", ""); err == nil {
		t.Fatal("empty default actor must error")
	}
}
