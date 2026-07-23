package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	factorsvc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var factorStartedAt = time.Now()

// Initialize loads config and prepares the factor service runtime.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
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
	storageCredentials, err := gatewayauth.ResolveCredentials(cfg.Storage.KeyID, cfg.Storage.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load factor storage gateway credentials: %w", err)
	}

	authInfo := factorAuthInfo()
	factorRepo := dbm.Factors()
	bindingRepo := dbm.Bindings()
	meta := registry.NewMetadataSync(newMetadataClient(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, storageCredentials), authInfo)
	_ = registry.NewService(factorRepo, meta, registry.Options{FactorsDir: cfg.Engine.FactorsDir})

	storage := storageio.NewClientWithCredentials(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, storageCredentials, authInfo)
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
	eventBatcher := trigger.NewDurableEventBatcher(time.Duration(cfg.Scheduler.EventBatchWindowMS)*time.Millisecond, bindings, dbm)
	if err := eventBatcher.Replay(ctx); err != nil {
		log.ErrorContextf(ctx, "恢复 factor event inbox 失败: %v", err)
		return nil, err
	}
	if cfg.NATS.URL == "" {
		log.WarnContextf(ctx, "factor nats.url is empty, realtime trigger startup skipped")
	} else {
		consumer = trigger.NewNATSConsumer(trigger.NATSConfig{
			URLs:           cfg.NATS.URLs,
			URL:            cfg.NATS.URL,
			Stream:         cfg.NATS.Stream,
			Consumer:       cfg.NATS.Consumer,
			FetchMaxWait:   cfg.NATS.FetchMaxWait,
			CredentialFile: cfg.NATS.CredentialFile,
		}, eventBatcher)
		if err := consumer.Start(ctx); err != nil {
			log.ErrorContextf(ctx, "启动 factor NATS trigger 失败: %v", err)
			return nil, err
		}
		realtimeCtx, cancelRealtime := context.WithCancel(ctx)
		stopRealtime = cancelRealtime
		waitRealtime = startRealtimeLoop(realtimeCtx, realtimeLoopDeps{
			consumer:         consumer,
			eventBatcher:     eventBatcher,
			scheduler:        sched,
			factors:          factorRepo,
			meta:             meta,
			bindings:         bindingRepo,
			factorsDir:       cfg.Engine.FactorsDir,
			eventBatchWindow: time.Duration(cfg.Scheduler.EventBatchWindowMS) * time.Millisecond,
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
	h, err := report.NewHandler(report.DefaultConfig("moox_factor"))
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
		workerStatus := engine.WorkerPoolStatus{}
		if runtimeExec != nil {
			workerStatus = runtimeExec.Status()
		}
		workerReady := workerStatus.Ready && workerStatus.Workers > 0
		schedulerReady := sched != nil
		ready := databaseReady && workerReady && schedulerReady
		state.SetReady(ready)
		rsp := healthz.Base("factor", cfg.Instance.InstanceID, "", "", factorStartedAt, ready)
		rsp.Details = map[string]any{
			"database":        databaseReady,
			"worker_ready":    workerReady,
			"worker_version":  workerStatus.WorkerVersion,
			"python_version":  workerStatus.PythonVersion,
			"arrow_available": workerStatus.ArrowAvailable,
			"scheduler_ready": schedulerReady,
			"role":            cfg.Instance.Role,
			"worker_count":    cfg.Engine.Workers,
			"nats_enabled":    cfg.NATS.URL != "",
			"storage_gateway": cfg.Storage.GatewayTarget,
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
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		log.InfoContext(ctx, "factor reconcile schedule triggered")
		return sched.Drain(ctx)
	})
}

type metadataClientAdapter struct {
	client storagepb.MetadataClientProxy
}

func newMetadataClient(target, targetNode string, credentials gatewayauth.Credentials) *metadataClientAdapter {
	target = gatewayauth.ServiceGatewayTarget(storageio.NormalizeStorageTarget(target, "11003"))
	return &metadataClientAdapter{
		client: storagepb.NewMetadataClientProxy(gatewayauth.NewTRPCClientOptions(target, targetNode, credentials)...),
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

func (c *metadataClientAdapter) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return c.client.CheckDatasetActivation(ctx, req)
}

func (c *metadataClientAdapter) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return c.client.ActivateDataset(ctx, req)
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

type realtimeLoopDeps struct {
	consumer         interface{ Close() error }
	eventBatcher     *trigger.EventBatcher
	scheduler        *scheduler.Service
	factors          *store.FactorRepository
	meta             *registry.MetadataSync
	bindings         *store.BindingRepository
	eventBatchWindow time.Duration
	factorsDir       string
}

func factorAuthInfo() *commonpb.AuthInfo {
	return &commonpb.AuthInfo{
		AppId:     "moox-factor",
		Operator:  "moox-factor",
		RequestId: fmt.Sprintf("factor-%d", time.Now().UnixNano()),
	}
}

func listEnabledBindings(ctx context.Context, repo *store.BindingRepository) ([]domain.FactorBinding, error) {
	return repo.ListEnabled(ctx)
}

func startRealtimeLoop(ctx context.Context, deps realtimeLoopDeps) func() {
	flushInterval := deps.eventBatchWindow / 2
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	if flushInterval < 200*time.Millisecond {
		flushInterval = 200 * time.Millisecond
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		eventBatchTicker := time.NewTicker(flushInterval)
		bindingReloadTicker := time.NewTicker(30 * time.Second)
		defer eventBatchTicker.Stop()
		defer bindingReloadTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-eventBatchTicker.C:
				drainEventBatch(ctx, deps)
			case <-bindingReloadTicker.C:
				bindings, err := listEnabledBindings(ctx, deps.bindings)
				if err != nil {
					log.WarnContextf(ctx, "刷新 factor binding 快照失败: %v", err)
					continue
				}
				deps.eventBatcher.SetBindings(bindings)
			}
		}
	}()
	return wg.Wait
}

