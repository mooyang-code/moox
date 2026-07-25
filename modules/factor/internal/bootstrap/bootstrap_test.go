package bootstrap

import (
	"context"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"path/filepath"
	"testing"
	"time"
)

func TestFactorHealthSnapshot(t *testing.T) {
	cfg := Default()
	state := health.New("factor", cfg.Instance.InstanceID, "", "")
	rsp := factorHealthSnapshot(cfg, nil, nil, nil, nil, state)(context.Background())

	if rsp.Module != "factor" || rsp.Ready || rsp.Status != "degraded" {
		t.Fatalf("health response = %+v", rsp)
	}
	if rsp.Details["worker_count"] != cfg.Engine.Workers {
		t.Fatalf("worker_count = %v, want %d", rsp.Details["worker_count"], cfg.Engine.Workers)
	}
}

type factorRealtimeStatus bool

func (s factorRealtimeStatus) Ready() bool { return bool(s) }

func TestRealtimeConsumerReadinessIsRequiredOnlyWhenEnabled(t *testing.T) {
	cfg := Default()
	cfg.NATS.URLs = nil
	if !realtimeConsumerReady(cfg, nil) {
		t.Fatal("disabled realtime consumer must not block readiness")
	}
	cfg.NATS.URLs = []string{"nats://127.0.0.1:4222"}
	if realtimeConsumerReady(cfg, nil) || realtimeConsumerReady(cfg, factorRealtimeStatus(false)) {
		t.Fatal("enabled realtime consumer must be live")
	}
	if !realtimeConsumerReady(cfg, factorRealtimeStatus(true)) {
		t.Fatal("live realtime consumer must satisfy readiness")
	}
}

func TestSnapshotDir_UsesShmOrDatabaseSibling(t *testing.T) {
	cfg := Default()
	cfg.Database.Path = "/tmp/factor/data/factor.db"
	cfg.Engine.ShmDir = ""
	assert.Equal(t, filepath.Join("/tmp/factor/data", "snapshots"), snapshotDir(cfg))

	cfg.Engine.ShmDir = "/dev/shm/factor"
	assert.Equal(t, "/dev/shm/factor", snapshotDir(cfg))
}

func TestParamsFromJSON_AndFactorAuthInfo(t *testing.T) {
	got, err := paramsFromJSON(`[1,2,3]`)
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2, 3}, got)

	_, err = paramsFromJSON(`not-json`)
	require.Error(t, err)

	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "gateway-key")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "gateway-secret")
	auth := factorAuthInfo()
	require.NotNil(t, auth)
	assert.Equal(t, "moox-factor", auth.AppId)
	assert.Empty(t, auth.AppKey)
	assert.NotEqual(t, "gateway-secret", auth.AppKey)
	assert.Equal(t, "moox-factor", auth.Operator)
	assert.NotEmpty(t, auth.RequestId)
}

