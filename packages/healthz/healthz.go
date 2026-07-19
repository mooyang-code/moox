package healthz

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/server"
)

// Response is the shared process-level health payload exposed by MooX services.
type Response struct {
	Module             string         `json:"module"`
	Service            string         `json:"service,omitempty"`
	InstanceID         string         `json:"instance_id,omitempty"`
	Ready              bool           `json:"ready"`
	Status             string         `json:"status"`
	Version            string         `json:"version,omitempty"`
	GitCommit          string         `json:"git_commit,omitempty"`
	BootID             string         `json:"boot_id,omitempty"`
	BuildTime          string         `json:"build_time,omitempty"`
	ConfigHash         string         `json:"config_hash,omitempty"`
	PipelineConfigHash string         `json:"pipeline_config_hash,omitempty"`
	StartTime          time.Time      `json:"start_time,omitempty"`
	Time               time.Time      `json:"time"`
	Details            map[string]any `json:"details,omitempty"`
}

// SnapshotFunc returns a health snapshot for the current request.
type SnapshotFunc func(context.Context) Response

// State contains the shared process health state used by module wrappers.
type State struct {
	Module             string
	InstanceID         string
	Version            string
	GitCommit          string
	BootID             string
	BuildTime          string
	ConfigHash         string
	PipelineConfigHash string
	StartedAt          time.Time
	ReadyFlag          atomic.Bool
	SnapshotFunc       SnapshotFunc
}

// NewState creates a shared health state.
func NewState(module, instance, version, commit string) *State {
	return &State{
		Module: module, InstanceID: instance, Version: version, GitCommit: commit,
		BootID: os.Getenv("MOOX_BOOT_ID"), BuildTime: os.Getenv("MOOX_BUILD_TIME"),
		ConfigHash: os.Getenv("MOOX_CONFIG_HASH"), PipelineConfigHash: os.Getenv("MOOX_PIPELINE_CONFIG_HASH"),
		StartedAt: time.Now().UTC(),
	}
}

// Ready reports the current readiness flag.
func (s *State) Ready() bool { return s != nil && s.ReadyFlag.Load() }

// SetReady updates the current readiness flag.
func (s *State) SetReady(value bool) {
	if s != nil {
		s.ReadyFlag.Store(value)
	}
}

// Snapshot returns the current state and prevents a custom snapshot from
// reporting ready before the module has explicitly completed initialization.
func (s *State) Snapshot(ctx context.Context) Response {
	if s != nil && s.SnapshotFunc != nil {
		rsp := s.SnapshotFunc(ctx)
		if !s.Ready() {
			rsp.Ready = false
			if rsp.Status == "ok" {
				rsp.Status = "degraded"
			}
		}
		s.enrich(&rsp)
		return rsp
	}
	if s == nil {
		return Response{Ready: false, Status: "error", Time: time.Now()}
	}
	return Base(s.Module, s.InstanceID, s.Version, s.GitCommit, s.StartedAt, s.Ready())
}

func (s *State) enrich(rsp *Response) {
	if s == nil || rsp == nil {
		return
	}
	if rsp.BootID == "" {
		rsp.BootID = s.BootID
	}
	if rsp.BuildTime == "" {
		rsp.BuildTime = s.BuildTime
	}
	if rsp.ConfigHash == "" {
		rsp.ConfigHash = s.ConfigHash
	}
	if rsp.PipelineConfigHash == "" {
		rsp.PipelineConfigHash = s.PipelineConfigHash
	}
}

// Mux is the exact-path router used by MooX standard HTTP services.
// tRPC's http_no_protocol registration accepts a net/http.Handler, while the
// service lifecycle remains owned by thttp.RegisterNoProtocolServiceMux.
type Mux struct {
	routes   map[string]http.Handler
	prefixes []muxPrefix
}

type muxPrefix struct {
	path    string
	handler http.Handler
}

// NewMux creates an exact-path HTTP router for a tRPC standard service.
func NewMux() *Mux {
	return &Mux{routes: make(map[string]http.Handler)}
}

// Handle registers an exact URL path.
func (m *Mux) Handle(path string, handler http.Handler) {
	if m == nil || path == "" || handler == nil {
		return
	}
	if m.routes == nil {
		m.routes = make(map[string]http.Handler)
	}
	m.routes[path] = handler
}

// HandleFunc registers an exact URL path handler.
func (m *Mux) HandleFunc(path string, handler http.HandlerFunc) {
	m.Handle(path, handler)
}

