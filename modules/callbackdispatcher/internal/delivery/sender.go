// Package delivery 向用户填写的通知地址发 POST：请求格式与签名、出站防护、结果分类和重试时间表。
package delivery

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/netguard"
)

const (
	// maxResponseBytes 是最多读取的响应体大小；snippetBytes 是记录下来的前缀长度。
	maxResponseBytes = 64 << 10
	snippetBytes     = 1 << 10
	userAgent        = "MooX-EventCenter/1"
)

// Request 是要通知的一条事件。
type Request struct {
	RuleID       string
	EventID      string
	EventName    string
	EventVersion uint32
	SpaceID      string
	SubjectID    string
	OccurredAt   time.Time
	// PayloadJSON 是事件 payload 转成的 JSON 对象。
	PayloadJSON string
}

// Target 是通知地址与验证密钥。
type Target struct {
	URL          string
	Secret       string
	AllowPrivate bool
}

// Class 是一次投递结果的分类。
type Class int

const (
	// Success 是返回了 2xx。
	Success Class = iota
	// Retryable 是网络错误、超时、5xx、408、429 这类重试可能好转的失败。
	Retryable
	// Permanent 是多半配置错了、重试也没用的失败：其余 4xx、重定向、地址被防护拒绝、地址无效。
	Permanent
)

// Result 是一次投递的结果。
type Result struct {
	Class      Class
	StatusCode int
	Duration   time.Duration
	// Snippet 是对方返回内容的前 1KB。
	Snippet string
	// Err 是没拿到响应时的原因，或者对状态码的说明。
	Err error
}

// Config 是发送器的超时。
type Config struct {
	ConnectTimeout time.Duration
	RequestTimeout time.Duration
}

// Sender 发送通知。出站请求不经过任何代理，不跟随重定向，不复用连接，
// 并且按“是否允许内网地址”分成两个客户端，让地址检查不会被另一条规则建立的连接绕过。
type Sender struct {
	public  *http.Client
	private *http.Client
	now     func() time.Time
}

// NewSender 创建发送器。
func NewSender(cfg Config) *Sender {
	return newSender(cfg, nil, nil)
}

