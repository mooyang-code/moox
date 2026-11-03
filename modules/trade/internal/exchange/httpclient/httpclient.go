// Package httpclient 提供交易所适配层共用的 HTTP 工具：带超时的请求执行、限流记录和拦截。
package httpclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// DefaultTimeout 默认请求超时。
const DefaultTimeout = 15 * time.Second

const (
	// defaultHostPause 是 429 没有 Retry-After 时整个域名的暂停时长。
	defaultHostPause = 10 * time.Second
	// defaultBanPause 是 418 没有 Retry-After 时的暂停时长，币安最短封禁 2 分钟。
	defaultBanPause = 2 * time.Minute
	// defaultOrderPause 是下单限流没有 Retry-After 时该账户的下单暂停时长。
	defaultOrderPause = 10 * time.Second
	// throttlePause 是系统限流后该账户下单的短暂退避。
	throttlePause = 2 * time.Second
	// wafPause 是 WAF 403 后整个域名的暂停时长。
	wafPause = 30 * time.Second
)

// sharedTransport 被所有客户端复用：同一域名的并发下单需要更多空闲连接。
var (
	stockTransport  = http.DefaultTransport
	sharedTransport = func() *http.Transport {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 256
		transport.MaxIdleConnsPerHost = 32
		return transport
	}()
)

// transport 返回连接池调大的共享传输层；测试替换了 http.DefaultTransport 时沿用替换后的。
func transport() http.RoundTripper {
	if http.DefaultTransport != stockTransport {
		return http.DefaultTransport
	}
	return sharedTransport
}

// 观察回调由进程启动时设置，用于指标和告警；为空时不回调。
var (
	// OnPause 在域名或账户下单被限流暂停时调用。
	OnPause func(host string, reason exchange.ErrorKind, until time.Time)
	// OnWeight 在记录到新的权重读数时调用。
	OnWeight func(host string, used, limit int)
)

// Classifier 让适配器按响应体里的错误码细分限流，返回空表示按 HTTP 状态处理。
type Classifier func(status int, raw []byte) exchange.ErrorKind

// Client 封装一个可复用的 *http.Client。一个适配器一份，下单暂停按 Client 计，域名暂停和权重按进程计。
type Client struct {
	HTTP     *http.Client
	BaseURL  string
	Classify Classifier

	orders pauseState
}

// New 创建客户端。
func New(baseURL string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: DefaultTimeout, Transport: transport()},
		BaseURL: strings.TrimRight(baseURL, "/"),
	}
}

// Request 描述一次 REST 调用。
type Request struct {
	Method string
	Path   string
	Query  url.Values
	// RawQuery 是已编码好的查询串，非空时原样发送、忽略 Query（签名要求字节一致、签名在末尾）。
	RawQuery string
	Body     []byte
	Headers  map[string]string
	Timeout  time.Duration
	// Order 标记新下单请求：账户下单暂停时只拦这类请求，查询和撤单照常。
	Order bool
}

// Status 是客户端当前的限流状态。
type Status struct {
	UsedWeight        int
	WeightLimit       int
	PausedUntil       time.Time
	PauseReason       exchange.ErrorKind
	OrdersPausedUntil time.Time
}

// UsageRatio 是本分钟已用权重占额度的比例；没有额度配置时为 0。
func (s Status) UsageRatio() float64 {
	if s.WeightLimit <= 0 {
		return 0
	}
	return float64(s.UsedWeight) / float64(s.WeightLimit)
}

// Status 返回客户端所在域名和本账户的限流状态。
func (c *Client) Status() Status {
	if c == nil {
		return Status{}
	}
	now := time.Now()
	host := hostOf(c.BaseURL)
	used, limit, until, reason := host.snapshot(now)
	ordersUntil, _ := c.orders.until(now)
	return Status{UsedWeight: used, WeightLimit: limit, PausedUntil: until, PauseReason: reason, OrdersPausedUntil: ordersUntil}
}

// SetWeightLimit 设置域名每分钟的权重额度，用于报告用量比例。
func (c *Client) SetWeightLimit(limit int) {
	if c == nil {
		return
	}
	hostOf(c.BaseURL).setLimit(limit)
}

// PauseOrders 暂停本账户下单，用于响应体里才能识别的下单限流（例如 OKX 50011）。
func (c *Client) PauseOrders(d time.Duration, reason exchange.ErrorKind) {
	if c == nil {
		return
	}
	until := time.Now().Add(d)
	c.orders.extend(until, reason)
	if OnPause != nil {
		OnPause(hostKey(c.BaseURL), reason, until)
	}
}

