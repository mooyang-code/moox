package dnsresolver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnscache"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/stretchr/testify/require"
)

func TestCoordinatorPrefersTradeAndRetainsLastGoodSnapshot(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"203.0.113.2", "203.0.113.3"}, LatencyMS: map[string]uint32{"203.0.113.2": 8, "203.0.113.3": 12}},
	}}
	coordinator := NewCoordinator(nil, remote, []string{"fapi.binance.com"}, time.Minute)
	require.NoError(t, coordinator.Refresh(context.Background()))
	snapshot := coordinator.Snapshot()
	require.Equal(t, []string{"203.0.113.2", "203.0.113.3"}, snapshot["fapi.binance.com"].IPs)
	require.False(t, snapshot["fapi.binance.com"].ResolvedAt.IsZero())

	remote.err = errors.New("trade unavailable")
	coordinator.Interval = -time.Second
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.Equal(t, []string{"203.0.113.2", "203.0.113.3"}, coordinator.Snapshot()["fapi.binance.com"].IPs)
	require.Error(t, coordinator.LastError())
}

func TestCoordinatorLocalOnlyRollbackKeepsLegacyDNSPath(t *testing.T) {
	local := dnscache.New(dnscache.Config{
		Domains:         []string{"localhost"},
		RefreshInterval: time.Hour,
		ResolveTimeout:  time.Second,
	})
	coordinator := NewCoordinator(local, nil, []string{"localhost"}, time.Minute)

	require.NoError(t, coordinator.Refresh(context.Background()))
	snapshot := coordinator.Snapshot()
	require.NotEmpty(t, snapshot["localhost"].IPs)
	require.Equal(t, "local", coordinator.Status().Source)
	require.Empty(t, coordinator.LastError())
}

func TestCoordinatorMergesPartialRemoteWithLocalSnapshot(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"203.0.113.2"}},
	}}
	coordinator := NewCoordinator(nil, remote, []string{"fapi.binance.com", "api.binance.com"}, 0)
	coordinator.routes = map[string]sources.DNSResolution{
		"api.binance.com": {IPs: []string{"203.0.113.4"}},
	}
	require.NoError(t, coordinator.Refresh(context.Background()))
	got := coordinator.Snapshot()
	require.Equal(t, []string{"203.0.113.2"}, got["fapi.binance.com"].IPs)
	require.Equal(t, []string{"203.0.113.4"}, got["api.binance.com"].IPs)
}

func TestCoordinatorFallsBackWhenRemoteOmitsARequestedDomain(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}},
	}}
	coordinator := NewCoordinator(nil, remote, []string{"fapi.binance.com", "api.binance.com"}, time.Nanosecond)
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.NotEmpty(t, coordinator.Snapshot()["fapi.binance.com"])
	_, exists := coordinator.Snapshot()["api.binance.com"]
	require.False(t, exists, "an omitted remote domain must not be treated as a complete response")
}

func TestCoordinatorRecordsReceiptTimeAfterRemoteResponse(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}},
	}, delay: 20 * time.Millisecond}
	started := time.Now().UTC()
	coordinator := NewCoordinator(nil, remote, []string{"fapi.binance.com"}, time.Nanosecond)
	require.NoError(t, coordinator.Refresh(context.Background()))
	received := coordinator.Snapshot()["fapi.binance.com"].ResolvedAt
	require.GreaterOrEqual(t, received, started.Add(15*time.Millisecond))
}

func TestCoordinatorRefreshIsDueAndSerializesCalls(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{"fapi.binance.com": {IPs: []string{"203.0.113.2"}}}}
	coordinator := NewCoordinator(nil, remote, []string{"fapi.binance.com"}, time.Hour)
	require.True(t, coordinator.Due(time.Now()))
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.False(t, coordinator.Due(time.Now()))
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.Equal(t, 1, remote.calls)
}

func TestCoordinatorExpiresPreviousRouteAndAllowsLocalTakeover(t *testing.T) {
	old := time.Now().UTC().Add(-2 * time.Minute)
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}, ResolvedAt: time.Now().UTC()},
	}}
	coordinator := NewCoordinatorWithTTL(nil, remote, []string{"fapi.binance.com"}, time.Nanosecond, time.Minute)
	coordinator.routes = map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"8.8.8.8"}, ResolvedAt: old},
	}
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.Equal(t, []string{"1.1.1.1"}, coordinator.Snapshot()["fapi.binance.com"].IPs)
}

