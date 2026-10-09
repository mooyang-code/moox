package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const healthService = "trpc.moox.factor.Health"

// Runtime owns moox-factor-engine's long-lived resources.
type Runtime struct {
	cfg      *Config
	identity domain.EngineIdentity
	gateway  *gatewayclient.Client
	storage  *storageio.Client
	manager  *ManagerClient
	catalog  *CatalogCache
	python   *pyexec.Pool
	pipeline *pipeline.Runner
	runner   *measuredRunner
	runs     *runTracker
	monitor  *Health
	metrics  *observability.Metrics
	locks    *setlock.Locks
	lease    *leaseState
	syncer   *catalogSyncer
	health   *factorhealth.State

	// reconcileMu serializes consumer start/stop; consumerMu only guards the
	// pointer so status and health reads never wait on EventBus I/O.
	reconcileMu sync.Mutex
	consumerMu  sync.Mutex
	consumer    *eventconsumer.Consumer

	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
	err    error
}

// Initialize assembles the engine and starts its loops. It returns after the
// first catalog sync attempt: with neither a synced nor a saved catalog the
// engine stays not ready and keeps syncing in the background.
func Initialize(ctx context.Context, s *server.Server, cfg *Config, version string) (_ *Runtime, err error) {
	if ctx == nil || s == nil || cfg == nil {
		return nil, errors.New("factor engine context, server and config are required")
	}
	if s.Service(healthService) == nil {
		return nil, fmt.Errorf("factor engine service %q is required", healthService)
	}
	bootID, err := randomHex(16)
	if err != nil {
		return nil, err
	}
	r := &Runtime{
		cfg: cfg, identity: domain.EngineIdentity{EngineID: cfg.Engine.ID, BootID: bootID, Version: version},
		runs: newRunTracker(), monitor: NewHealth(cfg.Pipeline.PeriodBudgetMax), locks: setlock.New(""),
		lease: &leaseState{}, health: factorhealth.New("factor-engine", cfg.Engine.ID, version, ""),
	}
	appCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()

	if r.gateway, err = gatewayclient.New(gatewayclient.Options{Config: cfg.GatewayClient}); err != nil {
		return nil, fmt.Errorf("创建因子引擎的 gatewayclient: %w", err)
	}
	if r.storage, err = newStorageClient(r.gateway, cfg.Storage); err != nil {
		return nil, err
	}
	r.manager = NewManagerClient(r.gateway, cfg.Manager, r.identity)
	if r.catalog, err = NewCatalogCache(cfg.Python.FactorsDir, cfg.CatalogSync.StateFile); err != nil {
		return nil, err
	}
	if loaded, loadErr := r.catalog.Load(); loadErr != nil {
		log.ErrorContextf(ctx, "factor_engine_catalog_load_failed error=%v", loadErr)
	} else if loaded {
		log.InfoContextf(ctx, "factor_engine_catalog_loaded hash=%s", r.catalog.Status().Hash)
	}
	r.python, err = pyexec.New(appCtx, cfg.Python.Workers, process.Config{
		PythonBin: cfg.Python.Bin, WorkerPath: cfg.Python.WorkerPath,
		Args: []string{"--factors-dir", cfg.Python.FactorsDir}, TaskTimeout: cfg.Python.TaskTimeout,
		Limits: process.DefaultLimits(),
	})
	if err != nil {
		return nil, fmt.Errorf("start factor Python pool: %w", err)
	}
	if r.metrics, err = observability.NewMetrics(prometheus.DefaultRegisterer); err != nil {
		return nil, fmt.Errorf("register factor pipeline metrics: %w", err)
	}
	clock := periodclock.Continuous{}
	base := pipeline.NewRunner(&observedStore{Store: r.storage, health: r.monitor}, r.python, clock, pipeline.Config{
		ReadBatchSubjects: cfg.Pipeline.ReadBatchSubjects, ReadWorkers: cfg.Pipeline.ReadWorkers,
		ReadTimeout: cfg.Pipeline.ReadTimeout, WriteBatchRows: cfg.Pipeline.WriteBatchRows,
		WriteRetryBackoff: 300 * time.Millisecond, PythonWorkers: cfg.Python.Workers, FactorsDir: cfg.Python.FactorsDir,
		BackgroundWarmup: true,
	})
	r.pipeline = base
	r.runner = &measuredRunner{inner: base, metrics: r.metrics, health: r.monitor, runs: r.runs}

	r.syncer = &catalogSyncer{
		client: r.manager, cache: r.catalog, interval: cfg.CatalogSync.Interval, offset: cfg.CatalogSync.Offset,
		onChange: r.reconcileConsumer, now: time.Now,
	}
	beat := &heartbeater{
		client: r.manager, lease: r.lease, status: r.engineStatus, interval: cfg.Engine.HeartbeatInterval,
		onChange: r.reconcileConsumer, now: time.Now,
	}
	// Heartbeat before the first catalog sync: the sync's onChange reconciles
	// the consumer, and an engine that does not hold the lease must learn so
	// before that, or it briefly consumes periods beside the lease holder.
	beatCtx, cancelBeat := context.WithTimeout(appCtx, cfg.Manager.Timeout)
	_ = beat.BeatOnce(beatCtx)
	cancelBeat()
	syncCtx, cancelSync := context.WithTimeout(appCtx, cfg.Manager.Timeout)
	_ = r.syncer.SyncOnce(syncCtx)
	cancelSync()
	r.reconcileConsumer()

	executor := recalcexec.NewExecutor(r.runner,
		recalcexec.WithChunkPeriods(cfg.Recalc.ChunkPeriods), recalcexec.WithLocks(r.locks), recalcexec.WithClock(clock))
	recalc := &recalcLoop{client: r.manager, executor: executor, source: r.storage, active: r.active, poll: cfg.Recalc.PollInterval}

	r.goRun(appCtx, r.syncer.Run)
	r.goRun(appCtx, beat.Run)
	r.goRun(appCtx, recalc.Run)
	r.goRun(appCtx, r.superviseConsumer)
	r.goRun(appCtx, r.reportMetrics)
	r.goRun(appCtx, r.pipeline.RunWarmup)

	r.health.SnapshotFunc = r.healthSnapshot
	if err := factorhealth.Register(s.Service(healthService), r.health); err != nil {
		return nil, fmt.Errorf("register factor engine health service: %w", err)
	}
	r.health.SetReady(true)
	log.InfoContextf(ctx, "factor_engine_started engine_id=%s boot_id=%s catalog_loaded=%t lease_conflict=%t",
		r.identity.EngineID, bootID, r.catalog.Loaded(), r.lease.Conflict())
	return r, nil
}

