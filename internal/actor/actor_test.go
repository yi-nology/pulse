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

// TestResolveExistingMemberTypeWins 槽位类型以库为准（issue #2 回归）：
// 执行者名字进任何槽位，已存在的同名成员直接取回——agent 成员名进
// defaultActor（human 缺省）槽位不再报类型冲突；反向同理。delegatedBy
// 契约上是人类成员，保持严格档。
func TestResolveExistingMemberTypeWins(t *testing.T) {
	tests := []struct {
		name         string
		seed         func(s *store.Store) // 预置既有成员
		defaultActor string
		agentName    string
		wantName     string
		wantType     string
	}{
		{
			name:         "agent 成员名进 defaultActor 槽位：按库取回不冲突",
			seed:         func(s *store.Store) { s.GetOrCreateMember("codex", "agent") },
			defaultActor: "codex",
			wantName:     "codex",
			wantType:     "agent",
		},
		{
			name:         "agent 槽位撞既有 human 成员：同样按库为准",
			seed:         func(s *store.Store) { s.GetOrCreateMember("zhangyi", "human") },
			defaultActor: "op",
			agentName:    "zhangyi",
			wantName:     "zhangyi",
			wantType:     "human",
		},
		{
			name:         "两槽位都不存在：按槽位缺省类型创建",
			seed:         func(s *store.Store) {},
			defaultActor: "zhangyi",
			agentName:    "codex",
			wantName:     "codex",
			wantType:     "agent",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := store.Open(":memory:")
			defer s.Close()
			tc.seed(s)
			a, _, err := Resolve(s, tc.defaultActor, tc.agentName, "")
			if err != nil {
				t.Fatalf("Resolve(%q, %q): %v", tc.defaultActor, tc.agentName, err)
			}
			if a.Name != tc.wantName || a.Type != tc.wantType {
				t.Fatalf("actor = %+v, want name=%q type=%q", a, tc.wantName, tc.wantType)
			}
		})
	}
}
