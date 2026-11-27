// Package health exposes gateway liveness, readiness, and local metrics.
package health

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type routeState struct {
	hash     string
	count    int
	disabled bool
	ready    bool
}

var (
	routesCurrentDesc = prometheus.NewDesc(
		"host_gateway_routes_current",
		"Number of routes currently active in the host gateway.",
		nil, nil,
	)
	routeInfoDesc = prometheus.NewDesc(
		"host_gateway_route_info",
		"Current route snapshot metadata for the host gateway.",
		[]string{"route_hash"}, nil,
	)
	routeLastSyncDesc = prometheus.NewDesc(
		"host_gateway_route_last_sync_timestamp_seconds",
		"Unix timestamp of the last successful control plane route sync.",
		nil, nil,
	)
	routeSyncStaleDesc = prometheus.NewDesc(
		"host_gateway_route_sync_stale",
		"Whether host gateway route synchronization is stale (1) or fresh (0).",
		nil, nil,
	)
)

type State struct {
	routes             atomic.Pointer[routeState]
	lastSyncUnix       atomic.Int64
	staleAfterSeconds  atomic.Int64
	clockMu            sync.RWMutex
	clock              func() time.Time
	startedAt          time.Time
	registry           *prometheus.Registry
	syncErrors         prometheus.Counter
	validationFailures prometheus.Counter
	authFailures       prometheus.Counter
	replayFailures     prometheus.Counter
	upstreamFailures   *prometheus.CounterVec
	requests           *prometheus.CounterVec
	durations          *prometheus.SummaryVec
}

func NewState() *State {
	staleAfter := int64(90)
	if value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("MOOX_GATEWAY_ROUTE_SYNC_STALE_AFTER_SECONDS")), 10, 64); err == nil && value > 0 {
		staleAfter = value
	}
	registry := prometheus.NewRegistry()
	state := &State{
		clock:     time.Now,
		startedAt: time.Now().UTC(),
		registry:  registry,
		syncErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "host_gateway_route_sync_errors_total",
			Help: "Total control plane route synchronization errors.",
		}),
		validationFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "host_gateway_route_validation_failures_total",
			Help: "Total route snapshot validation failures.",
		}),
		authFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "host_gateway_auth_failures_total",
			Help: "Total authentication and authorization failures at the host gateway.",
		}),
		replayFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "host_gateway_replay_failures_total",
			Help: "Total replay protection failures at the host gateway.",
		}),
		upstreamFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "host_gateway_upstream_failures_total",
			Help: "Total upstream forwarding failures by failure type.",
		}, []string{"type"}),
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "host_gateway_requests_total",
			Help: "Total requests handled by the host gateway.",
		}, []string{"service", "method", "status"}),
		durations: prometheus.NewSummaryVec(prometheus.SummaryOpts{
			Name: "host_gateway_request_duration_seconds",
			Help: "Request handling duration in seconds at the host gateway.",
		}, []string{"service", "method"}),
	}
	state.staleAfterSeconds.Store(staleAfter)
	// 预初始化已知的上游失败类型，确保尚未发生故障时指标也存在。
	state.upstreamFailures.WithLabelValues("connection")
	state.upstreamFailures.WithLabelValues("timeout")
	registry.MustRegister(
		state.syncErrors,
		state.validationFailures,
		state.authFailures,
		state.replayFailures,
		state.upstreamFailures,
		state.requests,
		state.durations,
		routeCollector{state: state},
	)
	return state
}

// SetClock is intended for deterministic runtime tests. It must be called
// before the State is shared with the health HTTP handler.
func (state *State) SetClock(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	state.clockMu.Lock()
	state.clock = now
	state.clockMu.Unlock()
}

// SetRouteSyncStaleAfter overrides the maximum age of a successful control
// plane sync and heartbeat before readiness is degraded.
func (state *State) SetRouteSyncStaleAfter(after time.Duration) {
	if after <= 0 {
		after = 90 * time.Second
	}
	state.staleAfterSeconds.Store(int64(after / time.Second))
}

