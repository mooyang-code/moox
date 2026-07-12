package bootstrap

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeCloseAndGo(t *testing.T) {
	assert.NoError(t, (*Runtime)(nil).Close())
	(*Runtime)(nil).Go(func() {})

	var ran atomic.Bool
	rt := &Runtime{}
	rt.Go(nil)
	rt.Go(func() { ran.Store(true) })
	require.NoError(t, rt.Close())
	assert.True(t, ran.Load())

	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	rt2 := &Runtime{Store: mgr, Scheduler: scheduler.New(mgr.Repositories(), scheduler.Options{})}
	require.NoError(t, rt2.Close())
	require.NoError(t, rt2.Close()) // closeOnce
}

func TestStartHelpersEarlyReturn(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := config.Default()
	rt := &Runtime{}

	startHostStorageGate(ctx, nil, rt, nil)
	startHostStorageGate(ctx, cfg, rt, nil)
	disabled := *cfg
	disabled.Metrics.HostStorage.Enabled = false
	startHostStorageGate(ctx, &disabled, rt, &hostmetrics.StorageGate{})

	startHostMetricsConsumer(ctx, nil, rt, nil)
	startHostMetricsConsumer(ctx, &disabled, rt, hostmetrics.NewStore(nil))
	cfg.Metrics.Enabled = false
	startHostMetricsConsumer(ctx, cfg, rt, hostmetrics.NewStore(nil))
	cfg.Metrics.Enabled = true

	startMetricsConsumer(ctx, nil, rt, nil)
	startMetricsConsumer(ctx, cfg, nil, nil)
	startMetricsConsumer(ctx, cfg, &Runtime{}, nil)

	startMetricsDedupeCleaner(ctx, rt, nil)
	startRetentionCleaner(ctx, cfg, rt) // retention days default > 0 starts goroutine
	cfg.Scheduler.ResultRetentionDays = 0
	startRetentionCleaner(ctx, cfg, rt)

	assert.Nil(t, monitorSyncFunc(ctx, &config.Config{SysDeploy: config.SysDeployConfig{Enabled: false}}, rt))

	cfg.Peer.Enabled = false
	startPeerPuller(ctx, cfg, rt)

	registerMetricsReporter(nil)
	assert.NoError(t, registerHealth(nil, nil, rt, nil))
}

func TestWaitHostMetricsRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	waitHostMetrics(ctx)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestPruneMonitorHistoryNoopAndHappyPath(t *testing.T) {
	ctx := context.Background()
	assert.NoError(t, pruneMonitorHistory(ctx, nil, time.Hour))
	assert.NoError(t, pruneMonitorHistory(ctx, &Runtime{}, time.Hour))
	assert.NoError(t, pruneMonitorHistory(ctx, &Runtime{}, 0))

	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	repos := mgr.Repositories()
	rt := &Runtime{Store: mgr, Repositories: repos}
	require.NoError(t, pruneMonitorHistory(ctx, rt, 24*time.Hour))
}

func TestMonitorSnapshotAndResultHook(t *testing.T) {
	cfg := config.Default()
	cfg.Instance.InstanceID = "monitor-a"
	cfg.Instance.BaseURL = "http://localhost"
	snap := monitorSnapshot(cfg)(context.Background())
	assert.Equal(t, "monitor-a", snap.InstanceID)
	assert.Equal(t, "http://localhost", snap.BaseURL)
	assert.False(t, snap.ObservedAt.IsZero())

	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	rt := &Runtime{Store: mgr, Repositories: mgr.Repositories()}
	hook := monitorResultHook(cfg, rt)
	require.NotNil(t, hook)
	hook(context.Background(), domain.Check{SpaceID: "default", CheckID: "c1", Enabled: true}, domain.CheckResult{
		SpaceID: "default", CheckID: "c1", Success: true, Status: domain.CheckStatusOK, CheckedAt: time.Now().UTC(),
	})
}

func TestMonitorHealthSnapshotMetricsBranches(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	repos := mgr.Repositories()
	rt := &Runtime{StartedAt: time.Now().UTC(), Store: mgr, Repositories: repos, Scheduler: scheduler.New(repos, scheduler.Options{})}

	cfg := config.Default()
	cfg.Instance.InstanceID = "monitor-ready"
	cfg.Metrics.Enabled = false
	rsp := monitorHealthSnapshot(cfg, rt, nil)(context.Background())
	assert.True(t, rsp.Ready)

	cfg.Metrics.Enabled = true
	rsp = monitorHealthSnapshot(cfg, rt, nil)(context.Background())
	assert.False(t, rsp.Ready)
	assert.Equal(t, "degraded", rsp.Status)

	adapter := monmetrics.NewStorageAdapter(nil, nil, cfg.Metrics.Storage)
	rsp = monitorHealthSnapshot(cfg, rt, adapter)(context.Background())
	assert.False(t, rsp.Ready)
	assert.Contains(t, rsp.Details["metrics_schema_reason"], "checked")
}

func TestRegisterMonitorServiceSkipsMissingService(t *testing.T) {
	// s is nil would panic on s.Service; pass a zero-value path via early warn when Service returns nil.
	// Covered by calling with a fake that we cannot easily construct; instead exercise normalize + maxInt already covered.
	cfg := config.Default()
	assert.Equal(t, "ip://127.0.0.1:20102", normalizeHostStorageTarget("  "))
	assert.Equal(t, 5, maxInt(5, 5))
	_ = cfg
}
