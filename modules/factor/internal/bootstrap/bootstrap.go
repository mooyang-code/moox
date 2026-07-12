package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/metricspublish"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	factorsvc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var factorStartedAt = time.Now()

// Initialize loads config and prepares the factor service runtime.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	log.InfoContextf(ctx, "开始初始化 moox-factor...")

	cfg, err := Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 factor 配置失败: %v", err)
		return nil, err
	}
	dbm, err := store.Open(&store.Options{
		Path:            cfg.Database.Path,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
	})
	if err != nil {
		log.ErrorContextf(ctx, "初始化 factor 数据库失败: %v", err)
		return nil, err
	}
	keepResources := false
	var runtimeExec *engine.RuntimePoolExecutor
	var sched *scheduler.Service
	var consumer *trigger.NATSConsumer
	var stopRealtime context.CancelFunc
	var waitRealtime func()
	var cleanupOnce sync.Once
	cleanup := func() {
		cleanupOnce.Do(func() {
			if stopRealtime != nil {
				stopRealtime()
			}
			if waitRealtime != nil {
				waitRealtime()
			}
			if consumer != nil {
				_ = consumer.Close()
			}
			if sched != nil {
				_ = sched.Stop()
			}
			if runtimeExec != nil {
				_ = runtimeExec.Close()
			}
			_ = dbm.Close()
		})
	}
	defer func() {
		if !keepResources {
			cleanup()
		}
	}()
	if err := dbm.ApplySchema(factorschema.AllSQL()); err != nil {
		log.ErrorContextf(ctx, "初始化 factor schema 失败: %v", err)
		return nil, err
	}

	authInfo := factorAuthInfo(cfg)
	factorRepo := dbm.Factors()
	bindingRepo := dbm.Bindings()
	meta := registry.NewMetadataSync(newMetadataClient(cfg.Storage.MetadataTarget), authInfo)
	_ = registry.NewService(factorRepo, meta, registry.Options{FactorsDir: cfg.Engine.FactorsDir})

	storage := storageio.NewClient(cfg.Storage.AccessTarget, authInfo)
	runtimeExec, err = engine.NewRuntimePoolExecutor(ctx, cfg.Engine.Workers, process.Config{PythonBin: cfg.Engine.PythonBin, WorkerPath: "./pyworker/worker.py", Args: []string{"--factors-dir", cfg.Engine.FactorsDir, "--sections-dir", cfg.Engine.SectionsDir, "--encoding", cfg.Engine.Encoding}, TaskTimeout: time.Duration(cfg.Engine.TaskTimeoutMS) * time.Millisecond, Limits: process.DefaultLimits()})
	if err != nil {
		log.ErrorContextf(ctx, "启动 factor Python worker 失败: %v", err)
		return nil, err
	}
	sched = scheduler.NewService(scheduler.Config{
		Workers:             cfg.Engine.Workers,
		MaxRetry:            cfg.Scheduler.MaxRetry,
		BatchMinEstimatedMS: cfg.Engine.BatchMinEstimatedMS,
		SnapshotDir:         snapshotDir(cfg),
	}, storage, runtimeExec)
	if err := sched.Start(ctx); err != nil {
		return nil, fmt.Errorf("start factor scheduler: %w", err)
	}

	bindings, err := listEnabledBindings(ctx, bindingRepo)
	if err != nil {
		log.ErrorContextf(ctx, "加载 factor binding 快照失败: %v", err)
		return nil, err
	}
	debounce := trigger.NewDebouncer(time.Duration(cfg.Scheduler.DebounceWindowMS)*time.Millisecond, bindings)
	if cfg.NATS.URL == "" {
		log.WarnContextf(ctx, "factor nats.url is empty, realtime trigger startup skipped")
	} else {
		consumer = trigger.NewNATSConsumer(trigger.NATSConfig{
			URLs:           cfg.NATS.URLs,
			URL:            cfg.NATS.URL,
			Stream:         cfg.NATS.Stream,
			Consumer:       cfg.NATS.Consumer,
			Subject:        cfg.NATS.Subject,
			CredentialFile: cfg.NATS.CredentialFile,
		}, debounce)
		if err := consumer.Start(ctx); err != nil {
			log.ErrorContextf(ctx, "启动 factor NATS trigger 失败: %v", err)
			return nil, err
		}
		realtimeCtx, cancelRealtime := context.WithCancel(ctx)
		stopRealtime = cancelRealtime
		waitRealtime = startRealtimeLoop(realtimeCtx, realtimeLoopDeps{
			consumer:       consumer,
			debounce:       debounce,
			scheduler:      sched,
			factors:        factorRepo,
			meta:           meta,
			bindings:       bindingRepo,
			factorsDir:     cfg.Engine.FactorsDir,
			debounceWindow: time.Duration(cfg.Scheduler.DebounceWindowMS) * time.Millisecond,
		})
	}
	registerReconcileSchedule(s, sched)
	registerMetricsReporter(s)

	service := s.Service("trpc.moox.factor.FactorMgr")
	if service == nil {
		log.WarnContextf(ctx, "FactorMgr service is not configured, skip register")
	} else {
		factorpb.RegisterFactorMgrService(service, factorsvc.NewWithRuntime(
			dbm,
			sched,
			runtimeExec,
			factorsvc.WithFactorsDir(cfg.Engine.FactorsDir),
			factorsvc.WithMetadataSync(meta),
		))
	}
	if err := registerHealth(s, cfg, dbm, sched, runtimeExec); err != nil {
		return nil, err
	}

	keepResources = true
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			cleanup()
		}()
	}
	log.InfoContextf(ctx, "moox-factor 初始化完成")
	return s, nil
}

