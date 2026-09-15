package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/feishu"
	"github.com/zhangyi/pulse/internal/store"
)

// fakeBitableForAutopush 起一个最小假飞书（token + 记录搜索/新建/更新）并把
// PULSE_FEISHU_ENDPOINT 指过去，返回各类调用计数读取函数与 search 脚本注入器
// （items 形态即飞书 records/search 的原始 item，供墓碑归档等活动路径用）。
func fakeBitableForAutopush(t *testing.T) (createN, updateN func() int, setSearch func(items ...map[string]any)) {
	t.Helper()
	var mu sync.Mutex
	var creates, updates int
	var searchItems []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		respJSONForTest(w, map[string]any{"code": 0, "tenant_access_token": "t-1", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/records/search"):
			mu.Lock()
			items := searchItems
			mu.Unlock()
			if items == nil {
				items = []map[string]any{}
			}
			respJSONForTest(w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/records"):
			mu.Lock()
			creates++
			mu.Unlock()
			respJSONForTest(w, map[string]any{"code": 0, "data": map[string]any{
				"record": map[string]any{"record_id": "recM1"},
			}})
		default:
			mu.Lock()
			updates++
			mu.Unlock()
			respJSONForTest(w, map[string]any{"code": 0})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("PULSE_FEISHU_ENDPOINT", srv.URL)
	return func() int { mu.Lock(); defer mu.Unlock(); return creates },
		func() int { mu.Lock(); defer mu.Unlock(); return updates },
		func(items ...map[string]any) { mu.Lock(); defer mu.Unlock(); searchItems = items }
}

// respJSONForTest 与 cli 包的同名助手同构（本包此前无 HTTP 假服务需要）。
func respJSONForTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// TestMCPAutopushOnWriteTools：装配 AutopushFunc 后，MCP 写工具自动同步——
// add_task 触发 RecordCreate（record_id 回填本地），update_task 触发 RecordUpdate；
// 钩子收到的 agentName 是当前 agent（PULSE_ACTOR 语义），pull 归档墓碑产生的
// archive 活动归因到 agent（而非 cfg.DefaultActor 的人类）；工具结果不受同步影响。
func TestMCPAutopushOnWriteTools(t *testing.T) {
	s := testEnv(t)
	// 配置补飞书凭据（BestEffort 依据凭据判断是否同步）
	cfgPath := config.DefaultPath()
	if err := os.WriteFile(cfgPath, []byte("default_actor: tester\nfeishu:\n  app_id: cli_a1\n  app_secret: sec1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := requireProject(t, s, "demo")
	p.FeishuBitableAppToken = "appB"
	p.FeishuTaskTableID = "tblT"
	p.FeishuVersionTableID = "tblV"
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}
	createN, updateN, setSearch := fakeBitableForAutopush(t)

	// 与 cli/mcp.go 相同的装配方式（钩子带 agentName，CLI 侧传空串维持人类归因）
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var hookAgents []string
	AutopushFunc = func(st *store.Store, projectKey, agentName string) {
		mu.Lock()
		hookAgents = append(hookAgents, agentName)
		mu.Unlock()
		feishu.BestEffort(st, cfg, projectKey, agentName)
	}
	t.Cleanup(func() { AutopushFunc = nil })

	sc := connect(t, s, "claude")
	created := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t1"}))
	if n := createN(); n != 1 {
		t.Fatalf("add_task 应触发 1 次 RecordCreate, got %d", n)
	}
	mu.Lock()
	if len(hookAgents) != 1 || hookAgents[0] != "claude" {
		mu.Unlock()
		t.Fatalf("钩子 agentName = %v, want [claude]", hookAgents)
	}
	mu.Unlock()

	// 远端该记录被别机标记废弃（墓碑）：update_task 触发的 autopush 在 pull 侧归档本地行，
	// archive 活动的执行者必须是 agent（修复前 BestEffort 恒解析 cfg.DefaultActor 人类）
	setSearch(map[string]any{
		"record_id": "recM1", "fields": map[string]any{"已废弃": true},
		"last_modified_time": time.Now().Unix() + 30,
	})
	decode(t, callTool(t, sc, "update_task",
		map[string]any{"id": created["ID"], "status": "done"}))
	if n := updateN(); n == 0 {
		t.Fatal("update_task 必须触发远端 RecordUpdate")
	}
	agent := memberByName(t, s, "claude")
	var sawArchive bool
	for _, a := range activities(t, s, p.ID) {
		if a.EntityType == "task" && a.Action == "archive" {
			if a.ActorID != agent.ID || a.ActorType != "agent" {
				t.Fatalf("autopush archive 活动 actor=%d/%s, want agent claude %d",
					a.ActorID, a.ActorType, agent.ID)
			}
			sawArchive = true
		}
	}
	if !sawArchive {
		t.Fatalf("autopush pull 归档应落 archive 活动: %+v", activities(t, s, p.ID))
	}

	tasks, err := s.ListTasks(p.ID, store.TaskFilter{IncludeArchived: true})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("tasks: %+v err=%v", tasks, err)
	}
	if tasks[0].BitableRecordID == "" || tasks[0].BitableSyncedHash == "" {
		t.Fatalf("record_id/hash 未回填: %+v", tasks[0])
	}
}

// TestMCPNoAutopushWhenUnwired：未装配 AutopushFunc（如单测直连 Register）时写工具照常成功。
func TestMCPNoAutopushWhenUnwired(t *testing.T) {
	s := testEnv(t)
	requireProject(t, s, "demo")
	old := AutopushFunc
	AutopushFunc = nil
	t.Cleanup(func() { AutopushFunc = old })

	sc := connect(t, s, "claude")
	out := callTool(t, sc, "add_task", map[string]any{"project": "demo", "title": "t1"})
	if decode(t, out)["Title"] != "t1" {
		t.Fatalf("add_task must succeed without autopush: %s", out)
	}
}
