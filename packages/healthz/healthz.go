package healthz

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

// Response is the shared process-level health payload exposed by MooX services.
type Response struct {
	Module     string         `json:"module"`
	Service    string         `json:"service,omitempty"`
	InstanceID string         `json:"instance_id,omitempty"`
	Ready      bool           `json:"ready"`
	Status     string         `json:"status"`
	Version    string         `json:"version,omitempty"`
	GitCommit  string         `json:"git_commit,omitempty"`
	StartTime  time.Time      `json:"start_time,omitempty"`
	Time       time.Time      `json:"time"`
	Details    map[string]any `json:"details,omitempty"`
}

// SnapshotFunc returns a health snapshot for the current request.
type SnapshotFunc func(context.Context) Response

// Base constructs a standard health payload with stable fields populated.
func Base(module, instanceID, version, gitCommit string, start time.Time, ready bool) Response {
	status := "degraded"
	if ready {
		status = "ok"
	}
	return Response{
		Module:     module,
		Service:    module,
		InstanceID: instanceID,
		Ready:      ready,
		Status:     status,
		Version:    version,
		GitCommit:  gitCommit,
		StartTime:  start,
		Time:       time.Now(),
	}
}

// Handler converts a SnapshotFunc into a JSON /healthz HTTP handler.
func Handler(snapshot SnapshotFunc) http.Handler {
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

// Start serves the shared health handler on addr. An empty addr disables it.
func Start(ctx context.Context, addr string, snapshot SnapshotFunc) (*http.Server, error) {
	return StartWithHandler(ctx, addr, Handler(snapshot))
}

func StartWithHandler(ctx context.Context, addr string, handler http.Handler) (*http.Server, error) {
	if addr == "" {
		return nil, nil
	}
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			_ = srv.Close()
		}
	}()
	return srv, nil
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
