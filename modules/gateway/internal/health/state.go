// Package health exposes gateway liveness, readiness, and local metrics.
package health

import (
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type routeState struct {
	hash     string
	count    int
	disabled bool
	ready    bool
}

type requestKey struct {
	service string
	method  string
	status  int
}

type durationKey struct{ service, method string }
type durationValue struct {
	count uint64
	sum   float64
}
type storageCheck struct{ check func() error }

type State struct {
	routes             atomic.Pointer[routeState]
	storage            atomic.Pointer[storageCheck]
	syncErrors         atomic.Uint64
	validationFailures atomic.Uint64
	reportErrors       atomic.Uint64
	authFailures       atomic.Uint64
	replayFailures     atomic.Uint64
	connectionFailures atomic.Uint64
	timeoutFailures    atomic.Uint64
	lastSyncUnix       atomic.Int64
	lastReportUnix     atomic.Int64
	staleAfterSeconds  atomic.Int64
	clockMu            sync.RWMutex
	clock              func() time.Time
	mu                 sync.Mutex
	requests           map[requestKey]uint64
	durations          map[durationKey]durationValue
}

func NewState() *State {
	staleAfter := int64(90)
	if value, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("MOOX_GATEWAY_ROUTE_SYNC_STALE_AFTER_SECONDS")), 10, 64); err == nil && value > 0 {
		staleAfter = value
	}
	state := &State{requests: make(map[requestKey]uint64), durations: make(map[durationKey]durationValue), clock: time.Now}
	state.staleAfterSeconds.Store(staleAfter)
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

func (state *State) SetStorageCheck(check func() error) {
	if check == nil {
		state.storage.Store(nil)
		return
	}
	state.storage.Store(&storageCheck{check: check})
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
	lastReport := state.lastReportUnix.Load()
	return lastSync > 0 && lastReport > 0 && now >= lastSync && now-lastSync <= staleAfter && now >= lastReport && now-lastReport <= staleAfter
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

func (state *State) RouteSyncFailed()       { state.syncErrors.Add(1) }
func (state *State) RouteValidationFailed() { state.validationFailures.Add(1) }
func (state *State) RouteReportFailed() {
	state.reportErrors.Add(1)
}
func (state *State) AuthFailed()   { state.authFailures.Add(1) }
func (state *State) ReplayFailed() { state.replayFailures.Add(1) }

func (state *State) RouteSyncSucceeded(at time.Time) {
	state.lastSyncUnix.Store(at.Unix())
}

func (state *State) RouteReportSucceeded(at time.Time) {
	state.lastReportUnix.Store(at.Unix())
}

func (state *State) UpstreamFailed(kind string) {
	switch kind {
	case "timeout":
		state.timeoutFailures.Add(1)
	case "connection":
		state.connectionFailures.Add(1)
	}
}

func (state *State) ObserveRequest(service, method string, status int, elapsed time.Duration) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.requests[requestKey{service: service, method: method, status: status}]++
	key := durationKey{service: service, method: method}
	value := state.durations[key]
	value.count++
	value.sum += elapsed.Seconds()
	state.durations[key] = value
}

func (state *State) Handler() http.Handler {
	mux := http.NewServeMux()
	prometheusHandler := promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{DisableCompression: true})
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		if check := state.storage.Load(); check != nil && check.check() != nil {
			http.Error(response, "persistent storage unavailable", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, _ *http.Request) {
		if !state.Ready() {
			http.Error(response, "control plane sync or heartbeat is stale", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /metrics", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		state.writeMetrics(response)
		prometheusHandler.ServeHTTP(response, request)
	})
	return mux
}

func (state *State) writeMetrics(response http.ResponseWriter) {
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_sync_errors_total counter\ngateway_route_sync_errors_total %d\n", state.syncErrors.Load())
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_validation_failures_total counter\ngateway_route_validation_failures_total %d\n", state.validationFailures.Load())
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_report_errors_total counter\ngateway_route_report_errors_total %d\n", state.reportErrors.Load())
	_, _ = fmt.Fprintf(response, "# TYPE gateway_auth_failures_total counter\ngateway_auth_failures_total %d\n", state.authFailures.Load())
	_, _ = fmt.Fprintf(response, "# TYPE gateway_replay_failures_total counter\ngateway_replay_failures_total %d\n", state.replayFailures.Load())
	_, _ = fmt.Fprintf(response, "# TYPE gateway_upstream_failures_total counter\ngateway_upstream_failures_total{type=\"connection\"} %d\ngateway_upstream_failures_total{type=\"timeout\"} %d\n", state.connectionFailures.Load(), state.timeoutFailures.Load())
	hash, count := state.Current()
	_, _ = fmt.Fprintf(response, "# TYPE gateway_routes_current gauge\ngateway_routes_current %d\n", count)
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_info gauge\ngateway_route_info{route_hash=\"%s\"} 1\n", escapeLabel(hash))
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_last_sync_timestamp_seconds gauge\ngateway_route_last_sync_timestamp_seconds %d\n", state.lastSyncUnix.Load())
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_last_report_timestamp_seconds gauge\ngateway_route_last_report_timestamp_seconds %d\n", state.lastReportUnix.Load())
	stale := 1
	if state.Ready() {
		stale = 0
	}
	_, _ = fmt.Fprintf(response, "# TYPE gateway_route_sync_stale gauge\ngateway_route_sync_stale %d\n", stale)

	state.mu.Lock()
	requestKeys := make([]requestKey, 0, len(state.requests))
	for key := range state.requests {
		requestKeys = append(requestKeys, key)
	}
	sort.Slice(requestKeys, func(i, j int) bool {
		left, right := requestKeys[i], requestKeys[j]
		if left.service != right.service {
			return left.service < right.service
		}
		if left.method != right.method {
			return left.method < right.method
		}
		return left.status < right.status
	})
	durationKeys := make([]durationKey, 0, len(state.durations))
	for key := range state.durations {
		durationKeys = append(durationKeys, key)
	}
	sort.Slice(durationKeys, func(i, j int) bool {
		if durationKeys[i].service != durationKeys[j].service {
			return durationKeys[i].service < durationKeys[j].service
		}
		return durationKeys[i].method < durationKeys[j].method
	})
	requests := make(map[requestKey]uint64, len(state.requests))
	for key, value := range state.requests {
		requests[key] = value
	}
	durations := make(map[durationKey]durationValue, len(state.durations))
	for key, value := range state.durations {
		durations[key] = value
	}
	state.mu.Unlock()

	_, _ = fmt.Fprintln(response, "# TYPE gateway_requests_total counter")
	for _, key := range requestKeys {
		_, _ = fmt.Fprintf(response, "gateway_requests_total{service=\"%s\",method=\"%s\",status=\"%d\"} %d\n", escapeLabel(key.service), escapeLabel(key.method), key.status, requests[key])
	}
	_, _ = fmt.Fprintln(response, "# TYPE gateway_request_duration_seconds summary")
	for _, key := range durationKeys {
		labels := fmt.Sprintf("service=\"%s\",method=\"%s\"", escapeLabel(key.service), escapeLabel(key.method))
		value := durations[key]
		_, _ = fmt.Fprintf(response, "gateway_request_duration_seconds_sum{%s} %s\ngateway_request_duration_seconds_count{%s} %d\n", labels, strconv.FormatFloat(value.sum, 'g', -1, 64), labels, value.count)
	}
}

func escapeLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}