func TestRegisterHelpers_NilServerPaths(t *testing.T) {
	registerMetricsReporter(nil)
	require.NoError(t, registerHealth(nil, nil, nil, nil, nil, nil))

	cfg := Default()
	err := registerHealth(nil, cfg, nil, nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unavailable")
}

func TestBuildSchedulerTask_AndSyncMetadata(t *testing.T) {
	db := openBootstrapTestDB(t)
	repo := db.Factors()
	require.NoError(t, repo.Upsert(context.Background(), domain.FactorDef{
		FactorID:      "bias",
		Name:          "Bias",
		Kind:          domain.FactorKindTimeseries,
		SourceCode:    "def signal(df): return df",
		SourceHash:    "abc",
		ParamsJSON:    `[20]`,
		LookbackBars:  40,
		WritebackBars: 5,
		Status:        domain.FactorStatusEnabled,
	}))

	task, err := buildSchedulerTask(context.Background(), repo, t.TempDir(), trigger.Task{
		SpaceID:         "crypto",
		SourceDataset:   "kline",
		TargetDataset:   "kline_factor",
		SubjectID:       "BTC",
		Freq:            "1m",
		BarTime:         time.Unix(1, 0).UTC(),
		FactorIDs:       []string{"bias"},
		PendingEventIDs: []string{"message-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, "crypto", task.SpaceID)
	assert.Equal(t, 40, task.LookbackBars)
	require.Len(t, task.Factors, 1)
	assert.Equal(t, []int{20}, task.Factors[0].Params)
	assert.Equal(t, "event", task.TriggerType)
	replayed, err := buildSchedulerTask(context.Background(), repo, t.TempDir(), trigger.Task{
		SpaceID:         "crypto",
		SourceDataset:   "kline",
		TargetDataset:   "kline_factor",
		SubjectID:       "BTC",
		Freq:            "1m",
		BarTime:         time.Unix(1, 0).UTC(),
		FactorIDs:       []string{"bias"},
		PendingEventIDs: []string{"message-1"},
	})
	require.NoError(t, err)
	assert.Equal(t, task.TaskID, replayed.TaskID)

	differentEvent, err := buildSchedulerTask(context.Background(), repo, t.TempDir(), trigger.Task{
		SpaceID:         "crypto",
		SourceDataset:   "kline",
		TargetDataset:   "kline_factor",
		SubjectID:       "BTC",
		Freq:            "1m",
		BarTime:         time.Unix(1, 0).UTC(),
		FactorIDs:       []string{"bias"},
		PendingEventIDs: []string{"message-2"},
	})
	require.NoError(t, err)
	assert.NotEqual(t, task.TaskID, differentEvent.TaskID)

	_, err = buildSchedulerTask(context.Background(), repo, t.TempDir(), trigger.Task{
		FactorIDs: []string{"missing"},
	})
	require.Error(t, err)

	_, err = buildSchedulerTask(context.Background(), repo, t.TempDir(), trigger.Task{})
	require.Error(t, err)

	require.NoError(t, syncTaskMetadata(context.Background(), nil, task, repo))
	require.NoError(t, syncTaskMetadata(context.Background(), registry.NewMetadataSync(nil, nil), task, repo))
}

func TestStartRealtimeLoopAndDrainEventBatch(t *testing.T) {
	db := openBootstrapTestDB(t)
	eventBatcher := trigger.NewEventBatcher(50*time.Millisecond, nil)
	sched := scheduler.NewService(scheduler.Config{Workers: 1, MaxRetry: 1}, &bootstrapFakeStorage{}, &bootstrapFakeExecutor{})
	require.NoError(t, sched.Start(context.Background()))
	t.Cleanup(func() { _ = sched.Stop() })

	ctx, cancel := context.WithCancel(context.Background())
	wait := startRealtimeLoop(ctx, realtimeLoopDeps{
		eventBatcher:     eventBatcher,
		scheduler:        sched,
		factors:          db.Factors(),
		bindings:         db.Bindings(),
		eventBatchWindow: 50 * time.Millisecond,
		factorsDir:       t.TempDir(),
	})
	drainEventBatch(ctx, realtimeLoopDeps{
		eventBatcher: eventBatcher,
		scheduler:    sched,
		factors:      db.Factors(),
		factorsDir:   t.TempDir(),
	})
	cancel()
	wait()
}

func TestMetadataClientAdapter_DelegatesCalls(t *testing.T) {
	adapter := newMetadataClient(Default().Storage.GatewayTarget, "", gatewayauth.CredentialsFromEnv())
	require.NotNil(t, adapter)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _ = adapter.CreateFactor(ctx, &storagepb.CreateFactorReq{})
	_, _ = adapter.CreateDataset(ctx, &storagepb.CreateDatasetReq{})
	_, _ = adapter.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{})
	_, _ = adapter.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{})
	_, _ = adapter.GetFactor(ctx, &storagepb.GetFactorReq{})
	_, _ = adapter.GetDataset(ctx, &storagepb.GetDatasetReq{})
	_, _ = adapter.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{})
	_, _ = adapter.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{})
	_, _ = adapter.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{})
	_, _ = adapter.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{})
	_, _ = adapter.BindDatasetSubject(ctx, &storagepb.BindDatasetSubjectReq{})
}

func TestListEnabledBindings_EmptyStore(t *testing.T) {
	db := openBootstrapTestDB(t)
	got, err := listEnabledBindings(context.Background(), db.Bindings())
	require.NoError(t, err)
	assert.Empty(t, got)
}

func openBootstrapTestDB(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(factorschema.AllSQL()))
	return db
}

type bootstrapFakeStorage struct{}

func (bootstrapFakeStorage) ReadWindow(context.Context, storageio.WindowKey, int, time.Time, []string) (*engine.DataFrame, error) {
	return &engine.DataFrame{DataTimes: []time.Time{time.Now().UTC()}}, nil
}

func (bootstrapFakeStorage) WriteFactorPatch(context.Context, *engine.FactorTask, *engine.DataFrame, *engine.FactorResult) error {
	return nil
}

type bootstrapFakeExecutor struct{}

func (bootstrapFakeExecutor) Execute(context.Context, *engine.FactorTask, *engine.DataFrame) (*engine.FactorResult, error) {
	return &engine.FactorResult{}, nil
}

func (bootstrapFakeExecutor) Close() error { return nil }