func drainEventBatch(ctx context.Context, deps realtimeLoopDeps) {
	tasks, err := deps.eventBatcher.FlushPending(ctx, time.Now())
	if err != nil {
		log.WarnContextf(ctx, "flush factor event inbox 失败: %v", err)
		return
	}
	for _, task := range tasks {
		schedTask, err := buildSchedulerTask(ctx, deps.factors, deps.factorsDir, task)
		if err != nil {
			log.WarnContextf(ctx, "构造 factor 调度任务失败: %v", err)
			if restoreErr := deps.eventBatcher.RestorePending(ctx); restoreErr != nil {
				log.WarnContextf(ctx, "恢复失败的 factor event inbox 失败: %v", restoreErr)
			}
			return
		}
		if err := syncTaskMetadata(ctx, deps.meta, schedTask, deps.factors); err != nil {
			log.WarnContextf(ctx, "同步 factor metadata 失败: %v", err)
			if restoreErr := deps.eventBatcher.RestorePending(ctx); restoreErr != nil {
				log.WarnContextf(ctx, "恢复失败的 factor event inbox 失败: %v", restoreErr)
			}
			return
		}
		if err := deps.scheduler.EnqueueChecked(ctx, schedTask); err != nil {
			log.WarnContextf(ctx, "factor 调度任务入队失败: %v", err)
			if restoreErr := deps.eventBatcher.RestorePending(ctx); restoreErr != nil {
				log.WarnContextf(ctx, "恢复失败的 factor event inbox 失败: %v", restoreErr)
			}
			return
		}
	}
	if err := deps.eventBatcher.CommitPending(ctx, tasks...); err != nil {
		log.WarnContextf(ctx, "提交已入队的 factor event inbox 失败: %v", err)
		if restoreErr := deps.eventBatcher.RestorePending(ctx); restoreErr != nil {
			log.WarnContextf(ctx, "恢复待提交的 factor event inbox 失败: %v", restoreErr)
		}
		return
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
			TaskID:        deterministicTaskID(task),
			FactorVersion: task.FactorVersion,
			TargetRunID:   task.TargetRunID,
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
		TriggerType: func() string {
			if task.TriggerType != "" {
				return task.TriggerType
			}
			return "event"
		}(),
		FactorIDs: append([]string(nil), task.FactorIDs...),
	}, nil
}

// deterministicTaskID makes replay and JetStream redelivery converge on the
// same logical scheduler task. The scheduler itself remains an in-memory
// queue, so this is a durable at-least-once boundary with deterministic task
// identity, not a cross-process exactly-once guarantee.
func deterministicTaskID(task trigger.Task) string {
	factorIDs := append([]string(nil), task.FactorIDs...)
	sort.Strings(factorIDs)
	pendingIDs := append([]string(nil), task.PendingEventIDs...)
	sort.Strings(pendingIDs)
	h := sha256.New()
	write := func(value string) {
		_, _ = h.Write([]byte(fmt.Sprintf("%d:%s;", len(value), value)))
	}
	for _, value := range []string{
		task.TriggerType,
		task.FactorVersion,
		task.TargetRunID,
		task.SpaceID,
		task.SourceDataset,
		task.TargetDataset,
		task.SubjectID,
		task.Freq,
		task.BarTime.UTC().Format(time.RFC3339Nano),
	} {
		write(value)
	}
	for _, factorID := range factorIDs {
		write("factor:" + factorID)
	}
	for _, messageID := range pendingIDs {
		write("message:" + messageID)
	}
	return fmt.Sprintf("ft-%x", h.Sum(nil)[:16])
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
