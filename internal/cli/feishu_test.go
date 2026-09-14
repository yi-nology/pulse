package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/store"
)

// respJSON 以指定 HTTP 状态码写出一个 JSON 业务响应（与 feishu 包测试的同名助手一致）。
func respJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// feishuTestEnv 在 testEnv 基础上补飞书凭据（app_id/app_secret 写入配置文件）。
func feishuTestEnv(t *testing.T) (*store.Store, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("PULSE_HOME", dir)
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := "default_actor: tester\nfeishu:\n  app_id: cli_a1\n  app_secret: sec1\n"
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o600); err != nil {
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
	return s, cfg
}

// newFakeFeishu 启动假飞书（token/建 base/建表/建视图/建文档）并把 PULSE_FEISHU_ENDPOINT
// 指过去，返回各类调用计数供断言。
func newFakeFeishu(t *testing.T) (appCreates, tableCreates, viewCreates, docCreates *atomic.Int32) {
	t.Helper()
	var appN, tableN, viewN, docN atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		respJSON(w, http.StatusOK, map[string]any{"code": 0, "tenant_access_token": "t-1", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		appN.Add(1)
		respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
			"app": map[string]any{"app_token": "appB"},
		}})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/views") {
			viewN.Add(1)
			respJSON(w, http.StatusOK, map[string]any{"code": 0})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/tables") {
			n := tableN.Add(1)
			id := "tblTask"
			if n == 2 {
				id = "tblVer"
			}
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"table_id": id}})
			return
		}
		http.NotFound(w, r)
	})
	mux.HandleFunc("/open-apis/docx/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		docN.Add(1)
		respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
			"document": map[string]any{"document_id": "docD"},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("PULSE_FEISHU_ENDPOINT", srv.URL)
	return &appN, &tableN, &viewN, &docN
}

// TestFeishuBindRequiresConfig：未配置飞书凭据时给中文引导错误，不产生任何写库。
func TestFeishuBindRequiresConfig(t *testing.T) {
	s, _, _ := testEnv(t) // 只有 default_actor，无 feishu 段
	if _, err := s.CreateProject("demo", "演示", ""); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "feishu", "bind", "--project", "demo")
	if err == nil {
		t.Fatal("未配置飞书时 bind 必须失败")
	}
	for _, want := range []string{"未配置飞书", "feishu.app_id", "PULSE_FEISHU_APP_SECRET"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("stderr %q must contain %q", errOut, want)
		}
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	if p.FeishuBitableAppToken != "" {
		t.Fatalf("失败路径不应写 token: %+v", p)
	}
}

// TestFeishuBindCreatesBaseEndToEnd：走假飞书完整走通创建模式——输出 token、
// SaveProject 落库、feishu_bind 活动落库；二次 bind 幂等跳过、零新调用零新活动。
func TestFeishuBindCreatesBaseEndToEnd(t *testing.T) {
	s, _ := feishuTestEnv(t)
	if _, err := s.CreateProject("demo", "演示", ""); err != nil {
		t.Fatal(err)
	}
	appN, tableN, viewN, docN := newFakeFeishu(t)

	out, errOut, err := runCLI(t, "feishu", "bind", "--project", "demo")
	if err != nil {
		t.Fatalf("bind failed: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"已绑定飞书", "appB", "tblTask", "tblVer", "docD", "--app-token appB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
	if n := appN.Load(); n != 1 {
		t.Fatalf("AppCreate 次数 = %d, want 1", n)
	}
	if n := tableN.Load(); n != 2 {
		t.Fatalf("TableCreate 次数 = %d, want 2", n)
	}
	if n := viewN.Load(); n != 1 {
		t.Fatalf("ViewCreate 次数 = %d, want 1", n)
	}
	if n := docN.Load(); n != 1 {
		t.Fatalf("DocCreate 次数 = %d, want 1", n)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	if p.FeishuBitableAppToken != "appB" || p.FeishuTaskTableID != "tblTask" ||
		p.FeishuVersionTableID != "tblVer" || p.FeishuDocToken != "docD" {
		t.Fatalf("token 未完整写回: %+v", p)
	}
	requireActivity(t, s, p.ID, "feishu_bind", "project", "tester")

	// 二次 bind：幂等跳过，不产生任何新调用与新活动
	before := [4]int32{appN.Load(), tableN.Load(), viewN.Load(), docN.Load()}
	out2, _, err := runCLI(t, "feishu", "bind", "--project", "demo")
	if err != nil {
		t.Fatalf("二次 bind: %v", err)
	}
	if !strings.Contains(out2, "已绑定飞书") || !strings.Contains(out2, "幂等跳过") {
		t.Fatalf("二次 bind 应幂等跳过: %q", out2)
	}
	after := [4]int32{appN.Load(), tableN.Load(), viewN.Load(), docN.Load()}
	if after != before {
		t.Fatalf("二次 bind 产生了新调用: before=%v after=%v", before, after)
	}
	requireActivity(t, s, p.ID, "feishu_bind", "project", "tester") // 仍只有一条
}

// TestFeishuBindAdoptExistingBase：--app-token 进入采用模式——零 API 调用（假飞书计数为 0），
// token 全部落库并落活动；缺 --task-table 报中文错误且不写库。
func TestFeishuBindAdoptExistingBase(t *testing.T) {
	s, _ := feishuTestEnv(t)
	if _, err := s.CreateProject("demo", "演示", ""); err != nil {
		t.Fatal(err)
	}
	appN, tableN, viewN, docN := newFakeFeishu(t) // 服务在但不应被调用，计数 0 即证明采用模式零调用

	// 缺 --task-table：报错且不写库
	_, errOut, err := runCLI(t, "feishu", "bind", "--project", "demo", "--app-token", "appX")
	if err == nil || !strings.Contains(errOut, "--task-table") {
		t.Fatalf("缺 --task-table 必须报错, err=%v stderr=%s", err, errOut)
	}
	if p, _, _ := s.GetProjectByKey("demo"); p.FeishuBitableAppToken != "" {
		t.Fatalf("失败路径不应写 token: %+v", p)
	}

	out, errOut, err := runCLI(t, "feishu", "bind", "--project", "demo",
		"--app-token", "appX", "--task-table", "t1", "--version-table", "t2", "--doc", "d1")
	if err != nil {
		t.Fatalf("adopt bind failed: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"已绑定既有飞书 base", "appX", "t1", "t2", "d1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
	if appN.Load()+tableN.Load()+viewN.Load()+docN.Load() != 0 {
		t.Fatal("采用模式不允许产生任何飞书调用")
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	if p.FeishuBitableAppToken != "appX" || p.FeishuTaskTableID != "t1" ||
		p.FeishuVersionTableID != "t2" || p.FeishuDocToken != "d1" {
		t.Fatalf("采用 token 未写回: %+v", p)
	}
	requireActivity(t, s, p.ID, "feishu_bind", "project", "tester")
}