// HandlePrefix registers a longest-prefix route for diagnostic endpoints such
// as /debug/pprof/. Exact routes still take precedence.
func (m *Mux) HandlePrefix(path string, handler http.Handler) {
	if m == nil || path == "" || handler == nil {
		return
	}
	m.prefixes = append(m.prefixes, muxPrefix{path: path, handler: handler})
}

// ServeHTTP dispatches the exact path or returns 404.
func (m *Mux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if m != nil {
		if handler, ok := m.routes[r.URL.Path]; ok {
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
				return
			}
			handler.ServeHTTP(w, r)
			return
		}
		var matched *muxPrefix
		for i := range m.prefixes {
			candidate := &m.prefixes[i]
			if strings.HasPrefix(r.URL.Path, candidate.path) && (matched == nil || len(candidate.path) > len(matched.path)) {
				matched = candidate
			}
		}
		if matched != nil {
			matched.handler.ServeHTTP(w, r)
			return
		}
	}
	http.NotFound(w, r)
}

// Base constructs a standard health payload with stable fields populated.
func Base(module, instanceID, version, gitCommit string, start time.Time, ready bool) Response {
	status := "degraded"
	if ready {
		status = "ok"
	}
	return Response{
		Module:             module,
		Service:            module,
		InstanceID:         instanceID,
		Ready:              ready,
		Status:             status,
		Version:            version,
		GitCommit:          gitCommit,
		BootID:             os.Getenv("MOOX_BOOT_ID"),
		BuildTime:          os.Getenv("MOOX_BUILD_TIME"),
		ConfigHash:         os.Getenv("MOOX_CONFIG_HASH"),
		PipelineConfigHash: os.Getenv("MOOX_PIPELINE_CONFIG_HASH"),
		StartTime:          start,
		Time:               time.Now(),
	}
}

// Handler converts a SnapshotFunc into a readiness JSON HTTP handler.
func Handler(snapshot SnapshotFunc) http.Handler {
	return readinessHandler(snapshot)
}

// LivenessHandler returns HTTP 200 when the process can execute the handler.
// Dependency state is intentionally kept in Details, while readiness is
// reported by ReadinessHandler on /readyz.
func LivenessHandler(snapshot SnapshotFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rsp := safeSnapshot(r.Context(), snapshot)
		rsp.Ready = true
		rsp.Status = "ok"
		if rsp.Time.IsZero() {
			rsp.Time = time.Now()
		}
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(rsp)
	})
}

// ReadinessHandler returns HTTP 503 until the snapshot reports Ready=true.
func ReadinessHandler(snapshot SnapshotFunc) http.Handler {
	return readinessHandler(snapshot)
}

func readinessHandler(snapshot SnapshotFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		rsp := safeSnapshot(r.Context(), snapshot)
		if rsp.Time.IsZero() {
			rsp.Time = time.Now()
		}
		if rsp.Status == "" {
			if rsp.Ready {
				rsp.Status = "ok"
			} else {
				rsp.Status = "degraded"
			}
		}
		code := http.StatusOK
		if !rsp.Ready {
			code = http.StatusServiceUnavailable
		}
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(rsp)
	})
}

// RegisterNoProtocolServiceMux exposes health endpoints through a tRPC
// http_no_protocol service. This keeps health traffic on the tRPC server's
// lifecycle, filters, timeout and monitoring pipeline instead of opening a
// second net/http listener.
func RegisterNoProtocolServiceMux(service server.Service, mux http.Handler) error {
	if service == nil {
		return errors.New("health service is not configured")
	}
	if mux == nil {
		return errors.New("health handler is required")
	}
	thttp.RegisterNoProtocolServiceMux(service, mux)
	return nil
}

// StandardMux builds the monitor-facing health and metrics routes.
func StandardMux(snapshot SnapshotFunc, metrics http.Handler) http.Handler {
	mux := NewMux()
	mux.Handle("/healthz", LivenessHandler(snapshot))
	mux.Handle("/readyz", ReadinessHandler(snapshot))
	if metrics != nil {
		mux.Handle("/metrics", metrics)
	}
	return mux
}

func safeSnapshot(ctx context.Context, snapshot SnapshotFunc) (rsp Response) {
	defer func() {
		if recover() != nil {
			rsp = Response{
				Ready:  false,
				Status: "error",
				Time:   time.Now(),
			}
		}
	}()
	if snapshot == nil {
		return Response{Ready: false, Status: "error", Time: time.Now()}
	}
	return snapshot(ctx)
}