func snapshotDir(cfg *Config) string {
	if cfg.Engine.ShmDir != "" {
		return cfg.Engine.ShmDir
	}
	return filepath.Join(filepath.Dir(cfg.Database.Path), "snapshots")
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := metricspublish.NewHandler(metricspublish.DefaultConfig("moox_factor"))
	if err != nil {
		log.Warnf("factor metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.factor.metrics.timer")
	if service == nil {
		log.Warn("factor metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}

func registerHealth(s *server.Server, cfg *Config, dbm *store.Store, sched *scheduler.Service, runtimeExec *engine.RuntimePoolExecutor) error {
	if cfg == nil {
		return nil
	}
	state := health.New("factor", cfg.Instance.InstanceID, "", "")
	state.SnapshotFunc = factorHealthSnapshot(cfg, dbm, sched, runtimeExec, state)
	if s == nil {
		return fmt.Errorf("factor health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.factor.Health"), state); err != nil {
		return fmt.Errorf("factor health server failed to start: %w", err)
	}
	return nil
}

func factorHealthSnapshot(cfg *Config, dbm *store.Store, sched *scheduler.Service, runtimeExec *engine.RuntimePoolExecutor, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		databaseReady := dbm != nil && dbm.Ping(ctx) == nil
		workerReady := runtimeExec != nil && runtimeExec.Status().Workers > 0
		schedulerReady := sched != nil
		ready := databaseReady && workerReady && schedulerReady
		state.SetReady(ready)
		rsp := healthz.Base("factor", cfg.Instance.InstanceID, "", "", factorStartedAt, ready)
		rsp.Details = map[string]any{
			"database":        databaseReady,
			"worker_ready":    workerReady,
			"scheduler_ready": schedulerReady,
			"role":            cfg.Instance.Role,
			"worker_count":    cfg.Engine.Workers,
			"nats_enabled":    cfg.NATS.URL != "",
			"storage_access":  cfg.Storage.AccessTarget,
		}
		return rsp
	}
}

func registerReconcileSchedule(s *server.Server, sched *scheduler.Service) {
	timer.RegisterScheduler("factorReconcileSchedule", &timer.DefaultScheduler{})
	service := s.Service("trpc.moox.factor.reconcile.timer")
	if service == nil {
		log.Warn("factor reconcile timer service is not configured, skip register")
		return
	}
	if sched == nil {
		log.Warn("factor scheduler is nil, skip reconcile timer handler register")
		return
	}
	timer.RegisterHandlerService(service, func(ctx context.Context, rawParams string) error {
		log.InfoContextf(ctx, "factor reconcile schedule triggered params=%s", rawParams)
		return sched.Drain(ctx)
	})
}

type metadataClientAdapter struct {
	client storagepb.MetadataClientProxy
}

func newMetadataClient(target string) *metadataClientAdapter {
	return &metadataClientAdapter{
		client: storagepb.NewMetadataClientProxy(client.WithTarget(storageio.NormalizeStorageTarget(target, "20100"))),
	}
}

func (c *metadataClientAdapter) CreateFactor(ctx context.Context, req *storagepb.CreateFactorReq) (*storagepb.CreateFactorRsp, error) {
	return c.client.CreateFactor(ctx, req)
}

func (c *metadataClientAdapter) CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	return c.client.CreateDataset(ctx, req)
}

func (c *metadataClientAdapter) UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	return c.client.UpdateDataset(ctx, req)
}

func (c *metadataClientAdapter) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return c.client.UpsertDatasetColumn(ctx, req)
}

