package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// —— 可注入参数与常量 ——————————————————————————————————————

// backoffBase 是限流退避的基准间隔：第 n 次重试前等待 backoffBase*2^(n-1)。
// 故意做成包级变量，便于测试注入极小值（见 client_test.go 的 shortBackoff）；生产代码不要修改它。
var backoffBase = 500 * time.Millisecond

const (
	// maxRetries 是限流（HTTP 429 或业务码 99991400）时的最大重试次数，即单个请求最多发 1+maxRetries 次。
	maxRetries = 5
	// rateLimitCode 是飞书业务侧的限流错误码。
	rateLimitCode = 99991400
	// tokenRefreshMargin 让令牌在过期前 5 分钟即视为失效并刷新，避免边界上用到临期 token。
	tokenRefreshMargin = 5 * time.Minute
	// defaultEndpoint 是飞书开放平台默认域名（NewClient 的 endpoint 为空时使用）。
	defaultEndpoint = "https://open.feishu.cn"
	// tokenPath 是自建应用获取 tenant_access_token 的端点。
	tokenPath = "/open-apis/auth/v3/tenant_access_token/internal"
)

// —— Client ————————————————————————————————————————————————

// Client 是飞书客户端基座：负责 tenant_access_token 的获取/缓存/过期前刷新、
// 限流指数退避重试，以及按 base（appToken）串行化写请求（spec 的"每 base 串行写"）。
// 业务能力全部通过 API() 暴露的 bitableAPI 接口提供；本包不依赖 internal/store。
type Client struct {
	appID     string
	appSecret string
	endpoint  string // 形如 https://open.feishu.cn，末尾无斜杠
	http      *http.Client

	mu      sync.Mutex             // 保护 token 与 flights
	token   cachedToken            // 当前缓存的租户令牌
	flights map[string]*baseFlight // 每 appToken 一把串行写锁（引用计数管理生命周期）

	tokenMu sync.Mutex // 串行化令牌刷新，避免并发冷启动请求同时打 token 端点

	api bitableAPI
}

// cachedToken 是缓存的租户令牌；validUntil 已扣除过期前 5 分钟的提前量。
type cachedToken struct {
	value      string
	validUntil time.Time
}

// baseFlight 是某个 base（appToken）的串行写锁；refs 记录登记/等待的协程数，
// 归零即把条目从 map 摘除，防止长期运行下锁表无界增长。
type baseFlight struct {
	mu   sync.Mutex
	refs int
}

// NewClient 创建客户端；endpoint 为空时使用官方域名 https://open.feishu.cn，
// 测试可注入 httptest 地址。appID/appSecret 为飞书自建应用凭据。
func NewClient(appID, appSecret, endpoint string) *Client {
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	c := &Client{
		appID:     appID,
		appSecret: appSecret,
		endpoint:  endpoint,
		http:      &http.Client{Timeout: 30 * time.Second},
		flights:   map[string]*baseFlight{},
	}
	c.api = remoteAPI{c: c}
	return c
}

// API 返回收口全部飞书 HTTP 交互的接口实例。
func (c *Client) API() bitableAPI { return c.api }

// —— tenant_access_token ———————————————————————————————————

// getToken 返回可用的 tenant_access_token：优先命中缓存；过期（含过期前 5 分钟）时刷新，线程安全。
func (c *Client) getToken(ctx context.Context) (string, error) {
	if tok, ok := c.cachedToken(); ok {
		return tok, nil
	}
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if tok, ok := c.cachedToken(); ok { // 双重检查：等锁期间可能已被其他协程刷新
		return tok, nil
	}
	payload, err := json.Marshal(map[string]string{"app_id": c.appID, "app_secret": c.appSecret})
	if err != nil {
		return "", err
	}
	body, err := c.doWithRetry(ctx, http.MethodPost, c.endpoint+tokenPath, payload, nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"` // 有效期，秒
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("解析 token 响应失败: %w", err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("获取 tenant_access_token 失败: code=%d msg=%s", resp.Code, resp.Msg)
	}
	ttl := time.Duration(resp.Expire) * time.Second
	validUntil := time.Now().Add(ttl - tokenRefreshMargin)
	if !validUntil.After(time.Now()) { // expire 小于提前量时兜底取半程，避免每个请求都刷 token
		validUntil = time.Now().Add(ttl / 2)
	}
	c.mu.Lock()
	c.token = cachedToken{value: resp.TenantAccessToken, validUntil: validUntil}
	c.mu.Unlock()
	return resp.TenantAccessToken, nil
}

// cachedToken 在锁内读取缓存的令牌，未过期则返回。
func (c *Client) cachedToken() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token.value != "" && time.Now().Before(c.token.validUntil) {
		return c.token.value, true
	}
	return "", false
}

