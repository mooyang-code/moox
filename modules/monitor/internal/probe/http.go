package probe

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/requestauth"
)

const maxBodyExcerptBytes = 2048

// placementHTTPSTransport 探测部署的 https 健康地址（例如控制台代理）：只判断它是否在服务，不发送任何凭据，所以不校验
// 证书（control 通常用 Caddy 内部 CA 签发的证书）；不复用连接，避免探测之间互相影响。
var placementHTTPSTransport = &http.Transport{
	TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // 只探测存活，不传输凭据
	DisableKeepAlives: true,
}

// keepRedirect 不跟随重定向：https 探测把 3xx 也算正常。
func keepRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

type HTTPRunner struct {
	HealthSigner *HealthSigner
	Client       *http.Client
	Now          func() time.Time
}

type HealthSigner struct {
	Version   string
	AccessKey string
	SecretKey string
}

func (r HTTPRunner) Run(ctx context.Context, check domain.Check) domain.CheckResult {
	timeout := checkTimeout(check)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	method := check.Method
	if method == "" {
		method = http.MethodGet
	}
	matchStatus, err := ParseStatusExpectation(check.ExpectedStatus)
	if err != nil {
		return failResult(check, 0, err.Error())
	}
	req, err := http.NewRequestWithContext(reqCtx, method, check.URL, bytes.NewBufferString(check.Body))
	if err != nil {
		return failResult(check, 0, err.Error())
	}
	for k, v := range parseHeaders(check.Headers) {
		req.Header.Set(k, v)
	}
	if r.HealthSigner != nil && check.Source == domain.CheckSourcePlacement && isHealthPath(req.URL) {
		now := time.Now
		if r.Now != nil {
			now = r.Now
		}
		nonce, nonceErr := requestauth.NewNonce()
		if nonceErr != nil {
			return failResult(check, 0, nonceErr.Error())
		}
		timestamp := now().Unix()
		signature, signErr := requestauth.Sign(r.HealthSigner.SecretKey, requestauth.Material{Method: method, Path: req.URL.EscapedPath(), Body: []byte(check.Body), Timestamp: timestamp, Nonce: nonce})
		if signErr != nil {
			return failResult(check, 0, signErr.Error())
		}
		req.Header.Set("X-Moox-Health-Auth", strings.Join([]string{r.HealthSigner.Version, r.HealthSigner.AccessKey, strconv.FormatInt(timestamp, 10), nonce, signature}, "/"))
	}

	start := time.Now()
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	if check.Source == domain.CheckSourcePlacement && req.URL.Scheme == "https" {
		client = &http.Client{Timeout: timeout, Transport: placementHTTPSTransport, CheckRedirect: keepRedirect}
	}
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return failResult(check, latency, connectFailureText(err, timeout))
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyExcerptBytes+1))
	excerpt := string(body)
	if len(body) > maxBodyExcerptBytes {
		excerpt = string(body[:maxBodyExcerptBytes])
	}
	result := baseResult(check, latency)
	result.HTTPStatus = resp.StatusCode
	result.Connected = true
	result.BodyExcerpt = excerpt

	if !matchStatus(resp.StatusCode) {
		result.Success = false
		result.Status = domain.CheckStatusDown
		result.ErrorMessage = fmt.Sprintf("健康接口返回异常状态码 %d：服务未就绪或出错", resp.StatusCode)
		if reason := healthFailureReason(req.URL, excerpt); reason != "" {
			result.ErrorMessage = reason
		}
		return result
	}
	if check.MaxResponseMS > 0 && latency.Milliseconds() > int64(check.MaxResponseMS) {
		result.Success = false
		result.Status = domain.CheckStatusDegraded
		result.ErrorMessage = fmt.Sprintf("响应过慢：%dms，超过阈值 %dms", latency.Milliseconds(), check.MaxResponseMS)
		return result
	}
	if check.BodyContains != "" && !strings.Contains(excerpt, check.BodyContains) {
		result.Success = false
		result.Status = domain.CheckStatusDown
		result.ErrorMessage = "健康接口返回内容表明服务未就绪"
		return result
	}
	result.Success = true
	result.Status = domain.CheckStatusOK
	return result
}

func healthFailureReason(u *url.URL, body string) string {
	if u == nil || !isHealthPath(u) || strings.TrimSpace(body) == "" {
		return ""
	}
	var payload struct {
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return ""
	}
	for _, key := range []string{"storage_write_error", "database_checks_error", "observability_storage_write_error"} {
		if value, ok := payload.Details[key].(string); ok && strings.TrimSpace(value) != "" {
			return "健康检查失败：" + strings.TrimSpace(value)
		}
	}
	return ""
}

func isHealthPath(u *url.URL) bool {
	if u == nil {
		return false
	}
	switch u.EscapedPath() {
	case "/healthz", "/readyz", "/metrics":
		return true
	default:
		return false
	}
}

func ParseStatusExpectation(raw string) (func(int) bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "200-299"
	}
	var matchers []func(int) bool
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			pieces := strings.SplitN(part, "-", 2)
			minCode, err := strconv.Atoi(strings.TrimSpace(pieces[0]))
			if err != nil {
				return nil, fmt.Errorf("invalid status expectation %q", raw)
			}
			maxCode, err := strconv.Atoi(strings.TrimSpace(pieces[1]))
			if err != nil {
				return nil, fmt.Errorf("invalid status expectation %q", raw)
			}
			matchers = append(matchers, func(code int) bool {
				return code >= minCode && code <= maxCode
			})
			continue
		}
		expected, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("invalid status expectation %q", raw)
		}
		matchers = append(matchers, func(code int) bool {
			return code == expected
		})
	}
	if len(matchers) == 0 {
		return nil, fmt.Errorf("invalid status expectation %q", raw)
	}
	return func(code int) bool {
		for _, matcher := range matchers {
			if matcher(code) {
				return true
			}
		}
		return false
	}, nil
}

func parseHeaders(raw string) map[string]string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