// active reports whether this engine may compute: it has a catalog and no
// other engine holds the lease.
func (r *Runtime) active() bool { return r.catalog.Loaded() && !r.lease.Conflict() }

// reconcileConsumer starts the period consumer when the engine becomes active,
// stops it on a lease conflict and refreshes its set filters otherwise.
func (r *Runtime) reconcileConsumer() {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	current := r.currentConsumer()
	switch {
	case r.active() && current == nil:
		consumer, err := eventconsumer.NewConsumer(context.Background(), eventconsumer.ConsumerConfig{
			URLs: r.cfg.EventBus.URLs, CredentialFile: r.cfg.EventBus.CredentialFile,
			FetchMaxWait: r.cfg.EventBus.FetchMaxWait, PeriodBudgetMin: r.cfg.Pipeline.PeriodBudgetMin,
			PeriodBudgetMax: r.cfg.Pipeline.PeriodBudgetMax,
		}, r.catalog, &observedStore{Store: r.storage, health: r.monitor}, r.runner, r.locks)
		if err != nil {
			log.ErrorContextf(context.Background(), "factor_engine_consumer_start_failed error=%v", err)
			return
		}
		r.setConsumer(consumer)
		log.InfoContextf(context.Background(), "factor_engine_consumer_started filters=%v", consumer.CurrentFilterSubjects())
	case !r.active() && current != nil:
		r.setConsumer(nil)
		if err := current.Close(); err != nil {
			log.ErrorContextf(context.Background(), "factor_engine_consumer_stop_failed error=%v", err)
		}
		log.WarnContextf(context.Background(), "factor_engine_consumer_stopped lease_conflict=%t", r.lease.Conflict())
	case current != nil:
		current.SetsChanged()
	}
}

// superviseConsumer retries a consumer start that failed (EventBus down).
func (r *Runtime) superviseConsumer(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.currentConsumer() == nil && r.active() {
				r.reconcileConsumer()
			}
		}
	}
}

func (r *Runtime) setConsumer(consumer *eventconsumer.Consumer) {
	r.consumerMu.Lock()
	r.consumer = consumer
	r.consumerMu.Unlock()
}

func (r *Runtime) currentConsumer() *eventconsumer.Consumer {
	r.consumerMu.Lock()
	defer r.consumerMu.Unlock()
	return r.consumer
}

func (r *Runtime) engineStatus() domain.EngineStatus {
	catalog := r.catalog.Status()
	status := domain.EngineStatus{
		PythonWorkers: int32(r.cfg.Python.Workers), RecentRuns: r.runs.all(time.Now()),
		CatalogHash: catalog.Hash, CatalogSyncedAt: catalog.SyncedAt,
	}
	if r.python != nil {
		status.PythonBusy = int32(r.python.Busy())
	}
	if consumer := r.currentConsumer(); consumer != nil {
		status.ConsumerRunning = consumer.Ready()
		for _, lane := range consumer.LaneStatuses() {
			status.Lanes = append(status.Lanes, domain.LaneStatus{SetID: lane.SetID, Queued: int32(lane.Queued), Active: lane.Active})
		}
	}
	status.Lanes = withWarmup(status.Lanes, r.catalog.SetIDs(), r.pipeline.WarmupStatuses())
	return status
}

