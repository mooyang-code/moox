package engine

import (
	"context"
	"net/http"
	"sync"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/modules/caddyhttp"
)

// admission owns complete request lifetimes, including Caddy's upgrade and
// streaming handlers. Cancellation closes their upstream and client streams.
type admission struct {
	mu     sync.Mutex
	open   bool
	next   uint64
	active map[uint64]context.CancelFunc
	idle   chan struct{}
}

func newAdmission() *admission {
	idle := make(chan struct{})
	close(idle)
	return &admission{active: make(map[uint64]context.CancelFunc), idle: idle}
}

func (a *admission) start(ctx context.Context) (context.Context, func(), bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.open {
		return ctx, nil, false
	}
	if len(a.active) == 0 {
		a.idle = make(chan struct{})
	}
	a.next++
	id := a.next
	ctx, cancel := context.WithCancel(ctx)
	a.active[id] = cancel
	return ctx, func() {
		cancel()
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.active, id)
		if len(a.active) == 0 {
			close(a.idle)
		}
	}, true
}

func (a *admission) allow() { a.mu.Lock(); a.open = true; a.mu.Unlock() }

func (a *admission) close() <-chan struct{} {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.open = false
	return a.idle
}

func (a *admission) cancel() {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, cancel := range a.active {
		cancel()
	}
}

// A Caddy library instance has process-wide configuration. A single owner is
// enforced by Start; provisioning binds the middleware to that owner.
var ownership struct {
	sync.Mutex
	current *admission
}

type admissionHandler struct{ gate *admission }

func init() { caddy.RegisterModule(admissionHandler{}) }

func (admissionHandler) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "http.handlers.moox_console_admission", New: func() caddy.Module { return new(admissionHandler) }}
}

func (h *admissionHandler) Provision(caddy.Context) error {
	// Start holds ownership while provisioning; do not take its mutex here.
	h.gate = ownership.current
	return nil
}

func (h *admissionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request, next caddyhttp.Handler) error {
	if h.gate == nil {
		http.Error(w, "proxy unavailable", http.StatusServiceUnavailable)
		return nil
	}
	ctx, done, ok := h.gate.start(r.Context())
	if !ok {
		http.Error(w, "proxy draining or starting", http.StatusServiceUnavailable)
		return nil
	}
	defer done()
	// Keep ResponseWriter intact: Hijacker, Flusher, and ResponseController
	// must remain available to Caddy's standard protocol handlers.
	return next.ServeHTTP(w, r.WithContext(ctx))
}
