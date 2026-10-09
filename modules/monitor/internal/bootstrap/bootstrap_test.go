package bootstrap

import (
	"context"
	"errors"
	"os"
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
	"gopkg.in/yaml.v3"
)

func TestProbeRunnerUsesConfiguredHealthSigner(t *testing.T) {
	cfg := config.Default()
	cfg.HealthAuth = config.HealthAuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "secret"}
	runner := buildProbeRunner(cfg)
	require.NotNil(t, runner.HTTP.HealthSigner)
	assert.Equal(t, "monitor", runner.HTTP.HealthSigner.AccessKey)
}

func TestLoadMonitorDatasetHealthPolicyFallsBackToAppConfigPath(t *testing.T) {
	t.Setenv("MOOX_DATASET_HEALTH_POLICY", "")
	t.Setenv("MOOX_DATASET_HEALTH_POLICY_HASH", "")
	cfg := config.Default()
	cfg.Metrics.DatasetHealthPolicyPath = filepath.Join("..", "..", "..", "..", "config", "setup", "dataset-health-policy.yaml")
	policy, err := loadMonitorDatasetHealthPolicy(cfg)
	require.NoError(t, err)
	require.Equal(t, 2, policy.Version)
	require.Greater(t, policy.RealtimeTimeSeries.Defaults.RunMissedIntervals, 0)
}

func TestMonitorHealthSnapshotReportsClosedDatabaseAsNotReady(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatalf("open manager: %v", err)
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatalf("apply schema: %v", err)
	}
	if err := mgr.Close(); err != nil {
		t.Fatalf("close manager: %v", err)
	}
	repos := mgr.Repositories()
	runtime := &Runtime{StartedAt: time.Now(), Store: mgr, Repositories: repos, Scheduler: scheduler.New(repos, scheduler.Options{})}

	cfg := config.Default()
	cfg.Instance.InstanceID = "monitor-test"
	cfg.Metrics.Enabled = false
	rsp := monitorHealthSnapshot(cfg, runtime, nil, nil)(context.Background())
	if rsp.Ready {
		t.Fatalf("health response = %+v, want not ready", rsp)
	}
}

type collectorInventoryTestInvoke func(context.Context, string, string, any, any) error

func (f collectorInventoryTestInvoke) Invoke(ctx context.Context, service, method string, req, rsp any) error {
	return f(ctx, service, method, req, rsp)
}

func TestBuildKlineFreshnessInventoryBorrowsGateway(t *testing.T) {
	cfg := config.Default()
	cfg.KlineFreshness.Enabled = true
	gateway := collectorInventoryTestInvoke(func(context.Context, string, string, any, any) error { return nil })
	cache, err := buildKlineFreshnessInventory(cfg, gateway)
	require.NoError(t, err)
	require.NotNil(t, cache)
	_, err = buildKlineFreshnessInventory(cfg, nil)
	require.ErrorContains(t, err, "gateway client")
	disabled := *cfg
	disabled.KlineFreshness.Enabled = false
	cache, err = buildKlineFreshnessInventory(&disabled, nil)
	require.NoError(t, err)
	require.Nil(t, cache)
	invalid := *cfg
	invalid.KlineFreshness.InventoryPageSize = 101
	_, err = buildKlineFreshnessInventory(&invalid, gateway)
	require.ErrorContains(t, err, "page size")
}

func TestMaxInt(t *testing.T) {
	if maxInt(3, 7) != 7 || maxInt(9, 2) != 9 {
		t.Fatal("maxInt returned wrong value")
	}
}

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

	startObservabilityConsumer(ctx, nil, rt, nil, nil, nil)
	startObservabilityConsumer(ctx, &disabled, rt, nil, hostmetrics.NewStore(nil, nil), nil)
	cfg.Observability.Enabled = false
	startObservabilityConsumer(ctx, cfg, rt, nil, hostmetrics.NewStore(nil, nil), nil)
	cfg.Observability.Enabled = true
	startObservabilityConsumer(ctx, cfg, nil, nil, nil, nil)
	startObservabilityConsumer(ctx, cfg, &Runtime{}, nil, hostmetrics.NewStore(nil, nil), nil)

	assert.Nil(t, monitorSyncFunc(ctx, nil, &config.Config{SysDeploy: config.SysDeployConfig{Enabled: false}}, rt))
	assert.Nil(t, monitorSyncFunc(ctx, nil, nil, rt))

	registerMetricsReporter(nil, nil)
	assert.NoError(t, registerHealth(nil, nil, rt, nil, nil))
}

func TestMonitorSyncHandlerPropagatesTimerFailure(t *testing.T) {
	wantErr := errors.New("sysdeploy unavailable")
	called := 0
	handler := monitorSyncHandler(func(context.Context) (int, error) {
		called++
		return 0, wantErr
	})

	require.ErrorIs(t, handler(context.Background()), wantErr)
	assert.Equal(t, 1, called)
}

func TestSerializedMonitorSyncPreventsOverlappingRuns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	syncFunc := serializedMonitorSync(func(context.Context) (int, error) {
		close(started)
		<-release
		return 1, nil
	})
	firstDone := make(chan error, 1)
	go func() {
		_, err := syncFunc(context.Background())
		firstDone <- err
	}()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := syncFunc(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	close(release)
	require.NoError(t, <-firstDone)
}

