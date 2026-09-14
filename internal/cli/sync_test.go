package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/zhangyi/pulse/internal/model"
	"github.com/zhangyi/pulse/internal/store"
)

// syncFakeFeishu 是 sync/autopush/stale 检查共用的最小假飞书：
// token + 记录搜索/新建/更新端点，搜索结果可脚本化，调用计数可断言。
type syncFakeFeishu struct {
	mu      sync.Mutex
	items   []map[string]any // RecordSearch 返回的记录（脚本化）
	searchN int
	createN int
	created []map[string]any // RecordCreate 收到的 fields（按调用序）
}

func (f *syncFakeFeishu) setItems(items ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items = items
}

func (f *syncFakeFeishu) createCalls() (n int, fields []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.createN, f.created
}

func (f *syncFakeFeishu) searchCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searchN
}

// newSyncFakeFeishu 启动假飞书并把 PULSE_FEISHU_ENDPOINT 指过去。
func newSyncFakeFeishu(t *testing.T) *syncFakeFeishu {
	t.Helper()
	f := &syncFakeFeishu{}
	mux := http.NewServeMux()
	mux.HandleFunc("/open-apis/auth/v3/tenant_access_token/internal", func(w http.ResponseWriter, r *http.Request) {
		respJSON(w, http.StatusOK, map[string]any{"code": 0, "tenant_access_token": "t-1", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/records/search"):
			f.mu.Lock()
			f.searchN++
			items := f.items
			f.mu.Unlock()
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{"items": items}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/records"):
			var body struct {
				Fields map[string]any `json:"fields"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.mu.Lock()
			f.createN++
			n := f.createN
			f.created = append(f.created, body.Fields)
			f.mu.Unlock()
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
				"record": map[string]any{"record_id": fmt.Sprintf("recC%d", n)},
			}})
		default: // 更新与其他端点一律成功
			respJSON(w, http.StatusOK, map[string]any{"code": 0})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("PULSE_FEISHU_ENDPOINT", srv.URL)
	return f
}

// deadEndpoint 返回一个已关闭服务的 URL（连接即拒绝，模拟离线）。
func deadEndpoint(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NewServeMux())
	srv.Close()
	return srv.URL
}

// setupBoundProject = feishuTestEnv + demo 项目 + 采用模式 bind（token 落库）。
func setupBoundProject(t *testing.T) *store.Store {
	t.Helper()
	s, _ := feishuTestEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatalf("init: %v", err)
	}
	out, errOut, err := runCLI(t, "feishu", "bind", "--project", "demo",
		"--app-token", "appX", "--task-table", "t1", "--version-table", "t2")
	if err != nil {
		t.Fatalf("bind: %v stderr=%s out=%s", err, errOut, out)
	}
	return s
}

// mustProject 取回 demo 项目。
func mustProject(t *testing.T, s *store.Store) model.Project {
	t.Helper()
	p, found, err := s.GetProjectByKey("demo")
	if err != nil || !found {
		t.Fatalf("project demo: found=%v err=%v", found, err)
	}
	return p
}

// TestSyncCmdRequiresFeishuConfig：未配置飞书时给中文引导错误。
func TestSyncCmdRequiresFeishuConfig(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "sync", "--project", "demo")
	if err == nil {
		t.Fatal("未配置飞书时 sync 必须失败")
	}
	if !strings.Contains(errOut, "未配置飞书") {
		t.Fatalf("stderr %q must contain %q", errOut, "未配置飞书")
	}
}

// TestSyncCmdRequiresBinding：已配置飞书但项目未绑定时给引导错误。
func TestSyncCmdRequiresBinding(t *testing.T) {
	feishuTestEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	_, errOut, err := runCLI(t, "sync", "--project", "demo")
	if err == nil || !strings.Contains(errOut, "未绑定飞书") {
		t.Fatalf("未绑定时必须引导 bind, err=%v stderr=%s", err, errOut)
	}
}

// TestSyncCmdPushThenEcho：完整链路——本地任务 → pulse sync 推送（record_id 回填）→
// 再 sync 搜索回读同内容 → 自回声跳过；输出行格式符合约定。
func TestSyncCmdPushThenEcho(t *testing.T) {
	s := setupBoundProject(t)
	fake := newSyncFakeFeishu(t)
	p := mustProject(t, s)
	m, err := s.GetOrCreateMember("tester", "human")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateTask(model.Task{
		ProjectID: p.ID, Title: "写周报", Status: "todo", Priority: 3,
		EstimateDays: 2, AssigneeID: m.ID,
	}, m, nil); err != nil {
		t.Fatal(err)
	}

	out, errOut, err := runCLI(t, "sync", "--project", "demo")
	if err != nil {
		t.Fatalf("首次 sync: %v stderr=%s", err, errOut)
	}
	if want := "已推送 1、拉取 0、跳过回声 0、废弃 0、冲突 0"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
	if n, _ := fake.createCalls(); n != 1 {
		t.Fatalf("RecordCreate 次数 = %d, want 1", n)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{})
	if len(tasks) != 1 || tasks[0].BitableRecordID != "recC1" || len(tasks[0].BitableSyncedHash) != 16 {
		t.Fatalf("record_id/hash 未回填: %+v", tasks)
	}

	// 远端回读刚推送的内容 → 自回声跳过
	_, created := fake.createCalls()
	fake.setItems(map[string]any{
		"record_id": "recC1", "last_modified_time": 1000, "fields": created[0],
	})
	out, _, err = runCLI(t, "sync", "--project", "demo")
	if err != nil {
		t.Fatalf("二次 sync: %v", err)
	}
	if want := "已推送 0、拉取 0、跳过回声 1、废弃 0、冲突 0"; !strings.Contains(out, want) {
		t.Fatalf("stdout %q must contain %q", out, want)
	}
}

// TestAutopushAfterTaskAdd：task add 成功后自动 push（静默，无输出），本地回填 record_id；
// 未绑定的项目写命令不触发任何调用。
func TestAutopushAfterTaskAdd(t *testing.T) {
	s := setupBoundProject(t)
	fake := newSyncFakeFeishu(t)
	p := mustProject(t, s)

	out, errOut, err := runCLI(t, "task", "add", "自动推送", "--project", "demo")
	if err != nil {
		t.Fatalf("task add: %v stderr=%s", err, errOut)
	}
	if strings.Contains(out+errOut, "同步完成") {
		t.Fatalf("autopush 应静默: out=%q err=%q", out, errOut)
	}
	if n, _ := fake.createCalls(); n != 1 {
		t.Fatalf("autopush 应产生 1 次 RecordCreate, got %d", n)
	}
	tasks, _ := s.ListTasks(p.ID, store.TaskFilter{})
	if len(tasks) != 1 || tasks[0].BitableRecordID != "recC1" {
		t.Fatalf("record_id 未回填: %+v", tasks)
	}

	// 未绑定项目：task add 静默跳过，零新调用
	if _, _, err := runCLI(t, "init", "solo"); err != nil {
		t.Fatal(err)
	}
	before := fake.searchCalls() + func() int { n, _ := fake.createCalls(); return n }()
	if _, _, err := runCLI(t, "task", "add", "本地任务", "--project", "solo"); err != nil {
		t.Fatal(err)
	}
	after := fake.searchCalls() + func() int { n, _ := fake.createCalls(); return n }()
	if after != before {
		t.Fatalf("未绑定项目不应触发 autopush: %d → %d", before, after)
	}
}

// TestAutopushFailureKeepsExitCode：飞书不可达时 task add 仍成功（退出码不受影响），仅 stderr 警告。
func TestAutopushFailureKeepsExitCode(t *testing.T) {
	setupBoundProject(t)
	t.Setenv("PULSE_FEISHU_ENDPOINT", deadEndpoint(t))

	out, errOut, err := runCLI(t, "task", "add", "离线任务", "--project", "demo")
	if err != nil {
		t.Fatalf("飞书不可达时写命令必须照常成功: %v stdout=%s", err, out)
	}
	if !strings.Contains(errOut, "自动同步飞书失败") {
		t.Fatalf("stderr 应有警告: %q", errOut)
	}
	if !strings.Contains(out, "任务已创建") {
		t.Fatalf("本地写操作应照常输出: %q", out)
	}
}

// TestReportStaleCheckTriggersSilentPull：报表前 last_pull 缺失 → 静默同步一次；
// stale 窗口内再跑报表不再同步。
func TestReportStaleCheckTriggersSilentPull(t *testing.T) {
	setupBoundProject(t)
	fake := newSyncFakeFeishu(t)

	_, errOut, err := runCLI(t, "report", "workload", "--project", "demo")
	if err != nil {
		t.Fatalf("report: %v stderr=%s", err, errOut)
	}
	if fake.searchCalls() == 0 {
		t.Fatal("首次报表应触发静默 pull（搜索版本/任务表）")
	}
	before := fake.searchCalls()
	if _, _, err := runCLI(t, "report", "workload", "--project", "demo"); err != nil {
		t.Fatal(err)
	}
	if after := fake.searchCalls(); after != before {
		t.Fatalf("stale 窗口内不应再同步: %d → %d", before, after)
	}
}

// TestReportOfflineShowsStaleHint：离线时报表照常生成，仅提示数据可能滞后。
func TestReportOfflineShowsStaleHint(t *testing.T) {
	setupBoundProject(t)
	t.Setenv("PULSE_FEISHU_ENDPOINT", deadEndpoint(t))

	out, errOut, err := runCLI(t, "report", "workload", "--project", "demo")
	if err != nil {
		t.Fatalf("离线时报表必须照常成功: %v", err)
	}
	if !strings.Contains(errOut, "数据可能滞后") {
		t.Fatalf("stderr 应提示数据可能滞后: %q", errOut)
	}
	if len(out) == 0 {
		t.Fatal("报表应有输出")
	}
}

// TestReportUnconfiguredFeishuNoPull：未配置飞书时报表零同步零提示（纯本地路径）。
func TestReportUnconfiguredFeishuNoPull(t *testing.T) {
	_, _, _ = testEnv(t)
	if _, _, err := runCLI(t, "init", "demo"); err != nil {
		t.Fatal(err)
	}
	out, errOut, err := runCLI(t, "report", "workload", "--project", "demo")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if errOut != "" {
		t.Fatalf("未配置飞书不应有任何同步提示: %q", errOut)
	}
	if len(out) == 0 {
		t.Fatal("报表应有输出")
	}
}
