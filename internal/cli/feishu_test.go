package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zhangyi/pulse/internal/config"
	"github.com/zhangyi/pulse/internal/model"
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

// newFakeFeishu 启动假飞书（token/建 base/建表/建视图/建文档/记录搜索与新建/文档追加块）
// 并把 PULSE_FEISHU_ENDPOINT 指过去，返回各类调用计数供断言；blockBodies 按调用序
// 回放每次文档追加块请求的原始 JSON 体。
func newFakeFeishu(t *testing.T) (appCreates, tableCreates, viewCreates, docCreates, blockAppends *atomic.Int32, blockBodies func() []string) {
	t.Helper()
	var appN, tableN, viewN, docN, blockN atomic.Int32
	var mu sync.Mutex
	var bodies []string
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
		switch {
		case strings.HasSuffix(r.URL.Path, "/records/search"):
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"items": []any{}}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/records"):
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
				"record": map[string]any{"record_id": "recC"},
			}})
		case strings.HasSuffix(r.URL.Path, "/views"):
			viewN.Add(1)
			respJSON(w, http.StatusOK, map[string]any{"code": 0})
		case strings.HasSuffix(r.URL.Path, "/tables"):
			n := tableN.Add(1)
			id := "tblTask"
			if n == 2 {
				id = "tblVer"
			}
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"table_id": id}})
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/open-apis/docx/v1/documents", func(w http.ResponseWriter, r *http.Request) {
		docN.Add(1)
		respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
			"document": map[string]any{"document_id": "docD"},
		}})
	})
	mux.HandleFunc("/open-apis/docx/v1/documents/", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/children") {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
		blockN.Add(1)
		respJSON(w, http.StatusOK, map[string]any{"code": 0})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("PULSE_FEISHU_ENDPOINT", srv.URL)
	return &appN, &tableN, &viewN, &docN, &blockN,
		func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), bodies...) }
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
	appN, tableN, viewN, docN, _, _ := newFakeFeishu(t)

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
	if n := tableN.Load(); n != 8 { // 任务表+版本表+v1.1 六实体表
		t.Fatalf("TableCreate 次数 = %d, want 8", n)
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
	appN, tableN, viewN, docN, _, _ := newFakeFeishu(t) // 服务在但不应被调用，计数 0 即证明采用模式零调用

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

// TestFeishuPublishEndToEnd：bind 后 `pulse feishu publish` 完整走通——先静默同步
// （记录搜索/新建打假飞书），再把报表块追加到绑定文档；输出文档 token 并落
// feishu_publish 活动；all = weekly + versions 两次追加；非法 report 名报中文错误。
func TestFeishuPublishEndToEnd(t *testing.T) {
	s, _ := feishuTestEnv(t)
	if _, err := s.CreateProject("demo", "演示", ""); err != nil {
		t.Fatal(err)
	}
	_, _, _, docN, blockN, bodies := newFakeFeishu(t)

	if _, _, err := runCLI(t, "feishu", "bind", "--project", "demo"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	tester, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateVersion(model.Version{ProjectID: p.ID, Name: "v1.0", Status: "planned"}, tester, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(model.Task{ProjectID: p.ID, Title: "写发布说明", Status: "todo"}, tester, nil); err != nil {
		t.Fatal(err)
	}

	// weekly：一次追加，块体含防混淆标题与落款
	out, errOut, err := runCLI(t, "feishu", "publish", "--project", "demo", "--report", "weekly")
	if err != nil {
		t.Fatalf("publish weekly: %v stderr=%s", err, errOut)
	}
	for _, want := range []string{"已沉淀", "docD", "weekly", "tester"} {
		if !strings.Contains(out, want) {
			t.Fatalf("stdout %q must contain %q", out, want)
		}
	}
	if n := docN.Load(); n != 1 {
		t.Fatalf("已绑文档时不得新建文档, DocCreate 次数 = %d", n)
	}
	if n := blockN.Load(); n != 1 {
		t.Fatalf("weekly 应追加 1 次块, got %d", n)
	}
	for _, want := range []string{"周报 2026-W", "由 tester 触发", "由 pulse 导出 · 触发人 tester"} {
		if bs := bodies(); len(bs) != 1 || !strings.Contains(bs[0], want) {
			t.Fatalf("追加块体 %v 必须含 %q", bs, want)
		}
	}
	assertActivity(t, s, p.ID, "feishu_publish", "tester")

	// all：weekly + versions 两次追加
	if _, errOut, err := runCLI(t, "feishu", "publish", "--project", "demo", "--report", "all"); err != nil {
		t.Fatalf("publish all: %v stderr=%s", err, errOut)
	}
	if n := blockN.Load(); n != 3 {
		t.Fatalf("all 后累计追加 = %d, want 3（weekly 单独 1 次 + all 的 2 次）", n)
	}

	// 非法 report 名：中文报错且不产生任何追加
	_, errOut, err = runCLI(t, "feishu", "publish", "--project", "demo", "--report", "daily")
	if err == nil || !strings.Contains(errOut, "weekly|versions|all") {
		t.Fatalf("非法 report 必须报错, err=%v stderr=%s", err, errOut)
	}
	if n := blockN.Load(); n != 3 {
		t.Fatalf("非法 report 不应追加块, got %d", n)
	}
}

// TestFeishuPublishAutoCreatesDoc：未绑定文档（采用模式省略 --doc）首次 publish
// 自动建档后，CLI 输出与 store 落库都必须是真实的新 token，而非空串。
func TestFeishuPublishAutoCreatesDoc(t *testing.T) {
	s, _ := feishuTestEnv(t)
	if _, err := s.CreateProject("demo", "演示", ""); err != nil {
		t.Fatal(err)
	}
	_, _, _, docN, blockN, _ := newFakeFeishu(t)
	if _, _, err := runCLI(t, "feishu", "bind", "--project", "demo"); err != nil {
		t.Fatalf("bind: %v", err)
	}
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatal(err)
	}
	p.FeishuDocToken = "" // 模拟采用模式未绑 doc
	if err := s.SaveProject(p); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runCLI(t, "feishu", "publish", "--project", "demo", "--report", "weekly")
	if err != nil {
		t.Fatalf("publish: %v stderr=%s", err, errOut)
	}
	if !strings.Contains(out, "docD") {
		t.Fatalf("stdout %q must contain 新文档 token docD", out)
	}
	saved, _, err := s.GetProjectByKey("demo")
	if err != nil {
		t.Fatal(err)
	}
	if saved.FeishuDocToken != "docD" {
		t.Fatalf("store doc token = %q, want docD", saved.FeishuDocToken)
	}
	if n := docN.Load(); n != 2 { // bind 1 次 + publish 自动补建 1 次
		t.Fatalf("DocCreate 次数 = %d, want 2", n)
	}
	if n := blockN.Load(); n != 1 {
		t.Fatalf("BlockAppend 次数 = %d, want 1", n)
	}
}

// assertActivity 断言窗口内存在指定 action 的活动且触发人可解析到成员名
// （publish 测试现场已有多条活动，不能用恰好一条的 requireActivity）。
func assertActivity(t *testing.T, s *store.Store, projectID int64, action, actorName string) {
	t.Helper()
	acts, err := s.ActivitiesInWindow(projectID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ms, err := s.ListMembers()
	if err != nil {
		t.Fatal(err)
	}
	names := map[int64]string{}
	for _, m := range ms {
		names[m.ID] = m.Name
	}
	for _, a := range acts {
		if a.Action == action && names[a.ActorID] == actorName {
			return
		}
	}
	t.Fatalf("未找到 action=%s actor=%s 的活动: %+v", action, actorName, acts)
}
