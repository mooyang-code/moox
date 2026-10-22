package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
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
	pipeline *pipeline.Runner
	runner   *measuredRunner
	runs     *runTracker
	monitor  *Health
	metrics  *observability.Metrics
	locks    *setlock.Locks
	gap      *gapledger.Ledger
	// continuity 维护各集合的连续水位；advanceKick 唤醒追赶循环（补齐通知）。
	continuity  *eventconsumer.Continuity
	advanceKick chan string
	// appCtx 是引擎运行期的上下文；catchups 记录恢复追赶中各集合的本地数据同步进展。
	appCtx   context.Context
	catchups catchupTracker
	filled   *filledPeriods
	lease    *leaseState
	syncer   *catalogSyncer
	preload  *preloader
	series   *seriesstore.Store
	data     *localdata.Data
	admit    *memadmit.Admitter
	health   *factorhealth.State
	life     *lifecycle.Controller
	replays  *replayManager

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
	r.appCtx = appCtx
	r.cancel = cancel
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()

	if r.gateway, err = cfg.GatewayClient.OpenExternal(cfg.sourcePath); err != nil {
		return nil, err
	}
	if r.storage, err = newStorageClient(r.gateway, cfg.Storage); err != nil {
		return nil, err
	}
	if r.manager, err = NewManagerClient(r.gateway, cfg.Manager.Timeout, r.identity); err != nil {
		return nil, err
	}
	if r.catalog, err = NewCatalogCache(cfg.CatalogSync.StateFile); err != nil {
		return nil, err
	}
	if loaded, loadErr := r.catalog.Load(); loadErr != nil {
		log.ErrorContextf(ctx, "factor_engine_catalog_load_failed error=%v", loadErr)
	} else if loaded {
		log.InfoContextf(ctx, "factor_engine_catalog_loaded hash=%s", r.catalog.Status().Hash)
	}
	if r.metrics, err = observability.NewMetrics(prometheus.DefaultRegisterer); err != nil {
		return nil, fmt.Errorf("register factor pipeline metrics: %w", err)
	}
	clock := periodclock.Continuous{}
	// 本地时序存储与内存窗口（设计第 3、4 节）：窗口与块缓存是常驻内存，先从进程预算里扣除，计算再向准入器预留。
	r.admit = memadmit.New(int64(cfg.Memory.TotalBudget))
	r.admit.SetResident("seriesstore_block_cache", int64(cfg.SeriesStore.BlockCache))
	if r.series, err = seriesstore.Open(seriesstore.Options{Dir: cfg.SeriesStore.Dir, BlockCacheBytes: int64(cfg.SeriesStore.BlockCache)}); err != nil {
		return nil, fmt.Errorf("open factor series store: %w", err)
	}
	r.data = localdata.New(localdata.Config{
		Store: r.series, Storage: r.storage, Clock: clock, Admit: r.admit,
		Workers: cfg.Sync.Workers, BatchSubjects: cfg.Sync.BatchSubjects, Headroom: cfg.Window.Headroom, WindowMax: int64(cfg.Window.MaxMemory),
	})
	pipelineConfig := pipeline.Config{
		WriteBatchRows: cfg.Pipeline.WriteBatchRows, WriteRetryBackoff: 300 * time.Millisecond,
		ComputeWorkers: cfg.Compute.Workers, MaxMemory: int64(cfg.Compute.MaxMemory),
		OnCompileError:  func(setID, factorID string) { r.metrics.CompileErrors.WithLabelValues(setID, factorID).Inc() },
		MemoryAdmission: r.admit, AdmissionTimeout: cfg.Pipeline.PeriodBudgetMax,
	}
	// 跳过账本与恢复始终开启：预热中被跳过的周期也靠它补算。gap.enabled 只控制回看窗口的空洞检查。
	ledger, expired, err := gapledger.Open(cfg.Gap.LedgerDir, time.Now, cfg.Gap.GiveUpAfter)
	if err != nil {
		return nil, fmt.Errorf("open factor gap ledger: %w", err)
	}
	r.gap = ledger
	logExpired(expired, cfg.Gap.GiveUpAfter)
	r.metrics.GapGaveUp.Add(float64(len(expired)))
	log.InfoContextf(ctx, "factor_gap_ledger_opened dir=%s pending=%d ignored_lines=%d expired=%d", cfg.Gap.LedgerDir, ledger.PendingCount(), ledger.Ignored(), len(expired))
	// 记入预热中跳过的项、或者一批预热完成时唤醒跳过恢复：集合预热已就绪时这些项不用再等 Settle。
	gapKick := make(chan struct{}, 1)
	kickGap := func() {
		select {
		case gapKick <- struct{}{}:
		default:
		}
	}
	pipelineConfig.GapSink = warmingKickSink{GapSink: ledger, kick: kickGap}
	watermarks, err := watermark.Open(cfg.Watermark.StateFile)
	if err != nil {
		return nil, fmt.Errorf("open factor watermarks: %w", err)
	}
	if r.continuity, err = eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{
		Store: watermarks, Reader: r.storage, MaxMissingRatio: cfg.Watermark.MaxMissingRatio, SetRatios: cfg.Watermark.SetRatios, MaxPeriods: cfg.Watermark.MaxPeriods,
	}); err != nil {
		return nil, err
	}
	r.advanceKick, r.filled = make(chan string, 64), newFilledPeriods(time.Now)
	r.manager.OnFilled = func(filled []*factorpb.PeriodsFilled) { r.onFilled(filled, kickGap) }
	base := pipeline.NewRunner(&observedStore{Store: r.storage, health: r.monitor}, r.data, clock, pipelineConfig)
	r.pipeline = base
	r.runner = &measuredRunner{inner: base, metrics: r.metrics, health: r.monitor, runs: r.runs}
	r.preload = newPreloader(r.catalog, r.storage, r.data, r.active, func(string) { kickGap() })
	if r.life, err = r.newLifecycle(); err != nil {
		return nil, fmt.Errorf("open factor lifecycle: %w", err)
	}

	r.syncer = &catalogSyncer{
		client: r.manager, cache: r.catalog, interval: cfg.CatalogSync.Interval, offset: cfg.CatalogSync.Offset,
		onChange: r.reconcileConsumer, now: time.Now,
	}
	beat := &heartbeater{
		client: r.manager, lease: r.lease, status: r.engineStatus, interval: cfg.Engine.HeartbeatInterval,
		onChange: r.reconcileConsumer, now: time.Now, wake: make(chan struct{}, 1),
	}
	// 每算完一期立刻补发心跳：因子管理服务据此马上更新数据集指标并补报，Monitor 约 1 秒内看到这一期的完成时刻。
	r.runs.setOnRecord(beat.Wake)
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
		recalcexec.WithChunkPeriods(cfg.Recalc.ChunkPeriods), recalcexec.WithLocks(r.locks), recalcexec.WithClock(clock),
		recalcexec.WithMemoryBudget(int64(cfg.Compute.MaxMemory)), recalcexec.WithGate(r.writeGate))
	recalc := &recalcLoop{client: r.manager, executor: executor, source: r.storage, active: r.active, poll: cfg.Recalc.PollInterval}
	filler := &gapFiller{
		ledger: r.gap, sets: r.catalog, source: r.storage, exec: executor, universe: r.storage, clock: clock,
		active: r.active, cfg: cfg.Gap, now: time.Now, onGaveUp: func(n int) { r.metrics.GapGaveUp.Add(float64(n)) },
		kick: gapKick, warmup: r.pipeline.WarmupStatuses, filled: r.filled.covers,
		// 补齐的最新一期记入运行摘要（并立刻补发心跳）：预热中跳过的那一期补算后，因子管理服务马上把它记为完成。
		onRepaired: func(setID string, period RepairedPeriod) {
			if r.runs.repair(setID, period, time.Now()) {
				log.Infof("factor_period_repaired set_id=%s period_time=%s", setID, period.Period.UTC().Format(time.RFC3339))
			}
		},
	}
	follower := &seriesFollower{
		data: r.data, sets: r.catalog, source: r.storage, exec: executor, periods: committedThrough{r.continuity},
		active: r.active, interval: cfg.Sync.FollowInterval, chunk: cfg.Recalc.ChunkPeriods, now: time.Now,
		onGapped: func(seriesstore.Source) { r.metrics.SeriesChangeGaps.Inc() }, blocked: map[seriesstore.Source]bool{},
	}

	r.goRun(appCtx, filler.Run)
	r.goRun(appCtx, r.runCatchup)
	r.goRun(appCtx, r.syncer.Run)
	r.goRun(appCtx, beat.Run)
	r.goRun(appCtx, recalc.Run)
	r.goRun(appCtx, r.superviseConsumer)
	r.goRun(appCtx, r.reportMetrics)
	r.goRun(appCtx, r.preload.Run)
	r.goRun(appCtx, follower.Run)
	r.replays = newReplayManager(ReplayDeps{
		Sets: r.catalog, Source: r.storage, Columns: r.storage, Store: r.series, Runner: r.pipeline, Admit: r.admit,
		BatchOK: func() int { return cfg.Pipeline.WriteBatchRows },
	})
	r.goRun(appCtx, r.replays.Run)
	r.goRun(appCtx, func(ctx context.Context) {
		r.life.Recover(ctx)
		r.life.Run(ctx, cfg.Lifecycle.Interval)
	})

	r.health.SnapshotFunc = r.healthSnapshot
	if err := factorhealth.Register(s.Service(healthService), r.health, append(append(newLocalWriter(r.data, r.storage).routes(), r.lifecycleRoutes()...), r.replays.routes()...)...); err != nil {
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
// stops it on a lease conflict and refreshes its set filters otherwise. While
// active it also kicks the preloader, so a new lease holder or a newly enabled
// set starts loading its window before its first live period.
func (r *Runtime) reconcileConsumer() {
	r.reconcileMu.Lock()
	defer r.reconcileMu.Unlock()
	if r.active() {
		r.preload.Kick()
	}
	current := r.currentConsumer()
	switch {
	case r.active() && current == nil:
		consumer, err := eventconsumer.NewConsumer(context.Background(), eventconsumer.ConsumerConfig{
			URLs: r.cfg.EventBus.URLs, CredentialFile: r.cfg.EventBus.CredentialFile,
			FetchMaxWait: r.cfg.EventBus.FetchMaxWait, PeriodBudgetMin: r.cfg.Pipeline.PeriodBudgetMin,
			PeriodBudgetMax: r.cfg.Pipeline.PeriodBudgetMax, Continuity: r.continuity, Gate: r.gate, CatchupGate: r.catchupGate,
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
		ComputeWorkers: int32(r.cfg.Compute.Workers), ExprVersion: factorexpr.ExprVersion, RecentRuns: r.runs.all(time.Now()),
		CatalogHash: catalog.Hash, CatalogSyncedAt: catalog.SyncedAt,
	}
	if consumer := r.currentConsumer(); consumer != nil {
		status.ConsumerRunning = consumer.Ready()
		for _, lane := range consumer.LaneStatuses() {
			status.Lanes = append(status.Lanes, domain.LaneStatus{SetID: lane.SetID, Queued: int32(lane.Queued), Active: lane.Active})
		}
	}
	status.Lanes = withWarmup(status.Lanes, r.catalog.SetIDs(), r.pipeline.WarmupStatuses())
	if r.continuity != nil {
		for _, state := range r.continuity.States() {
			status.Watermarks = append(status.Watermarks, domain.SetWatermark{SetID: state.SetID, CommittedThrough: state.CommittedThrough, Target: state.Target,
				BlockedSince: state.BlockedSince, BlockedPeriod: state.BlockedPeriod, BlockedReason: state.BlockedReason})
		}
	}
	return status
}

func (r *Runtime) reportMetrics(ctx context.Context) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	knownSets := make(map[string]struct{})
	var gapSkipped int64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.reportGapMetrics(&gapSkipped)
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

// reportGapMetrics 把账本的待恢复数、累计跳过数与写盘状态同步到指标。
func (r *Runtime) reportGapMetrics(lastSkipped *int64) {
	r.metrics.GapPending.Set(float64(r.gap.PendingCount()))
	if total := r.gap.SkippedTotal(); total > *lastSkipped {
		r.metrics.GapSkipped.Add(float64(total - *lastSkipped))
		*lastSkipped = total
	}
	degraded := 0.0
	if r.gap.Degraded() {
		degraded = 1
	}
	r.metrics.GapLedgerDegraded.Set(degraded)
}

func (r *Runtime) healthSnapshot(context.Context) healthz.Response {
	catalog := r.catalog.Status()
	lease := r.lease.snapshot()
	consumer := r.currentConsumer()
	consumerReady := consumer != nil && consumer.Ready()
	r.monitor.SetDependency("catalog", catalog.Loaded)
	r.monitor.SetDependency("eventbus", consumerReady)
	r.monitor.SetDependency("lease", !lease.Conflict)
	// 账本写盘失败：账本可能不完整，健康检查降级（周期推进不受影响）。
	r.monitor.SetDependency("gap_ledger", !r.gap.Degraded())
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
	details["gap_pending"] = r.gap.PendingCount()
	details["gap_ledger_degraded"] = r.gap.Degraded()
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
	if r.life != nil {
		details["lifecycle"] = r.life.Compact()
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
		if r.gap != nil {
			r.err = errors.Join(r.err, r.gap.Close())
		}
		if r.series != nil {
			r.err = errors.Join(r.err, r.series.Close())
		}
		if r.gateway != nil {
			r.err = errors.Join(r.err, r.gateway.Close())
		}
	})
	return r.err
}

func newStorageClient(gateway gatewayclient.Invoker, cfg StorageConfig) (*storageio.Client, error) {
	secret, err := gatewayauth.ReadSigningSecret(cfg.AuthSecretFile)
	if err != nil {
		return nil, fmt.Errorf("read Storage auth secret: %w", err)
	}
	requestID, err := randomHex(8)
	if err != nil {
		return nil, err
	}
	return storageio.NewGatewayClient(gateway, storageio.NewAuthInfo("factor-engine-"+requestID, secret)), nil
}

func randomHex(n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random id: %w", err)
	}
	return hex.EncodeToString(raw), nil
}

// withWarmup 给每个启用因子集的 lane 补上预热状态。只上报目录里启用的因子集：
// 消费者订阅的是整个采集周期事件族，任何数据集（例如还没有建因子集的 1h K 线）的事件都会让处理器按
// “数据集+频率”建一条 lane，再去查有没有启用的因子集；没有因子集的 lane 不是计算任务，不能上报，
// 否则它们会被标成 warming，永远等不到首个周期。
// 目录里的因子集自引擎启动以来还没有周期到达，说明还没有载入任何数据，同样是 warming。
func withWarmup(lanes []domain.LaneStatus, setIDs []string, warmups []pipeline.WarmupStatus) []domain.LaneStatus {
	bySet := make(map[string]pipeline.WarmupStatus, len(warmups))
	for _, warmup := range warmups {
		bySet[warmup.SetID] = warmup
	}
	enabled := make(map[string]struct{}, len(setIDs))
	for _, setID := range setIDs {
		enabled[setID] = struct{}{}
	}
	kept := make([]domain.LaneStatus, 0, len(enabled))
	index := make(map[string]int, len(enabled))
	for _, lane := range lanes {
		if _, ok := enabled[lane.SetID]; !ok {
			continue
		}
		index[lane.SetID] = len(kept)
		kept = append(kept, lane)
	}
	for _, setID := range setIDs {
		if _, ok := index[setID]; !ok {
			index[setID] = len(kept)
			kept = append(kept, domain.LaneStatus{SetID: setID})
		}
	}
	for setID, i := range index {
		warmup, ok := bySet[setID]
		if !ok {
			kept[i].WarmupState = pipeline.WarmupWarming
			continue
		}
		kept[i].WarmupState = warmup.State
		kept[i].WarmSubjects = int32(warmup.WarmSubjects)
		kept[i].ExpectedSubjects = int32(warmup.ExpectedSubjects)
	}
	return kept
}

// committedThrough 把连续水位适配成失效重算需要的「已经算过的最后一期」。
type committedThrough struct{ c *eventconsumer.Continuity }

func (c committedThrough) CommittedThrough(setID string) (time.Time, bool) {
	for _, state := range c.c.States() {
		if state.SetID == setID && !state.CommittedThrough.IsZero() {
			return state.CommittedThrough, true
		}
	}
	return time.Time{}, false
}