// —— 每 base 串行写（singleFlight）—————————————————————————

// lockBase 登记并获得该 appToken 的串行写锁。返回的解锁函数先放锁、再递减引用计数，
// 计数归零时把条目从 map 摘除——先放锁保证等待者（已登记 refs）不会被"换新锁"绕过串行语义。
func (c *Client) lockBase(appToken string) func() {
	c.mu.Lock()
	f := c.flights[appToken]
	if f == nil {
		f = &baseFlight{}
		c.flights[appToken] = f
	}
	f.refs++
	c.mu.Unlock()

	f.mu.Lock()
	return func() {
		f.mu.Unlock() // 必须先放锁再做摘除判断，否则新等待者可能拿到另一把新锁
		c.mu.Lock()
		f.refs--
		if f.refs == 0 {
			delete(c.flights, appToken)
		}
		c.mu.Unlock()
	}
}

// —— 请求执行与限流退避 ——————————————————————————————————————

// apiResp 是飞书业务响应信封：code=0 表示成功，data 携带业务数据。
type apiResp struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// callAPI 是全部业务请求的统一入口：取 token → doWithRetry → 校验业务码 → 把 data 解析到 out（可为 nil）。
func (c *Client) callAPI(ctx context.Context, method, path string, payload, out any) error {
	tok, err := c.getToken(ctx)
	if err != nil {
		return err
	}
	var body []byte
	if payload != nil {
		if body, err = json.Marshal(payload); err != nil {
			return fmt.Errorf("序列化请求失败: %w", err)
		}
	}
	header := http.Header{"Authorization": []string{"Bearer " + tok}}
	raw, err := c.doWithRetry(ctx, method, c.endpoint+path, body, header)
	if err != nil {
		return err
	}
	var env apiResp
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("解析响应失败（%s %s）: %w", method, path, err)
	}
	if env.Code != 0 {
		return fmt.Errorf("飞书 API %s %s 失败: code=%d msg=%s", method, path, env.Code, env.Msg)
	}
	if out != nil && len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("解析响应 data 失败（%s %s）: %w", method, path, err)
		}
	}
	return nil
}

// doWithRetry 执行一次 HTTP 请求；遇 HTTP 429 或响应体业务码 99991400 时，
// 按 backoffBase*2^n 指数退避重试（最多 maxRetries 次）；其余错误立即返回。
// 返回最终一次响应的原始体，由调用方解析。
func (c *Client) doWithRetry(ctx context.Context, method, rawURL string, payload []byte, header http.Header) ([]byte, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoffBase<<(attempt-1)); err != nil {
				return nil, err
			}
		}
		body, retryable, err := c.once(ctx, method, rawURL, payload, header)
		if err == nil {
			return body, nil
		}
		if !retryable {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("限流退避重试 %d 次后仍失败: %w", maxRetries, lastErr)
}

// once 执行单次 HTTP 请求；retryable 标记该错误是否为限流类（值得退避重试）。
// 网络层错误不重试，直接交还调用方。
func (c *Client) once(ctx context.Context, method, rawURL string, payload []byte, header http.Header) (body []byte, retryable bool, err error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, false, err
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return body, true, fmt.Errorf("HTTP 429 Too Many Requests")
	}
	var env apiResp
	if json.Unmarshal(body, &env) == nil && env.Code == rateLimitCode {
		return body, true, fmt.Errorf("业务限流 code=%d", env.Code)
	}
	return body, false, nil
}

// sleepCtx 睡眠 wait，期间可被 ctx 取消。
func sleepCtx(ctx context.Context, wait time.Duration) error {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
