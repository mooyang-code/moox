package probe

import (
	"bytes"
	"context"
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
	if r.HealthSigner != nil && check.Source == domain.CheckSourceSysDeploy && isHealthPath(req.URL) {
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
	resp, err := client.Do(req)
	latency := time.Since(start)
	if err != nil {
		return failResult(check, latency, err.Error())
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
		result.ErrorMessage = fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode)
		return result
	}
	if check.MaxResponseMS > 0 && latency.Milliseconds() > int64(check.MaxResponseMS) {
		result.Success = false
		result.Status = domain.CheckStatusDegraded
		result.ErrorMessage = fmt.Sprintf("response time %dms exceeds %dms", latency.Milliseconds(), check.MaxResponseMS)
		return result
	}
	if check.BodyContains != "" && !strings.Contains(excerpt, check.BodyContains) {
		result.Success = false
		result.Status = domain.CheckStatusDown
		result.ErrorMessage = "response body does not contain expected text"
		return result
	}
	result.Success = true
	result.Status = domain.CheckStatusOK
	return result
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
