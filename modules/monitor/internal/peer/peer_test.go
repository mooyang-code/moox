package peer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/healthz"
)

func TestHTTPHandlerSnapshotAuth(t *testing.T) {
	handler := NewHTTPHandler(HTTPOptions{
		Token: "secret",
		Health: healthz.Handler(func(ctx context.Context) healthz.Response {
			rsp := healthz.Base("monitor", "monitor-a", "", "", time.Now(), true)
			rsp.Details = map[string]any{"peer_count": 1, "active_peer_count": 1}
			return rsp
		}),
		Snapshot: func(ctx context.Context) Snapshot {
			return Snapshot{InstanceID: "monitor-a", BaseURL: "http://monitor-a", ObservedAt: time.Now()}
		},
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	if rsp, err := http.Get(srv.URL + "/healthz"); err != nil || rsp.StatusCode != http.StatusOK {
		t.Fatalf("health status=%v err=%v", statusOf(rsp), err)
	}
	rsp, err := http.Get(srv.URL + "/internal/monitor/v1/snapshot")
	if err != nil {
		t.Fatalf("snapshot without token: %v", err)
	}
	if rsp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("snapshot status = %d", rsp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/internal/monitor/v1/snapshot", nil)
	req.Header.Set(PeerTokenHeader, "secret")
	rsp, err = http.DefaultClient.Do(req)
	if err != nil || rsp.StatusCode != http.StatusOK {
		t.Fatalf("snapshot status=%v err=%v", statusOf(rsp), err)
	}
}

func TestHTTPHandlerRejectsSnapshotWhenTokenIsNotConfigured(t *testing.T) {
	handler := NewHTTPHandler(HTTPOptions{
		Snapshot: func(context.Context) Snapshot { return Snapshot{InstanceID: "monitor-a"} },
	})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/internal/monitor/v1/snapshot", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("snapshot status = %d, want %d", rr.Code, http.StatusUnauthorized)
	}
}

func TestPullerStoresSnapshotsAndMarksStale(t *testing.T) {
	ctx := context.Background()
	mgr := openPeerDB(t)
	repo := store.NewPeerRepository(mgr.DB())
	handler := NewHTTPHandler(HTTPOptions{
		Token: "secret",
		Snapshot: func(ctx context.Context) Snapshot {
			return Snapshot{InstanceID: "monitor-b", BaseURL: "http://monitor-b", ObservedAt: time.Now()}
		},
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	puller := NewPuller(repo, PullerOptions{
		Peers:   []Remote{{InstanceID: "monitor-b", BaseURL: srv.URL, Token: "secret"}},
		Timeout: 100 * time.Millisecond,
	})
	if err := puller.PullOnce(ctx); err != nil {
		t.Fatalf("PullOnce: %v", err)
	}
	snapshots, err := repo.ListSnapshots(ctx)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshots len=%d err=%v", len(snapshots), err)
	}
	instances, err := repo.ListInstances(ctx)
	if err != nil || len(instances) != 1 || instances[0].Status != domain.InstanceStatusActive {
		t.Fatalf("instances=%+v err=%v", instances, err)
	}
	staleBefore := time.Now().Add(-time.Second)
	if err := repo.UpsertInstance(ctx, &domain.MonitorInstance{InstanceID: "stale", Status: domain.InstanceStatusActive, LastSeenAt: &staleBefore, Snapshot: "{}"}); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}
	if err := puller.MarkStale(ctx, time.Now(), 100*time.Millisecond); err != nil {
		t.Fatalf("MarkStale: %v", err)
	}
	instances, _ = repo.ListInstances(ctx)
	remoteActive := false
	for _, item := range instances {
		if item.InstanceID == "monitor-b" && item.Status == domain.InstanceStatusActive {
			remoteActive = true
		}
		if item.InstanceID == "stale" && item.Status != domain.InstanceStatusDown {
			t.Fatalf("stale instance not marked down: %+v", item)
		}
	}
	if !remoteActive {
		t.Fatalf("fresh remote instance not active: %+v", instances)
	}
}

func TestPullerContinuesAfterPeerFailure(t *testing.T) {
	ctx := context.Background()
	mgr := openPeerDB(t)
	repo := store.NewPeerRepository(mgr.DB())
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(NewHTTPHandler(HTTPOptions{
		Token: "secret",
		Snapshot: func(ctx context.Context) Snapshot {
			return Snapshot{InstanceID: "monitor-good", BaseURL: "http://monitor-good", ObservedAt: time.Now()}
		},
	}))
	defer good.Close()

	puller := NewPuller(repo, PullerOptions{
		Peers: []Remote{
			{InstanceID: "monitor-bad", BaseURL: bad.URL},
			{InstanceID: "monitor-good", BaseURL: good.URL, Token: "secret"},
		},
		Timeout: 100 * time.Millisecond,
	})
	if err := puller.PullOnce(ctx); err == nil {
		t.Fatal("PullOnce error = nil, want partial peer failure")
	}
	instances, err := repo.ListInstances(ctx)
	if err != nil {
		t.Fatalf("list instances: %v", err)
	}
	if len(instances) != 1 || instances[0].InstanceID != "monitor-good" || instances[0].Status != domain.InstanceStatusActive {
		t.Fatalf("healthy peer was not refreshed after bad peer failed: %+v", instances)
	}
}

func statusOf(rsp *http.Response) int {
	if rsp == nil {
		return 0
	}
	return rsp.StatusCode
}

func openPeerDB(t *testing.T) *store.Manager {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	return mgr
}