func (state *State) ApplyRoutes(hash string, count int, disabled bool) {
	state.routes.Store(&routeState{hash: hash, count: count, disabled: disabled, ready: true})
}

func (state *State) Disabled() bool {
	current := state.routes.Load()
	return current != nil && current.disabled
}

func (state *State) Ready() bool {
	current := state.routes.Load()
	if current == nil || !current.ready {
		return false
	}
	now := state.now().Unix()
	staleAfter := state.staleAfterSeconds.Load()
	if staleAfter <= 0 {
		staleAfter = 90
	}
	lastSync := state.lastSyncUnix.Load()
	return lastSync > 0 && now >= lastSync && now-lastSync <= staleAfter
}

// Readiness 是 /readyz 用的就绪判断，也随指标上报带给 Monitor：路由快照已应用且在时限内同步过。
func (state *State) Readiness(context.Context) (bool, string) {
	if current := state.routes.Load(); current == nil || !current.ready {
		return false, "还没有应用路由快照"
	}
	if !state.Ready() {
		return false, "路由快照同步已过期"
	}
	return true, ""
}

// Snapshot 返回 /healthz 与 /readyz 使用的标准健康快照。
func (state *State) Snapshot(ctx context.Context) healthz.Response {
	ready, reason := state.Readiness(ctx)
	payload := healthz.Base("host_gateway", "", "", "", state.startedAt, ready)
	if !ready {
		payload.Reason = reason
	}
	hash, count := state.Current()
	payload.Details = map[string]any{"route_hash": hash, "route_count": count, "disabled": state.Disabled()}
	return payload
}

func (state *State) now() time.Time {
	state.clockMu.RLock()
	now := state.clock
	state.clockMu.RUnlock()
	if now == nil {
		return time.Now()
	}
	return now()
}

func (state *State) Current() (string, int) {
	current := state.routes.Load()
	if current == nil {
		return "", 0
	}
	return current.hash, current.count
}

func (state *State) RouteSyncFailed()       { state.syncErrors.Inc() }
func (state *State) RouteValidationFailed() { state.validationFailures.Inc() }
func (state *State) AuthFailed()            { state.authFailures.Inc() }
func (state *State) ReplayFailed()          { state.replayFailures.Inc() }

func (state *State) RouteSyncSucceeded(at time.Time) {
	state.lastSyncUnix.Store(at.Unix())
}

func (state *State) UpstreamFailed(kind string) {
	switch kind {
	case "timeout", "connection":
		state.upstreamFailures.WithLabelValues(kind).Inc()
	}
}

func (state *State) ObserveRequest(service, method string, status int, elapsed time.Duration) {
	state.requests.WithLabelValues(service, method, strconv.Itoa(status)).Inc()
	state.durations.WithLabelValues(service, method).Observe(elapsed.Seconds())
}

func (state *State) Handler() http.Handler {
	gatherers := prometheus.Gatherers{prometheus.DefaultGatherer}
	if state.registry != nil {
		gatherers = prometheus.Gatherers{state.registry, prometheus.DefaultGatherer}
	}
	return healthz.StandardMux(state.Snapshot, promhttp.HandlerFor(gatherers, promhttp.HandlerOpts{DisableCompression: true}))
}

type routeCollector struct {
	state *State
}

func (routeCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- routesCurrentDesc
	ch <- routeInfoDesc
	ch <- routeLastSyncDesc
	ch <- routeSyncStaleDesc
}

func (c routeCollector) Collect(ch chan<- prometheus.Metric) {
	hash, count := c.state.Current()
	ch <- prometheus.MustNewConstMetric(routesCurrentDesc, prometheus.GaugeValue, float64(count))
	ch <- prometheus.MustNewConstMetric(routeInfoDesc, prometheus.GaugeValue, 1, hash)
	ch <- prometheus.MustNewConstMetric(routeLastSyncDesc, prometheus.GaugeValue, float64(c.state.lastSyncUnix.Load()))
	stale := 1.0
	if c.state.Ready() {
		stale = 0
	}
	ch <- prometheus.MustNewConstMetric(routeSyncStaleDesc, prometheus.GaugeValue, stale)
}
