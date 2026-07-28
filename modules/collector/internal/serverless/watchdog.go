package serverless

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/msgbox"
	"github.com/mooyang-code/moox/packages/observabilitypb"
	"github.com/mooyang-code/moox/packages/requestauth"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	maxWatchdogChecks      = 8
	maxWatchdogErrorRunes  = 256
	defaultWatchdogTimeout = 20 * time.Second
	defaultDirectCooldown  = 5 * time.Minute
	directSendTimeout      = 5 * time.Second
)

type WatchdogCheck func(context.Context) CheckResult

type CheckResult struct {
	CheckID    string
	Target     string
	Kind       string
	Success    bool
	StatusCode int
	Latency    time.Duration
	ErrorCode  string
	Error      string
	CheckedAt  time.Time
}

type HealthAuth struct {
	Version   string
	AccessKey string
	SecretKey string
}

type HealthEventReporter interface {
	ReportHealth(context.Context, *observabilitypb.HealthCheckReport, string) error
}

type MetricsReporter interface {
	Handle(context.Context) error
}

type WatchdogOptions struct {
	Enabled        bool
	ObserverID     string
	SpaceID        string
	Ready          func() bool
	Checks         []WatchdogCheck
	Events         HealthEventReporter
	Metrics        MetricsReporter
	DirectSender   msgbox.Sender
	Timeout        time.Duration
	DirectCooldown time.Duration
	Now            func() time.Time
	OnSkipped      func(string)
}

type WatchdogHandler struct {
	options        WatchdogOptions
	running        atomic.Bool
	lastDirectUnix atomic.Int64
}

func NewWatchdogHandler(options WatchdogOptions) (*WatchdogHandler, error) {
	if len(options.Checks) > maxWatchdogChecks {
		return nil, fmt.Errorf("SCF watchdog supports at most %d checks", maxWatchdogChecks)
	}
	if strings.TrimSpace(options.ObserverID) == "" {
		return nil, errors.New("SCF watchdog observer_id is required")
	}
	if options.Ready == nil {
		options.Ready = func() bool { return false }
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Timeout <= 0 {
		options.Timeout = defaultWatchdogTimeout
	}
	if options.DirectCooldown <= 0 {
		options.DirectCooldown = defaultDirectCooldown
	}
	return &WatchdogHandler{options: options}, nil
}

func (h *WatchdogHandler) Handle(ctx context.Context) error {
	if h == nil || !h.options.Enabled {
		return nil
	}
	if !h.running.CompareAndSwap(false, true) {
		h.skipped("overlap")
		return nil
	}
	defer h.running.Store(false)
	if !h.options.Ready() {
		h.skipped("not_ready")
		return nil
	}
	runCtx, cancel := context.WithTimeout(ctx, h.options.Timeout)
	defer cancel()
	return h.run(runCtx)
}

func (h *WatchdogHandler) run(ctx context.Context) error {
	results := make([]CheckResult, 0, len(h.options.Checks))
	monitorDown := false
	publishFailed := false
	for _, check := range h.options.Checks {
		result := check(ctx)
		if result.CheckedAt.IsZero() {
			result.CheckedAt = h.options.Now().UTC()
		}
		result.Error = truncateRunes(strings.TrimSpace(result.Error), maxWatchdogErrorRunes)
		results = append(results, result)
		if result.CheckID == "monitor_ready" && !result.Success {
			monitorDown = true
		}
		if h.options.Events != nil && !publishFailed {
			report := &observabilitypb.HealthCheckReport{
				ObserverId:   h.options.ObserverID,
				CheckId:      result.CheckID,
				Target:       result.Target,
				Kind:         result.Kind,
				Success:      result.Success,
				StatusCode:   int32(result.StatusCode),
				LatencyMs:    result.Latency.Milliseconds(),
				ErrorCode:    result.ErrorCode,
				ErrorSummary: result.Error,
				CheckedAt:    timestamppb.New(result.CheckedAt.UTC()),
			}
			if err := h.options.Events.ReportHealth(ctx, report, h.options.SpaceID); err != nil {
				publishFailed = true
			}
		}
	}
	var metricsErr error
	if h.options.Metrics != nil && !publishFailed {
		metricsErr = h.options.Metrics.Handle(ctx)
		if metricsErr != nil {
			publishFailed = true
		}
	}
	if monitorDown || publishFailed {
		directCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), directSendTimeout)
		h.sendDirect(directCtx, results, monitorDown, publishFailed)
		cancel()
	}
	if publishFailed {
		return errors.New("SCF watchdog health event publish failed")
	}
	return metricsErr
}