// Do 执行请求，返回响应体。非 2xx 视为错误并返回交易所原始 body 便于诊断。
// 限流暂停期间直接返回 ErrorNotSent，不睡眠：调用方常持锁且请求已签名。
func (c *Client) Do(ctx context.Context, req *Request) ([]byte, error) {
	if c == nil || c.HTTP == nil || req == nil {
		return nil, &exchange.Error{Kind: exchange.ErrorNotReady, Err: errors.New("http client is not configured")}
	}
	host := hostOf(c.BaseURL)
	now := time.Now()
	if until, reason := host.pausedUntil(now); !until.IsZero() {
		return nil, notSent(reason, until)
	}
	if req.Order {
		if until, reason := c.orders.until(now); !until.IsZero() {
			return nil, notSent(reason, until)
		}
	}
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	full := c.BaseURL + req.Path
	if req.RawQuery != "" {
		full += "?" + req.RawQuery
	} else if len(req.Query) > 0 {
		full += "?" + req.Query.Encode()
	}
	var bodyReader io.Reader
	if len(req.Body) > 0 {
		bodyReader = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, full, bodyReader)
	if err != nil {
		return nil, &exchange.Error{Kind: exchange.ErrorRejected, Err: err}
	}
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}
	if len(req.Body) > 0 && httpReq.Header.Get("Content-Type") == "" {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	httpReq = httpReq.WithContext(cctx)

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return nil, &exchange.Error{Kind: exchange.ErrorTransportUnknown, Err: err}
	}
	defer resp.Body.Close()
	if host.recordWeight(resp.Header, time.Now()) && OnWeight != nil {
		used, limit, _, _ := host.snapshot(time.Now())
		OnWeight(hostKey(c.BaseURL), used, limit)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, &exchange.Error{Kind: exchange.ErrorTransportUnknown, HTTPStatus: resp.StatusCode, Err: err}
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	kind := c.classify(resp.StatusCode, raw)
	if until := c.applyPause(host, kind, resp.StatusCode, resp.Header, time.Now()); !until.IsZero() && OnPause != nil {
		OnPause(hostKey(c.BaseURL), kind, until)
	}
	return raw, &exchange.Error{
		Kind:       kind,
		HTTPStatus: resp.StatusCode,
		Err:        fmt.Errorf("http %d: %s", resp.StatusCode, truncate(raw, 512)),
	}
}

func (c *Client) classify(status int, raw []byte) exchange.ErrorKind {
	if c.Classify != nil {
		if kind := c.Classify(status, raw); kind != "" {
			return kind
		}
	}
	switch status {
	case http.StatusUnauthorized:
		return exchange.ErrorAuthentication
	case http.StatusForbidden:
		// 币安的 403 是 WAF 拦截，不是鉴权失败。
		return exchange.ErrorRateLimited
	case http.StatusNotFound:
		return exchange.ErrorOrderNotFound
	case http.StatusRequestTimeout:
		// 408 是交易所等待后端超时，订单可能已受理。
		return exchange.ErrorTransportUnknown
	case http.StatusTeapot:
		return exchange.ErrorBanned
	case http.StatusTooManyRequests:
		return exchange.ErrorRateLimited
	}
	if status >= 500 {
		return exchange.ErrorTransportUnknown
	}
	return exchange.ErrorRejected
}

// applyPause 按限流类型暂停域名或本账户下单，返回暂停截止时间；不是限流时返回零值。
func (c *Client) applyPause(host *hostState, kind exchange.ErrorKind, status int, header http.Header, now time.Time) time.Time {
	retry := retryAfter(header)
	var until time.Time
	switch kind {
	case exchange.ErrorBanned:
		until = now.Add(orDefault(retry, defaultBanPause))
		host.pause(until, kind)
	case exchange.ErrorRateLimited:
		pause := defaultHostPause
		if status == http.StatusForbidden {
			pause = wafPause
		}
		until = now.Add(orDefault(retry, pause))
		host.pause(until, kind)
	case exchange.ErrorOrderRateLimited:
		until = now.Add(orDefault(retry, defaultOrderPause))
		c.orders.extend(until, kind)
	case exchange.ErrorThrottled:
		until = now.Add(orDefault(retry, throttlePause))
		c.orders.extend(until, kind)
	}
	return until
}

func notSent(reason exchange.ErrorKind, until time.Time) error {
	return &exchange.Error{
		Kind: exchange.ErrorNotSent,
		Code: string(reason),
		Err:  fmt.Errorf("限流暂停到 %s，请求未发出", until.UTC().Format(time.RFC3339)),
	}
}

func retryAfter(header http.Header) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait
		}
	}
	return 0
}

func orDefault(value, fallback time.Duration) time.Duration {
	if value > 0 {
		return value
	}
	return fallback
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