func (c *metadataClientAdapter) GetFactor(ctx context.Context, req *storagepb.GetFactorReq) (*storagepb.GetFactorRsp, error) {
	return c.client.GetFactor(ctx, req)
}

func (c *metadataClientAdapter) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return c.client.GetDataset(ctx, req)
}

func (c *metadataClientAdapter) ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error) {
	return c.client.ListDatasetColumns(ctx, req)
}

func (c *metadataClientAdapter) ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	return c.client.ListDatasetSubjects(ctx, req)
}

func (c *metadataClientAdapter) BindDatasetSubject(ctx context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error) {
	return c.client.BindDatasetSubject(ctx, req)
}

func (c *metadataClientAdapter) ListPrimaryStoreRoutes(ctx context.Context, req *storagepb.ListPrimaryStoreRoutesReq) (*storagepb.ListPrimaryStoreRoutesRsp, error) {
	return c.client.ListPrimaryStoreRoutes(ctx, req)
}

func (c *metadataClientAdapter) CreatePrimaryStoreRoute(ctx context.Context, req *storagepb.CreatePrimaryStoreRouteReq) (*storagepb.CreatePrimaryStoreRouteRsp, error) {
	return c.client.CreatePrimaryStoreRoute(ctx, req)
}

type realtimeLoopDeps struct {
	consumer       interface{ Close() error }
	debounce       *trigger.Debouncer
	scheduler      *scheduler.Service
	factors        *store.FactorRepository
	meta           *registry.MetadataSync
	bindings       *store.BindingRepository
	debounceWindow time.Duration
	factorsDir     string
}

func factorAuthInfo(cfg *Config) *commonpb.AuthInfo {
	return &commonpb.AuthInfo{
		AppId:     "moox-factor",
		AppKey:    cfg.SysDeploy.ServiceAuth.AccessKey,
		Operator:  "moox-factor",
		RequestId: fmt.Sprintf("factor-%d", time.Now().UnixNano()),
	}
}

func listEnabledBindings(ctx context.Context, repo *store.BindingRepository) ([]domain.FactorBinding, error) {
	return repo.ListEnabled(ctx)
}

func startRealtimeLoop(ctx context.Context, deps realtimeLoopDeps) func() {
	interval := deps.debounceWindow / 2
	if interval <= 0 {
		interval = time.Second
	}
	if interval < 200*time.Millisecond {
		interval = 200 * time.Millisecond
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		flushTicker := time.NewTicker(interval)
		reloadTicker := time.NewTicker(30 * time.Second)
		defer flushTicker.Stop()
		defer reloadTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-flushTicker.C:
				drainDebounced(ctx, deps)
			case <-reloadTicker.C:
				bindings, err := listEnabledBindings(ctx, deps.bindings)
				if err != nil {
					log.WarnContextf(ctx, "刷新 factor binding 快照失败: %v", err)
					continue
				}
				deps.debounce.SetBindings(bindings)
			}
		}
	}()
	return wg.Wait
}

