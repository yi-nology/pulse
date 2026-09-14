package mcpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/feishu"
	"github.com/zhangyi/pulse/internal/store"
)

// fakeBitableForAutopush 起一个最小假飞书（token + 记录搜索/新建/更新）并把
// PULSE_FEISHU_ENDPOINT 指过去，返回各类调用计数读取函数。
func fakeBitableForAutopush(t *testing.T) (createN, updateN func() int) {
	t.Helper()
	var mu sync.Mutex
	var creates, updates int
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		respJSONForTest(w, map[string]any{"code": 0, "tenant_access_token": "t-1", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/records/search"):
			respJSONForTest(w, map[string]any{"code": 0, "data": map[string]any{"items": []any{}}})
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
		func() int { mu.Lock(); defer mu.Unlock(); return updates }
}

// respJSONForTest 与 cli 包的同名助手同构（本包此前无 HTTP 假服务需要）。
func respJSONForTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// TestMCPAutopushOnWriteTools：装配 AutopushFunc 后，MCP 写工具自动同步——
// add_task 触发 RecordCreate（record_id 回填本地），update_task 触发 RecordUpdate；
// 工具结果不受同步影响（仍然成功）。
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
	createN, updateN := fakeBitableForAutopush(t)

	// 与 cli/mcp.go 相同的装配方式
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		t.Fatal(err)
	}
	AutopushFunc = func(st *store.Store, projectKey string) { feishu.BestEffort(st, cfg, projectKey) }
	t.Cleanup(func() { AutopushFunc = nil })

	sc := connect(t, s, "claude")
	created := decode(t, callTool(t, sc, "add_task",
		map[string]any{"project": "demo", "title": "t1"}))
	if n := createN(); n != 1 {
		t.Fatalf("add_task 应触发 1 次 RecordCreate, got %d", n)
	}

	decode(t, callTool(t, sc, "update_task",
		map[string]any{"id": created["ID"], "status": "done"}))
	if n := updateN(); n == 0 {
		t.Fatal("update_task 必须触发远端 RecordUpdate")
	}

	tasks, err := s.ListTasks(p.ID, store.TaskFilter{})
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
