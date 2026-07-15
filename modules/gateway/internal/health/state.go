// Package health exposes gateway liveness, readiness, and local metrics.
package health

import (
	"fmt"
	"net/http"
	"sync/atomic"
)

type routeState struct {
	hash     string
	count    int
	disabled bool
	ready    bool
}

type State struct {
	routes     atomic.Pointer[routeState]
	syncErrors atomic.Uint64
}

func NewState() *State { return &State{} }

func (state *State) ApplyRoutes(hash string, count int, disabled bool) {
	state.routes.Store(&routeState{hash: hash, count: count, disabled: disabled, ready: true})
}

func (state *State) Disabled() bool {
	current := state.routes.Load()
	return current != nil && current.disabled
}

func (state *State) Ready() bool {
	current := state.routes.Load()
	return current != nil && current.ready
}

func (state *State) Current() (string, int) {
	current := state.routes.Load()
	if current == nil {
		return "", 0
	}
	return current.hash, current.count
}

func (state *State) RouteSyncFailed() { state.syncErrors.Add(1) }

func (state *State) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, _ *http.Request) {
		if !state.Ready() {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ready\n"))
	})
	mux.HandleFunc("GET /metrics", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = fmt.Fprintf(response, "# HELP gateway_route_sync_errors_total Total failed gateway route synchronizations.\n# TYPE gateway_route_sync_errors_total counter\ngateway_route_sync_errors_total %d\n", state.syncErrors.Load())
	})
	return mux
}