func (h *WatchdogHandler) sendDirect(ctx context.Context, results []CheckResult, monitorDown, publishFailed bool) {
	if h.options.DirectSender == nil {
		return
	}
	now := h.options.Now().UTC()
	if lastUnix := h.lastDirectUnix.Load(); lastUnix > 0 {
		if now.Sub(time.Unix(lastUnix, 0)) < h.options.DirectCooldown {
			return
		}
	}
	var failures []string
	for _, result := range results {
		if !result.Success {
			failures = append(failures, result.CheckID+": "+result.Error)
		}
	}
	if monitorDown {
		failures = append(failures, "central monitor unavailable")
	}
	if publishFailed {
		failures = append(failures, "observability EventBus publish failed")
	}
	body := truncateRunes(strings.Join(failures, "\n"), 4096)
	if err := h.options.DirectSender.Send(ctx, msgbox.Message{
		Key:      h.options.ObserverID,
		Severity: msgbox.SeverityCritical,
		Title:    "MooX SCF Sentinel",
		Body:     body,
		Labels:   map[string]string{"space_id": truncateRunes(h.options.SpaceID, 256)},
	}); err == nil {
		h.lastDirectUnix.Store(now.Unix())
	}
}

func (h *WatchdogHandler) skipped(reason string) {
	if h.options.OnSkipped != nil {
		h.options.OnSkipped(reason)
	}
}

func SignedHTTPReadyCheck(checkID, target string, client *http.Client, auth HealthAuth) WatchdogCheck {
	return func(ctx context.Context) CheckResult {
		startedAt := time.Now()
		result := CheckResult{CheckID: checkID, Target: target, Kind: "http", CheckedAt: startedAt.UTC()}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			result.ErrorCode, result.Error = "invalid_target", err.Error()
			return result
		}
		if strings.TrimSpace(auth.SecretKey) != "" {
			timestamp := time.Now().UTC().Unix()
			nonce, err := requestauth.NewNonce()
			if err != nil {
				result.ErrorCode, result.Error = "auth_nonce", "generate health auth nonce"
				return result
			}
			signature, err := requestauth.Sign(auth.SecretKey, requestauth.Material{
				Method: request.Method, Path: request.URL.EscapedPath(), Timestamp: timestamp, Nonce: nonce,
			})
			if err != nil {
				result.ErrorCode, result.Error = "auth_sign", "sign health request"
				return result
			}
			version := firstNonBlank(auth.Version, "moox-health-v1")
			request.Header.Set("X-Moox-Health-Auth", fmt.Sprintf("%s/%s/%d/%s/%s", version, auth.AccessKey, timestamp, nonce, signature))
		}
		if client == nil {
			client = &http.Client{Timeout: 5 * time.Second}
		}
		response, err := client.Do(request)
		result.Latency = time.Since(startedAt)
		if err != nil {
			result.ErrorCode, result.Error = "unreachable", err.Error()
			return result
		}
		defer response.Body.Close()
		result.StatusCode = response.StatusCode
		result.Success = response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices
		if !result.Success {
			result.ErrorCode = "http_status"
			result.Error = fmt.Sprintf("HTTP %d", response.StatusCode)
		}
		return result
	}
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}
