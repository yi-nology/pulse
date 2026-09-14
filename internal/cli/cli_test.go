package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/store"
)

// testEnv 把 PULSE_HOME 重定向到临时目录，返回可供断言的 store 与输出缓冲。
func testEnv(t *testing.T) (*store.Store, *config.Config, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PULSE_HOME", dir)
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("default_actor: tester\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := store.Open(filepath.Join(dir, "pulse.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, cfg, &bytes.Buffer{}
}

// runCLI 执行一次命令并返回 stdout/stderr；生产 Execute 会把返回的 error 映射为退出码 1。
func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	root := newRootCmd()
	root.SetArgs(args)
	root.SetOut(out)
	root.SetErr(errOut)
	err := root.Execute()
	return out.String(), errOut.String(), err
}

func requireActivity(t *testing.T, s *store.Store, projectID int64, action, entityType string, actorName string) {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 1 || acts[0].Action != action || acts[0].EntityType != entityType {
		t.Fatalf("unexpected activity: %+v", acts)
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == acts[0].ActorID && m.Name == actorName {
			return
		}
	}
	t.Fatalf("activity actor %d not resolved to member %q: %+v", acts[0].ActorID, actorName, ms)
}

func TestInitCreatesProject(t *testing.T) {
	s, _, _ := testEnv(t)
	out, errOut, err := runCLI(t, "init", "demo")
	if err != nil {
		t.Fatalf("init failed: %v stderr=%s", err, errOut)
	}
	if want := "项目已创建: demo (id=1)"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project not persisted: found=%v err=%v", found, err)
	}
	if p.Name != "demo" || p.Status != "active" {
		t.Fatalf("unexpected project: %+v", p)
	}
	requireActivity(t, s, p.ID, "create", "project", "tester") // default_actor 经 actor.Resolve 落库
}

func TestInitDuplicateFails(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatalf("first init failed: %v", err)
	}
	_, errOut, err := runCLI(t, "init", "demo")
	if err == nil {
		t.Fatal("duplicate init must fail")
	}
	if want := "项目已存在: demo"; !strings.Contains(errOut, want) {
		t.Fatalf("stderr %q must contain %q", errOut, want)
	}
}

func TestProjectList(t *testing.T) {
	s, _, _ := testEnv(t)
	if _, err := s.CreateProject("demo", "演示", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProject("lab", "试验田", ""); err != nil {
		t.Fatal(err)
	}
	out, _, err := runCLI(t, "project", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"demo", "lab"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
}

func TestMemberAddAndList(t *testing.T) {
	s, _, _ := testEnv(t)
	out, errOut, err := runCLI(t, "member", "add", "alice", "--type", "agent", "--capacity", "3")
	if err != nil {
		t.Fatalf("member add failed: %v stderr=%s", err, errOut)
	}
	if want := "成员已添加: alice"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	var alice *struct {
		typ string
		cap float64
	}
	tester := 0
	for _, m := range ms {
		switch m.Name {
		case "alice":
			alice = &struct {
				typ string
				cap float64
			}{m.Type, m.Capacity}
		case "tester":
			tester++
		}
	}
	if alice == nil || alice.typ != "agent" || alice.cap != 3 {
		t.Fatalf("alice not persisted correctly: %+v", ms)
	}
	if tester == 0 {
		t.Fatalf("default_actor tester must be ensured by actor.Resolve: %+v", ms)
	}
	// 成员动作无项目归属（project_id 落 NULL），直接查 activity 表验证已落活动
	db, err := sql.Open("sqlite", config.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM activity WHERE action = 'create' AND entity_type = 'member'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("member add must log 1 activity, got %d", n)
	}

	// 缺省 --type 为 human、--capacity 为 5
	if _, _, err := runCLI(t, "member", "add", "bob"); err != nil {
		t.Fatal(err)
	}
	out, _, err = runCLI(t, "member", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"alice", "bob"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
	ms, err = s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "bob" && (m.Type != "human" || m.Capacity != 5) {
			t.Fatalf("bob defaults wrong: %+v", m)
		}
	}
}

func TestMemberAddRejectsBadType(t *testing.T) {
	_, _, _ = testEnv(t)
	_, _, err := runCLI(t, "member", "add", "eve", "--type", "robot")
	if err == nil {
		t.Fatal("bad --type must fail")
	}
}