func drainDebounced(ctx context.Context, deps realtimeLoopDeps) {
	for _, task := range deps.debounce.Flush(time.Now()) {
		schedTask, err := buildSchedulerTask(ctx, deps.factors, deps.factorsDir, task)
		if err != nil {
			log.WarnContextf(ctx, "构造 factor 调度任务失败: %v", err)
			continue
		}
		if err := syncTaskMetadata(ctx, deps.meta, schedTask, deps.factors); err != nil {
			log.WarnContextf(ctx, "同步 factor metadata 失败: %v", err)
			continue
		}
		deps.scheduler.Enqueue(ctx, schedTask)
	}
}

func buildSchedulerTask(ctx context.Context, repo *store.FactorRepository, factorsDir string, task trigger.Task) (scheduler.Task, error) {
	specs := make([]engine.FactorSpec, 0, len(task.FactorIDs))
	lookback := 0
	for _, factorID := range task.FactorIDs {
		factor, err := repo.Get(ctx, factorID)
		if err != nil {
			return scheduler.Task{}, fmt.Errorf("load factor %s: %w", factorID, err)
		}
		params, err := paramsFromJSON(factor.ParamsJSON)
		if err != nil {
			return scheduler.Task{}, fmt.Errorf("parse params for factor %s: %w", factor.FactorID, err)
		}
		sourcePath := factor.SourcePath
		if sourcePath == "" {
			sourcePath = filepath.Join(factorsDir, ".versions", "factor", factor.Name, factor.SourceHash, "module.py")
		}
		specs = append(specs, engine.FactorSpec{
			FactorID:      factor.FactorID,
			Name:          factor.Name,
			SourceHash:    factor.SourceHash,
			SourcePath:    sourcePath,
			EstimatedMS:   int64(factor.AvgRuntimeMS),
			Params:        params,
			WritebackBars: factor.WritebackBars,
			ExtraColumns:  registry.ExtraColumnsFromFactors([]domain.FactorDef{*factor}),
		})
		if factor.LookbackBars > lookback {
			lookback = factor.LookbackBars
		}
	}
	if len(specs) == 0 {
		return scheduler.Task{}, fmt.Errorf("no factor specs for task %s/%s/%s", task.SpaceID, task.SourceDataset, task.SubjectID)
	}
	if lookback <= 0 {
		lookback = registry.DefaultLookback(nil)
	}
	return scheduler.Task{
		FactorTask: engine.FactorTask{
			TaskID:        fmt.Sprintf("ft-%d", time.Now().UnixNano()),
			Kind:          domain.FactorKindTimeseries,
			SpaceID:       task.SpaceID,
			SourceDataset: task.SourceDataset,
			TargetDataset: task.TargetDataset,
			SubjectID:     task.SubjectID,
			Freq:          task.Freq,
			BarTime:       task.BarTime,
			LookbackBars:  lookback,
			Factors:       specs,
		},
		TriggerType: "event",
		FactorIDs:   append([]string(nil), task.FactorIDs...),
	}, nil
}

func syncTaskMetadata(ctx context.Context, meta *registry.MetadataSync, task scheduler.Task, repo *store.FactorRepository) error {
	if meta == nil {
		return nil
	}
	factors := make([]domain.FactorDef, 0, len(task.FactorIDs))
	for _, factorID := range task.FactorIDs {
		factor, err := repo.Get(ctx, factorID)
		if err != nil {
			return fmt.Errorf("load factor %s: %w", factorID, err)
		}
		if factor.Status != domain.FactorStatusEnabled {
			continue
		}
		factors = append(factors, *factor)
	}
	if len(factors) == 0 {
		return nil
	}
	targetDataset := task.TargetDataset
	if targetDataset == "" {
		targetDataset = registry.ResultDataset(task.SourceDataset)
	}
	return meta.SyncTargetDataset(ctx, task.SpaceID, task.SourceDataset, targetDataset, task.Freq, factors)
}

func paramsFromJSON(raw string) ([]int, error) {
	var params []int
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, err
	}
	return params, nil
}
