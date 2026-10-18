package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/health"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/snapshot"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/stretchr/testify/require"
)

type fakeSnapshots struct {
	cached           *snapshot.View
	loadErr, saveErr error
	saves            int
}

func (s *fakeSnapshots) Load() (*snapshot.View, error) { return s.cached, s.loadErr }
func (s *fakeSnapshots) Save(view *snapshot.View) error {
	s.saves++
	if s.saveErr == nil {
		s.cached = view
	}
	return s.saveErr
}

type fakeControl struct {
	pulled             *snapshot.View
	pullErr, reportErr error
	current, reported  string
	calls              int
}

func (c *fakeControl) Pull(_ context.Context, hash string) (*snapshot.View, error) {
	c.calls++
	c.current = hash
	return c.pulled, c.pullErr
}
func (c *fakeControl) Report(_ context.Context, hash string, _ int32, _ string) error {
	c.reported = hash
	return c.reportErr
}

func fixtureView(t *testing.T, components ...string) *snapshot.View {
	t.Helper()
	view, err := snapshot.Build("storage", testsnapshot.New(t, "storage", components...))
	require.NoError(t, err)
	return view
}

func TestCachedStartupAndFreshSnapshotReadiness(t *testing.T) {
	cache := &fakeSnapshots{cached: fixtureView(t, "storage-primary")}
	control := &fakeControl{pullErr: errors.New("offline")}
	h := health.NewState()
	r := New(Options{HostID: "storage", Snapshots: cache, Control: control, Health: h})
	require.NoError(t, r.Initialize(context.Background()))
	require.Equal(t, cache.cached.Hash(), control.current)
	require.Equal(t, cache.cached.Hash(), r.State().Load().Hash())
	require.False(t, h.Ready())
	control.pullErr = nil
	require.NoError(t, r.Refresh(context.Background()))
	require.True(t, h.Ready())
	require.Zero(t, cache.saves, "unchanged snapshots do not rewrite the cache")
	control.pullErr = snapshot.ErrInvalid
	require.Error(t, r.Refresh(context.Background()))
	require.Equal(t, cache.cached.Hash(), r.State().Load().Hash())
}

func TestPersistenceFailureAppliesRevocationAndRetriesUnchangedView(t *testing.T) {
	cache := &fakeSnapshots{cached: fixtureView(t, "storage-primary")}
	control := &fakeControl{}
	r := New(Options{HostID: "storage", Snapshots: cache, Control: control})
	require.NoError(t, r.Initialize(context.Background()))
	replacement := fixtureView(t)
	control.pulled = replacement
	cache.saveErr = errors.New("disk full")
	require.Error(t, r.Refresh(context.Background()))
	_, found := r.State().Load().Resolve("trpc.moox.storage.PrimaryStore", "ReadTimeSeriesRows")
	require.False(t, found, "a cache failure must not preserve withdrawn permissions")
	require.Equal(t, replacement.Hash(), control.reported)
	require.NotEqual(t, replacement.Hash(), cache.cached.Hash())
	control.pulled, cache.saveErr = nil, nil
	require.NoError(t, r.Refresh(context.Background()))
	require.Equal(t, replacement.Hash(), cache.cached.Hash())
	require.Equal(t, 2, cache.saves)
}

func TestStartupWithoutSnapshotFailsAndCancellationIsRespected(t *testing.T) {
	for _, pullErr := range []error{nil, errors.New("offline"), snapshot.ErrInvalid} {
		control := &fakeControl{pullErr: pullErr}
		r := New(Options{HostID: "storage", Snapshots: &fakeSnapshots{loadErr: errors.New("missing")}, Control: control})
		require.Error(t, r.Initialize(context.Background()))
	}
	control := &fakeControl{}
	r := New(Options{HostID: "storage", Snapshots: &fakeSnapshots{cached: fixtureView(t)}, Control: control})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, r.Initialize(ctx), context.Canceled)
	require.Zero(t, control.calls)
}

func TestHeartbeatFailureAndContinuousSyncWarning(t *testing.T) {
	now := time.Now()
	control := &fakeControl{pulled: fixtureView(t), reportErr: errors.New("heartbeat failed")}
	warnings := []string{}
	h := health.NewState()
	r := New(Options{HostID: "storage", Snapshots: &fakeSnapshots{}, Control: control, Health: h, Now: func() time.Time { return now }, Warn: func(s string) { warnings = append(warnings, s) }})
	require.NoError(t, r.Initialize(context.Background()))
	require.False(t, h.Ready())
	control.pulled, control.reportErr = nil, nil
	require.NoError(t, r.Refresh(context.Background()))
	require.True(t, h.Ready())
	control.pullErr = errors.New("offline")
	require.Error(t, r.Refresh(context.Background()))
	now = now.Add(91 * time.Second)
	require.False(t, h.Ready())
	now = now.Add(9 * time.Minute)
	require.Error(t, r.Refresh(context.Background()))
	require.Len(t, warnings, 2)
	require.Error(t, r.Refresh(context.Background()))
	require.Len(t, warnings, 2)
	control.pullErr = nil
	require.NoError(t, r.Refresh(context.Background()))
	control.pullErr = errors.New("offline")
	require.Error(t, r.Refresh(context.Background()))
	require.Len(t, warnings, 2)
}

func TestHealthAuthenticationAndServerTimeouts(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "fixture")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "synthetic-health-secret")
	handler, err := authenticatedHealthHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	require.NoError(t, err)
	rsp := httptest.NewRecorder()
	handler.ServeHTTP(rsp, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, http.StatusUnauthorized, rsp.Code)
	s := newHealthHTTPServer(handler)
	require.Positive(t, s.ReadHeaderTimeout)
	require.Positive(t, s.ReadTimeout)
	require.Positive(t, s.WriteTimeout)
	require.Positive(t, s.IdleTimeout)
}