func (r *Runtime) reportMetrics(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	knownSets := make(map[string]struct{})
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.metrics.PythonBusy.Set(float64(r.python.Busy()))
			currentSets := make(map[string]struct{})
			if consumer := r.currentConsumer(); consumer != nil {
				for _, lane := range consumer.LaneStatuses() {
					currentSets[lane.SetID] = struct{}{}
					r.metrics.LaneBacklog.WithLabelValues(lane.SetID).Set(float64(lane.Queued))
				}
			}
			for setID := range knownSets {
				if _, ok := currentSets[setID]; !ok {
					r.metrics.LaneBacklog.WithLabelValues(setID).Set(0)
				}
			}
			knownSets = currentSets
		}
	}
}

func (r *Runtime) healthSnapshot(context.Context) healthz.Response {
	catalog := r.catalog.Status()
	lease := r.lease.snapshot()
	consumer := r.currentConsumer()
	consumerReady := consumer != nil && consumer.Ready()
	pythonReady := r.python != nil && r.python.Ready()
	r.monitor.SetDependency("catalog", catalog.Loaded)
	r.monitor.SetDependency("python", pythonReady)
	r.monitor.SetDependency("eventbus", consumerReady)
	r.monitor.SetDependency("lease", !lease.Conflict)
	check := r.monitor.Check(time.Now())
	state := "ok"
	if !check.Healthy {
		state = "error"
	}
	details := map[string]any{
		"reasons": check.Reasons, "engine_id": r.identity.EngineID, "boot_id": r.identity.BootID,
		"catalog_hash": catalog.Hash, "catalog_sets": catalog.Sets, "consumer_ready": consumerReady,
		"lease_conflict": lease.Conflict,
	}
	if !catalog.SyncedAt.IsZero() {
		details["catalog_synced_at"] = catalog.SyncedAt.Format(time.RFC3339)
	}
	if !catalog.SyncErrorSince.IsZero() {
		details["catalog_sync_failing_since"] = catalog.SyncErrorSince.Format(time.RFC3339)
		details["catalog_sync_error"] = catalog.SyncError
	}
	if !lease.LastOK.IsZero() {
		details["last_heartbeat_ok_at"] = lease.LastOK.Format(time.RFC3339)
	}
	if lease.LastError != "" {
		details["heartbeat_error"] = lease.LastError
	}
	if r.python != nil {
		details["python_busy"] = r.python.Busy()
	}
	return healthz.Response{Module: "factor-engine", Ready: check.Healthy, Status: state, Time: time.Now().UTC(), Details: details}
}

func (r *Runtime) goRun(ctx context.Context, run func(context.Context)) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		run(ctx)
	}()
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.health != nil {
			r.health.SetReady(false)
		}
		if r.cancel != nil {
			r.cancel()
		}
		r.wg.Wait()
		r.reconcileMu.Lock()
		if consumer := r.currentConsumer(); consumer != nil {
			r.setConsumer(nil)
			r.err = errors.Join(r.err, consumer.Close())
		}
		r.reconcileMu.Unlock()
		if r.python != nil {
			r.err = errors.Join(r.err, r.python.Close())
		}
		if r.gateway != nil {
			r.gateway.Close()
		}
	})
	return r.err
}

func newStorageClient(gateway *gatewayclient.Client, cfg StorageConfig) (*storageio.Client, error) {
	secret := ""
	if strings.TrimSpace(cfg.AuthSecretFile) != "" {
		raw, err := os.ReadFile(cfg.AuthSecretFile)
		if err != nil {
			return nil, fmt.Errorf("read Storage auth secret: %w", err)
		}
		secret = strings.TrimSpace(string(raw))
	}
	requestID, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	// 因子引擎运行在操作员机器上，经外部接入访问 Storage。
	return storageio.NewClientWithOptions(gateway.ClientOptions(), storageio.NewAuthInfo("factor-engine-"+requestID, secret)), nil
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// withWarmup adds each enabled set's warm-up state to its lane. A set no live
// period has reached since the engine started has nothing loaded yet, so it is
// warming.
func withWarmup(lanes []domain.LaneStatus, setIDs []string, warmups []pipeline.WarmupStatus) []domain.LaneStatus {
	bySet := make(map[string]pipeline.WarmupStatus, len(warmups))
	for _, warmup := range warmups {
		bySet[warmup.SetID] = warmup
	}
	index := make(map[string]int, len(lanes))
	for i, lane := range lanes {
		index[lane.SetID] = i
	}
	for _, setID := range setIDs {
		if _, ok := index[setID]; !ok {
			index[setID] = len(lanes)
			lanes = append(lanes, domain.LaneStatus{SetID: setID})
		}
	}
	for setID, i := range index {
		warmup, ok := bySet[setID]
		if !ok {
			lanes[i].WarmupState = pipeline.WarmupWarming
			continue
		}
		lanes[i].WarmupState = warmup.State
		lanes[i].WarmSubjects = int32(warmup.WarmSubjects)
		lanes[i].ExpectedSubjects = int32(warmup.ExpectedSubjects)
	}
	return lanes
}
