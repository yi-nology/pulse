package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortBackoff 把限流退避基准调成 1ms 并在测试结束后恢复，避免重试用例真实睡眠数秒。
func shortBackoff(t *testing.T) {
	t.Helper()
	old := backoffBase
	backoffBase = time.Millisecond
	t.Cleanup(func() { backoffBase = old })
}

// respJSON 以指定 HTTP 状态码写出一个 JSON 业务响应。
func respJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// tokenOK 返回一个固定 token 的 token 端点 handler。
func tokenOK(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		respJSON(w, http.StatusOK, map[string]any{
			"code": 0, "tenant_access_token": token, "expire": 7200,
		})
	}
}

// newFakeFeishu 启动一个带固定 token 端点的假飞书，返回 client（供各用例自行追加路由）。
func newFakeFeishu(t *testing.T, mux *http.ServeMux) *Client {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return NewClient("app-id", "app-secret", srv.URL)
}

// TestClientTokenCachedAndRefreshed：token 只取一次并缓存；缓存被改为过期后下一次请求自动刷新。
func TestClientTokenCachedAndRefreshed(t *testing.T) {
	shortBackoff(t)
	var tokenCalls, apiCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(tokenPath, func(w http.ResponseWriter, r *http.Request) {
		tokenCalls.Add(1)
		respJSON(w, http.StatusOK, map[string]any{"code": 0, "tenant_access_token": "t-fixed", "expire": 7200})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer t-fixed" {
			t.Errorf("Authorization = %q, want Bearer t-fixed", got)
		}
		apiCalls.Add(1)
		respJSON(w, http.StatusOK, map[string]any{
			"code": 0, "data": map[string]any{"app": map[string]any{"app_token": "app1"}},
		})
	})
	c := newFakeFeishu(t, mux)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := c.API().AppCreate(ctx, "项目"); err != nil {
			t.Fatalf("AppCreate #%d: %v", i, err)
		}
	}
	if n := tokenCalls.Load(); n != 1 {
		t.Fatalf("token 端点被调用 %d 次, want 1（后续请求应命中缓存）", n)
	}
	if n := apiCalls.Load(); n != 3 {
		t.Fatalf("业务端点被调用 %d 次, want 3", n)
	}

	// 把缓存改成"已过期"，下一次请求应触发刷新（过期前 5 分钟刷新的同一分支）。
	c.mu.Lock()
	c.token.validUntil = time.Now().Add(-time.Second)
	c.mu.Unlock()
	if _, err := c.API().AppCreate(ctx, "项目"); err != nil {
		t.Fatalf("缓存过期后 AppCreate: %v", err)
	}
	if n := tokenCalls.Load(); n != 2 {
		t.Fatalf("缓存过期后 token 端点被调用 %d 次, want 2", n)
	}
}

// TestDoWithRetryOnRateLimit：先 429 两次（第二次走 HTTP 200 + 业务码 99991400），第三次成功，共 3 次请求。
func TestDoWithRetryOnRateLimit(t *testing.T) {
	shortBackoff(t)
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(tokenPath, tokenOK("t-1"))
	mux.HandleFunc("/open-apis/bitable/v1/apps/app1/tables/tbl1/records", func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1: // HTTP 层限流
			respJSON(w, http.StatusTooManyRequests, map[string]any{"code": 99991400, "msg": "Too many requests"})
		case 2: // 业务层限流（HTTP 200 + code 99991400）
			respJSON(w, http.StatusOK, map[string]any{"code": 99991400, "msg": "Too many requests"})
		default:
			respJSON(w, http.StatusOK, map[string]any{
				"code": 0, "data": map[string]any{"record": map[string]any{"record_id": "rec1"}},
			})
		}
	})
	c := newFakeFeishu(t, mux)

	recID, err := c.API().RecordCreate(context.Background(), "app1", "tbl1", map[string]any{"任务": "x"})
	if err != nil {
		t.Fatalf("RecordCreate: %v", err)
	}
	if recID != "rec1" {
		t.Fatalf("recordID = %q, want rec1", recID)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("共发出 %d 次请求, want 3（两次限流 + 一次成功）", n)
	}
}

// TestNonRetryableErrorNotRetried：业务码非限流错误必须立即返回，不能重试。
func TestNonRetryableErrorNotRetried(t *testing.T) {
	shortBackoff(t)
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(tokenPath, tokenOK("t-1"))
	mux.HandleFunc("/open-apis/bitable/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respJSON(w, http.StatusOK, map[string]any{"code": 1254290, "msg": "无权限"})
	})
	c := newFakeFeishu(t, mux)

	_, err := c.API().AppCreate(context.Background(), "项目")
	if err == nil || !strings.Contains(err.Error(), "1254290") {
		t.Fatalf("err = %v, want 含业务码 1254290 的错误", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("共发出 %d 次请求, want 1（非限流错误不重试）", n)
	}
}

