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

func TestCoordinatorPrefersEgressAndRetainsLastGoodSnapshot(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"203.0.113.2", "203.0.113.3"}, LatencyMS: map[string]uint32{"203.0.113.2": 8, "203.0.113.3": 12}},
	}}
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Minute})
	require.NoError(t, coordinator.Refresh(context.Background()))
	snapshot := coordinator.Snapshot()
	require.Equal(t, []string{"203.0.113.2", "203.0.113.3"}, snapshot["fapi.binance.com"].IPs)
	require.False(t, snapshot["fapi.binance.com"].ResolvedAt.IsZero())

	remote.err = errors.New("egress unavailable")
	coordinator.Interval = -time.Second
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.Equal(t, []string{"203.0.113.2", "203.0.113.3"}, coordinator.Snapshot()["fapi.binance.com"].IPs)
	require.Error(t, coordinator.LastError())
}

func TestCoordinatorLocalOnlyUsesLocalDNS(t *testing.T) {
	local := dnscache.New(dnscache.Config{
		Domains:         []string{"localhost"},
		RefreshInterval: time.Hour,
		ResolveTimeout:  time.Second,
	})
	coordinator := NewCoordinator(CoordinatorConfig{Local: local, Domains: []string{"localhost"}, Interval: time.Minute})

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
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com", "api.binance.com"}, RemoteDomains: []string{"fapi.binance.com", "api.binance.com"}})
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
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com", "api.binance.com"}, RemoteDomains: []string{"fapi.binance.com", "api.binance.com"}, Interval: time.Nanosecond})
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
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Nanosecond})
	require.NoError(t, coordinator.Refresh(context.Background()))
	received := coordinator.Snapshot()["fapi.binance.com"].ResolvedAt
	require.GreaterOrEqual(t, received, started.Add(15*time.Millisecond))
}

func TestCoordinatorRefreshIsDueAndSerializesCalls(t *testing.T) {
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{"fapi.binance.com": {IPs: []string{"203.0.113.2"}}}}
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Hour})
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
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Nanosecond, CacheTTL: time.Minute})
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
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Nanosecond})
	require.NoError(t, coordinator.Refresh(context.Background()))
	status := coordinator.Status()
	require.Equal(t, "egress", status.Source)
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
	require.Equal(t, "egress_rpc", status.LastErrorCategory)
	require.Equal(t, 1, status.RouteCount)
	require.NotEmpty(t, status.Hash)
	require.NotEmpty(t, status.ManagedHash)
}

func TestCoordinatorRestoresLastGoodEgressSnapshotAcrossRestart(t *testing.T) {
	path := t.TempDir() + "/dns_resolver_snapshot.json"
	firstRemote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{
		"fapi.binance.com": {IPs: []string{"1.1.1.1"}},
	}}
	first := NewCoordinator(CoordinatorConfig{Remote: firstRemote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Nanosecond, CacheTTL: time.Minute, PersistencePath: path})
	require.NoError(t, first.Refresh(context.Background()))
	require.FileExists(t, path)

	secondRemote := &fakeDomainResolver{err: errors.New("egress unavailable")}
	second := NewCoordinator(CoordinatorConfig{Remote: secondRemote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Nanosecond, CacheTTL: time.Minute, PersistencePath: path})
	require.NoError(t, second.RestoreLastGoodSnapshot())
	require.NoError(t, second.Refresh(context.Background()))
	require.Equal(t, []string{"1.1.1.1"}, second.Snapshot()["fapi.binance.com"].IPs)
	require.Equal(t, "retained", second.Status().Source)
	require.Equal(t, "egress_rpc", second.Status().LastErrorCategory)
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
	coordinator := NewCoordinator(CoordinatorConfig{Domains: []string{"fapi.binance.com"}, Interval: time.Minute, CacheTTL: time.Hour, PersistencePath: path})
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
	coordinator := NewCoordinator(CoordinatorConfig{Remote: remote, Domains: []string{"fapi.binance.com"}, RemoteDomains: []string{"fapi.binance.com"}, Interval: time.Nanosecond, CacheTTL: time.Minute, PersistencePath: path})
	err := coordinator.Refresh(context.Background())
	require.Error(t, err)
	require.Equal(t, "snapshot_persist", coordinator.Status().LastErrorCategory)
}

type fakeDomainResolver struct {
	routes    map[string]sources.DNSResolution
	err       error
	calls     int
	delay     time.Duration
	requested []string
}

func (f *fakeDomainResolver) ResolveDomains(_ context.Context, domains []string) (map[string]sources.DNSResolution, error) {
	f.calls++
	f.requested = append([]string(nil), domains...)
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	if f.err != nil {
		return nil, f.err
	}
	return cloneRoutes(f.routes), nil
}

func TestCoordinatorRequestsOnlyRemoteAllowlistAndKeepsLocalDomains(t *testing.T) {
	local := dnscache.New(dnscache.Config{Domains: []string{"localhost"}, ResolveTimeout: time.Second})
	remote := &fakeDomainResolver{routes: map[string]sources.DNSResolution{"api.binance.com": {IPs: []string{"8.8.8.8"}}}}
	coordinator := NewCoordinator(CoordinatorConfig{Local: local, Remote: remote, Domains: []string{"localhost", "api.binance.com"}, RemoteDomains: []string{"api.binance.com"}})
	require.NoError(t, coordinator.Refresh(context.Background()))
	require.Equal(t, []string{"api.binance.com"}, remote.requested)
	require.NotEmpty(t, coordinator.Snapshot()["localhost"].IPs)
	require.Equal(t, []string{"8.8.8.8"}, coordinator.Snapshot()["api.binance.com"].IPs)
	require.Equal(t, "hybrid", coordinator.Status().Source)
}