func TestCoordinatorStatusIncludesSourceHashAgeAndErrorCategory(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}},
	}}
	coordinator := NewCoordinator(nil, remote, []string{"fapi.binance.com"}, time.Nanosecond)
	require.NoError(t, coordinator.Refresh(context.Background()))
	status := coordinator.Status()
	require.Equal(t, "trade", status.Source)
	require.NotEmpty(t, status.Hash)
	require.NotEmpty(t, status.ManagedHash)
	require.Equal(t, 1, status.RouteCount)
	require.NotZero(t, status.LastRefreshAt)
	require.NotZero(t, status.LastSuccessAt)

	remote.err = errors.New("temporary gateway outage")
	coordinator.Interval = -time.Second
	require.NoError(t, coordinator.Refresh(context.Background()))
	status = coordinator.Status()
	require.Equal(t, "retained", status.Source)
	require.Equal(t, "trade_rpc", status.LastErrorCategory)
	require.Equal(t, 1, status.RouteCount)
	require.NotEmpty(t, status.Hash)
	require.NotEmpty(t, status.ManagedHash)
}

func TestCoordinatorRestoresLastGoodTradeSnapshotAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/dns_resolver_snapshot.json"
	firstRemote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}},
	}}
	first := NewCoordinatorWithMetricsAndPersistence(nil, firstRemote, []string{"fapi.binance.com"}, time.Nanosecond, time.Minute, nil, path)
	require.NoError(t, first.Refresh(context.Background()))
	require.FileExists(t, path)

	secondRemote := &fakeDomainResolver{err: errors.New("trade unavailable")}
	second := NewCoordinatorWithMetricsAndPersistence(nil, secondRemote, []string{"fapi.binance.com"}, time.Nanosecond, time.Minute, nil, path)
	require.NoError(t, second.RestoreLastGoodSnapshot())
	require.NoError(t, second.Refresh(context.Background()))
	require.Equal(t, []string{"1.1.1.1"}, second.Snapshot()["fapi.binance.com"].IPs)
	require.Equal(t, "retained", second.Status().Source)
	require.Equal(t, "trade_rpc", second.Status().LastErrorCategory)
}

func TestCoordinatorRestoreFiltersExpiredAndRemovedDomains(t *testing.T) {
	path := t.TempDir() + "/dns_resolver_snapshot.json"
	old := time.Now().UTC().Add(-2 * time.Hour)
	raw, err := json.Marshal(persistedSnapshot{SavedAt: old, Routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}, ResolvedAt: old},
		"api.binance.com":  {IPs: []string{"1.0.0.1"}, ResolvedAt: old},
	}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte(raw), 0o600))
	coordinator := NewCoordinatorWithMetricsAndPersistence(nil, nil, []string{"fapi.binance.com"}, time.Minute, time.Hour, nil, path)
	require.NoError(t, coordinator.RestoreLastGoodSnapshot())
	_, ok := coordinator.Snapshot()["fapi.binance.com"]
	require.False(t, ok, "an expired persisted route must not be restored")
	_, ok = coordinator.Snapshot()["api.binance.com"]
	require.False(t, ok, "a route removed from the current domain set must not be restored")
}

func TestCoordinatorReportsSnapshotPersistenceFailure(t *testing.T) {
	path := t.TempDir() + "/not-a-directory/snapshot.json"
	require.NoError(t, os.WriteFile(filepath.Dir(path), []byte("file"), 0o600))
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{"fapi.binance.com": {IPs: []string{"1.1.1.1"}}}}
	coordinator := NewCoordinatorWithMetricsAndPersistence(nil, remote, []string{"fapi.binance.com"}, time.Nanosecond, time.Minute, nil, path)
	err := coordinator.Refresh(context.Background())
	require.Error(t, err)
	require.Equal(t, "snapshot_persist", coordinator.Status().LastErrorCategory)
}

type fakeDomainResolver struct {
	routes map[string]sources.DNSResolution
	err    error
	calls  int
	delay  time.Duration
}

func (f *fakeDomainResolver) ResolveDomains(context.Context, []string) (map[string]sources.DNSResolution, error) {
	f.calls++
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, f.err
	}
	return cloneRoutes(f.routes), nil
}