// newSender 的 lookup、dial 为空时用系统解析与拨号；测试注入它们，让地址检查照常执行而不真正出网。
func newSender(cfg Config, lookup func(context.Context, string) ([]string, error), dial func(ctx context.Context, network, address string) (net.Conn, error)) *Sender {
	build := func(allowPrivate bool) *http.Client {
		options := netguard.DialOptions{AllowPrivate: allowPrivate, Lookup: lookup, Dial: dial}
		if dial == nil {
			options.Dial = (&net.Dialer{Timeout: cfg.ConnectTimeout}).DialContext
		}
		transport := &http.Transport{
			Proxy:               nil,
			DialContext:         netguard.NewDialContext(options),
			DisableKeepAlives:   true,
			TLSHandshakeTimeout: cfg.ConnectTimeout,
		}
		return &http.Client{
			Transport:     transport,
			Timeout:       cfg.RequestTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	return &Sender{public: build(false), private: build(true), now: time.Now}
}

type envelope struct {
	EventID    string          `json:"event_id"`
	Event      string          `json:"event"`
	Version    uint32          `json:"version"`
	SpaceID    string          `json:"space_id"`
	Subject    string          `json:"subject"`
	OccurredAt string          `json:"occurred_at"`
	RuleID     string          `json:"rule_id"`
	Data       json.RawMessage `json:"data"`
}

// BuildBody 生成请求体。
func BuildBody(req Request) ([]byte, error) {
	data := json.RawMessage(req.PayloadJSON)
	if len(bytes.TrimSpace(data)) == 0 {
		data = json.RawMessage("{}")
	}
	return json.Marshal(envelope{
		EventID: req.EventID, Event: req.EventName, Version: req.EventVersion, SpaceID: req.SpaceID, Subject: req.SubjectID,
		OccurredAt: req.OccurredAt.UTC().Format(time.RFC3339), RuleID: req.RuleID, Data: data,
	})
}

// Sign 返回签名头的值：sha256=<HMAC-SHA256(密钥, 时间戳 + "." + 请求体) 的十六进制>。
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// ValidateURL 检查通知地址：只允许带主机名的 http、https 地址，不允许内嵌账号密码。
func ValidateURL(raw string) error {
	if len(raw) > 2048 {
		return errors.New("通知地址太长")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.Hostname() == "" {
		return errors.New("通知地址不是有效的网址")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("通知地址只能是 http 或 https")
	}
	if parsed.User != nil {
		return errors.New("通知地址不能内嵌账号密码")
	}
	return nil
}

// Send 发送一次。
func (s *Sender) Send(ctx context.Context, target Target, req Request) Result {
	started := s.now()
	finish := func(result Result) Result {
		result.Duration = s.now().Sub(started)
		return result
	}
	if err := ValidateURL(target.URL); err != nil {
		return finish(Result{Class: Permanent, Err: err})
	}
	body, err := BuildBody(req)
	if err != nil {
		return finish(Result{Class: Permanent, Err: fmt.Errorf("生成请求体失败: %w", err)})
	}
	timestamp := strconv.FormatInt(started.UnixMilli(), 10)
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL, bytes.NewReader(body))
	if err != nil {
		return finish(Result{Class: Permanent, Err: err})
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", userAgent)
	httpRequest.Header.Set("X-MooX-Event-Id", req.EventID)
	httpRequest.Header.Set("X-MooX-Event", req.EventName)
	httpRequest.Header.Set("X-MooX-Timestamp", timestamp)
	httpRequest.Header.Set("X-MooX-Signature", Sign(target.Secret, timestamp, body))

	client := s.public
	if target.AllowPrivate {
		client = s.private
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return finish(classifyTransportError(err))
	}
	defer response.Body.Close()
	snippet, _ := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if len(snippet) > snippetBytes {
		snippet = snippet[:snippetBytes]
	}
	result := Result{StatusCode: response.StatusCode, Snippet: strings.ToValidUTF8(string(snippet), "")}
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		result.Class = Success
	case response.StatusCode == http.StatusRequestTimeout || response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		result.Class, result.Err = Retryable, fmt.Errorf("对方返回状态码 %d", response.StatusCode)
	case response.StatusCode >= 300 && response.StatusCode < 400:
		result.Class, result.Err = Permanent, fmt.Errorf("对方返回状态码 %d（不跟随重定向，请直接填写最终地址）", response.StatusCode)
	default:
		result.Class, result.Err = Permanent, fmt.Errorf("对方返回状态码 %d", response.StatusCode)
	}
	return finish(result)
}

func classifyTransportError(err error) Result {
	switch {
	case errors.Is(err, netguard.ErrAddressBlocked):
		return Result{Class: Permanent, Err: errors.New("通知地址被出站防护拒绝：回环、链路本地、组播地址一律不允许，内网地址需要在规则里勾选“允许内网地址”")}
	case errors.Is(err, netguard.ErrLookupFailed):
		return Result{Class: Retryable, Err: errors.New("通知地址的域名解析失败")}
	case errors.Is(err, context.DeadlineExceeded):
		return Result{Class: Retryable, Err: errors.New("请求超时")}
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return Result{Class: Retryable, Err: errors.New("请求超时")}
	}
	// 其余按网络错误处理；错误文本里可能带地址，只保留最里面的原因。
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return Result{Class: Retryable, Err: fmt.Errorf("连接失败: %v", err)}
}

// retryDelays 是第 n 次失败之后到下一次重试的等待时间。
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour, 12 * time.Hour}

// NextDelay 返回第 failures 次失败（从 1 开始）之后的等待时间；重试次数用完时 ok 为 false。
func NextDelay(failures int) (time.Duration, bool) {
	if failures < 1 || failures > len(retryDelays) {
		return 0, false
	}
	return retryDelays[failures-1], true
}