// TestRecordWritesSerializedPerBase：并发 RecordCreate/RecordUpdate 在同一 base 上必须严格串行——
// handler 记录"进入/离开"事件并停留一段时间放大并发窗口，断言事件严格交替、无交叠。
func TestRecordWritesSerializedPerBase(t *testing.T) {
	shortBackoff(t)
	type event struct {
		kind string // enter / leave
	}
	events := make(chan event, 128)
	mux := http.NewServeMux()
	mux.HandleFunc(tokenPath, tokenOK("t-1"))
	// stay 记录进入/离开事件，并停留片刻放大并发窗口。
	stay := func() {
		events <- event{kind: "enter"}
		time.Sleep(15 * time.Millisecond)
		events <- event{kind: "leave"}
	}
	mux.HandleFunc("/open-apis/bitable/v1/apps/b1/tables/t1/records", func(w http.ResponseWriter, r *http.Request) {
		stay()
		respJSON(w, http.StatusOK, map[string]any{
			"code": 0, "data": map[string]any{"record": map[string]any{"record_id": "r-new"}},
		})
	})
	mux.HandleFunc("/open-apis/bitable/v1/apps/b1/tables/t1/records/u1", func(w http.ResponseWriter, r *http.Request) {
		stay()
		respJSON(w, http.StatusOK, map[string]any{"code": 0})
	})
	c := newFakeFeishu(t, mux)

	// 预置 token 缓存，使请求直达写路径，事件序列完全确定。
	c.mu.Lock()
	c.token = cachedToken{value: "t-warm", validUntil: time.Now().Add(time.Hour)}
	c.mu.Unlock()

	ctx := context.Background()
	const concurrency = 4
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = c.API().RecordCreate(ctx, "b1", "t1", map[string]any{"任务": i})
			} else {
				err = c.API().RecordUpdate(ctx, "b1", "t1", "u1", map[string]any{"任务": i})
			}
			if err != nil {
				t.Errorf("并发写 #%d: %v", i, err)
			}
		}(i)
	}
	go func() {
		wg.Wait()
		close(events)
	}()

	var seq []event
	for ev := range events {
		seq = append(seq, ev)
	}
	if len(seq) != concurrency*2 {
		t.Fatalf("事件数 = %d, want %d: %+v", len(seq), concurrency*2, seq)
	}
	for i, ev := range seq {
		want := "enter"
		if i%2 == 1 {
			want = "leave"
		}
		if ev.kind != want {
			t.Fatalf("第 %d 个事件 = %s, want %s（写请求出现交叠，未串行）: %+v", i, ev.kind, want, seq)
		}
	}
}

// TestRecordSearchPaginatesAndNormalizesTime：搜索自动翻页聚合全部记录；
// last_modified_time 字符串与数字两种回传形态都归一化为 int64 秒。
func TestRecordSearchPaginatesAndNormalizesTime(t *testing.T) {
	shortBackoff(t)
	var searchCalls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc(tokenPath, tokenOK("t-1"))
	mux.HandleFunc("/open-apis/bitable/v1/apps/b1/tables/t1/records/search", func(w http.ResponseWriter, r *http.Request) {
		switch searchCalls.Add(1) {
		case 1:
			if r.URL.Query().Get("page_token") != "" {
				t.Errorf("首次请求不应携带 page_token")
			}
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
				"items": []any{map[string]any{
					"record_id": "r1", "fields": map[string]any{"任务": "a"},
					"last_modified_time": "1700000000", // 字符串形态
				}},
				"has_more": true, "page_token": "p2",
			}})
		case 2:
			if got := r.URL.Query().Get("page_token"); got != "p2" {
				t.Errorf("page_token = %q, want p2", got)
			}
			respJSON(w, http.StatusOK, map[string]any{"code": 0, "data": map[string]any{
				"items": []any{
					map[string]any{"record_id": "r2", "fields": map[string]any{}, "last_modified_time": 1700000001}, // 数字形态
					map[string]any{"record_id": "r3", "fields": map[string]any{}},                                   // 缺省
				},
				"has_more": false,
			}})
		}
	})
	c := newFakeFeishu(t, mux)

	records, err := c.API().RecordSearch(context.Background(), "b1", "t1")
	if err != nil {
		t.Fatalf("RecordSearch: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("记录数 = %d, want 3（两页聚合）: %+v", len(records), records)
	}
	if records[0].RecordID != "r1" || records[0].Fields["任务"] != "a" {
		t.Fatalf("records[0] = %+v, want r1/任务=a", records[0])
	}
	wantTimes := map[string]int64{"r1": 1700000000, "r2": 1700000001, "r3": 0}
	for _, rec := range records {
		if rec.LastModifiedTime != wantTimes[rec.RecordID] {
			t.Fatalf("%s.LastModifiedTime = %d, want %d", rec.RecordID, rec.LastModifiedTime, wantTimes[rec.RecordID])
		}
	}
}

// TestNewClientEndpoint：endpoint 为空时取官方域名；末尾斜杠被去掉。
func TestNewClientEndpoint(t *testing.T) {
	if c := NewClient("a", "b", ""); c.endpoint != "https://open.feishu.cn" {
		t.Fatalf("默认 endpoint = %q, want https://open.feishu.cn", c.endpoint)
	}
	if c := NewClient("a", "b", "http://127.0.0.1:1234/"); c.endpoint != "http://127.0.0.1:1234" {
		t.Fatalf("endpoint = %q, want 去除尾部斜杠", c.endpoint)
	}
}