func TestMonitorTRPCConfigDeclaresSysDeployTimer(t *testing.T) {
	raw, err := os.ReadFile("../../config/trpc_go.yaml")
	require.NoError(t, err)
	var trpcConfig struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
				Timeout  int    `yaml:"timeout"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &trpcConfig))
	for _, service := range trpcConfig.Server.Services {
		if service.Name != "trpc.moox.monitor.sysdeploy.timer" {
			continue
		}
		assert.Equal(t, "0 * * * * *", service.Network)
		assert.Equal(t, "timer", service.Protocol)
		assert.Equal(t, 30000, service.Timeout)
		return
	}
	t.Fatal("missing trpc.moox.monitor.sysdeploy.timer service")
}

func TestWaitObservabilityRespectsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	waitObservabilityRetry(ctx)
	assert.Less(t, time.Since(start), 2*time.Second)
}

func TestMonitorResultHook(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	rt := &Runtime{Store: mgr, Repositories: mgr.Repositories()}
	now := time.Now().UTC()
	require.NoError(t, rt.Repositories.Checks.Create(context.Background(), &domain.Check{
		CheckID: "check-1", Name: "Peer", Kind: domain.CheckKindHTTP, Enabled: true,
	}))
	require.NoError(t, rt.Repositories.Results.Insert(context.Background(), &domain.CheckResult{
		ResultID: "result-1", CheckID: "check-1", Status: domain.CheckStatusDown, CheckedAt: now,
	}))
	require.NoError(t, rt.Repositories.Alerts.CreateEvent(context.Background(), &domain.AlertEvent{
		EventID: "event-1", EventType: domain.AlertEventTriggered, CreatedAt: now,
	}))
	hook := monitorResultHook(rt)
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
	cfg.Metrics.HostStorage.Enabled = false
	cfg.Observability.Enabled = false
	rsp := monitorHealthSnapshot(cfg, rt, nil, nil)(context.Background())
	assert.True(t, rsp.Ready)

	cfg.Metrics.Enabled = true
	cfg.Observability.Enabled = true
	rsp = monitorHealthSnapshot(cfg, rt, nil, nil)(context.Background())
	assert.False(t, rsp.Ready)
	assert.Equal(t, "degraded", rsp.Status)

	adapter := monmetrics.NewStorageAdapter(nil, nil, cfg.Metrics.Storage)
	rsp = monitorHealthSnapshot(cfg, rt, adapter, nil)(context.Background())
	assert.False(t, rsp.Ready)
	assert.Contains(t, rsp.Details["metrics_schema_reason"], "checked")
}

func TestObservabilityWriteFailureRequiresSubsequentSuccess(t *testing.T) {
	rt := &Runtime{}
	rt.recordObservabilityWriteFailure(reasonMetricsHistory, errors.New("storage unavailable"))

	ready, _ := rt.observabilityWriteReady(time.Now().Add(24 * time.Hour))
	assert.False(t, ready)

	rt.recordObservabilityWriteSuccess()
	ready, reason := rt.observabilityWriteReady(time.Now())
	assert.True(t, ready, "failure=%d success=%d", rt.observabilityWriteFailed.Load(), rt.observabilityWriteOK.Load())
	assert.Empty(t, reason, "failure=%d success=%d", rt.observabilityWriteFailed.Load(), rt.observabilityWriteOK.Load())
}

func TestHostWriteFailureIsNotClearedByMetricsSuccess(t *testing.T) {
	rt := &Runtime{}
	rt.recordHostWriteFailure(errors.New("host storage unavailable"))
	rt.recordObservabilityWriteSuccess()

	ready, _ := rt.observabilityWriteReady(time.Now())
	assert.False(t, ready)

	rt.recordHostWriteSuccess()
	ready, reason := rt.observabilityWriteReady(time.Now())
	assert.True(t, ready, "failure=%d success=%d", rt.hostWriteFailed.Load(), rt.hostWriteOK.Load())
	assert.Empty(t, reason, "failure=%d success=%d", rt.hostWriteFailed.Load(), rt.hostWriteOK.Load())
}

func TestStoreWriteStateSequenceDoesNotRegress(t *testing.T) {
	var sequence atomic.Int64
	storeWriteStateSequence(&sequence, 2)
	storeWriteStateSequence(&sequence, 1)
	assert.EqualValues(t, 2, sequence.Load())
}

func TestMonitorHealthSnapshotRequiresHostStorageSchema(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))

	cfg := config.Default()
	cfg.Metrics.Enabled = false
	cfg.Observability.Enabled = false
	cfg.Metrics.HostStorage.Enabled = true
	rt := &Runtime{StartedAt: time.Now().UTC(), Store: mgr, Repositories: mgr.Repositories(), Scheduler: scheduler.New(mgr.Repositories(), scheduler.Options{})}
	hostStore := hostmetrics.NewStore(nil, nil)
	hostStore.SetStorageReady(func() bool { return false })

	rsp := monitorHealthSnapshot(cfg, rt, nil, hostStore)(context.Background())
	assert.False(t, rsp.Ready)
	assert.Equal(t, false, rsp.Details["host_storage_schema_ready"])
}

func TestFailureReasonNamesTheFailingStepWithoutTheRawError(t *testing.T) {
	assert.Equal(t, "metrics history write to Storage failed", failureReason(reasonMetricsHistory, errors.New("write metrics history: dial tcp 10.0.0.8:20102: connection refused")))
	assert.Equal(t, "eventbus connection unavailable: authentication failed", failureReason(reasonEventbus, errors.New("nats: Authorization Violation")))
	assert.Empty(t, failureReason(reasonEventbus, nil))

	rt := &Runtime{}
	rt.recordObservabilityWriteFailure(reasonMetricsHistory, errors.New("storage-primary unavailable"))
	ready, reason := rt.observabilityWriteReady(time.Now())
	assert.False(t, ready)
	assert.Equal(t, "metrics history write to Storage failed", reason, "a Storage outage must not be reported as an eventbus problem")
}
