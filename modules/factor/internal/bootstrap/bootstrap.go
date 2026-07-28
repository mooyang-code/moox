package bootstrap

import (
	"context"
	"crypto/sha256"
	"fmt"
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
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
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
	var pythonExec *engine.PythonExecutor
	var sched *scheduler.Service
	var consumer *eventconsumer.Consumer
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
			if pythonExec != nil {
				_ = pythonExec.Close()
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
	storage := storageio.NewClientWithCredentials(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, storageCredentials, authInfo)
	pythonExec, err = engine.NewPythonExecutor(ctx, cfg.Engine.Workers, process.Config{
		PythonBin: cfg.Engine.PythonBin, WorkerPath: "./pyworker/worker.py",
		Args:        []string{"--factors-dir", cfg.Engine.FactorsDir},
		TaskTimeout: time.Duration(cfg.Engine.TaskTimeoutMS) * time.Millisecond,
		Limits:      process.DefaultLimits(),
	})
	if err != nil {
		log.ErrorContextf(ctx, "启动 factor Python worker 失败: %v", err)
		return nil, err
	}
	sched = scheduler.NewService(scheduler.Config{
		Workers: cfg.Engine.Workers, QueueCapacity: cfg.Scheduler.QueueCapacity,
		MaxRetry: cfg.Scheduler.MaxRetry,
	}, storage, pythonExec)
	if err := sched.Start(ctx); err != nil {
		return nil, fmt.Errorf("start factor scheduler: %w", err)
	}

	bindings, err := listExecutableBindings(ctx, bindingRepo)
	if err != nil {
		log.ErrorContextf(ctx, "加载 factor binding 快照失败: %v", err)
		return nil, err
	}
	eventBatcher := trigger.NewEventBatcher(time.Duration(cfg.Scheduler.EventBatchWindowMS)*time.Millisecond, bindings)
	if len(cfg.EventBus.URLs) == 0 {
		log.WarnContextf(ctx, "factor eventbus.urls is empty, realtime trigger startup skipped")
	} else {
		consumer = eventconsumer.New(eventconsumer.Config{
			URLs:           cfg.EventBus.URLs,
			FetchMaxWait:   cfg.EventBus.FetchMaxWait,
			CredentialFile: cfg.EventBus.CredentialFile,
		}, eventBatcher)
		if err := consumer.Start(ctx); err != nil {
			log.ErrorContextf(ctx, "启动 factor EventBus trigger 失败: %v", err)
			return nil, err
		}
		realtimeCtx, cancelRealtime := context.WithCancel(ctx)
		stopRealtime = cancelRealtime
		waitRealtime = startRealtimeLoop(realtimeCtx, realtimeLoopDeps{
			consumer:         consumer,
			eventBatcher:     eventBatcher,
			scheduler:        sched,
			factors:          factorRepo,
			bindings:         bindingRepo,
			factorsDir:       cfg.Engine.FactorsDir,
			eventBatchWindow: time.Duration(cfg.Scheduler.EventBatchWindowMS) * time.Millisecond,
		})
	}
	registerMetricsReporter(s)

	service := s.Service("trpc.moox.factor.FactorMgr")
	if service == nil {
		log.WarnContextf(ctx, "FactorMgr service is not configured, skip register")
	} else {
		factorpb.RegisterFactorMgrService(service, factorsvc.NewWithRuntime(
			dbm,
			sched,
			factorsvc.WithFactorsDir(cfg.Engine.FactorsDir),
			factorsvc.WithMetadataSync(meta),
		))
	}
	if err := registerHealth(s, cfg, dbm, sched, pythonExec, consumer); err != nil {
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

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("factor", "moox_factor"))
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

type realtimeStatus interface {
	Ready() bool
}

func registerHealth(s *server.Server, cfg *Config, dbm *store.Store, sched *scheduler.Service, pythonExec *engine.PythonExecutor, consumer realtimeStatus) error {
	if cfg == nil {
		return nil
	}
	state := health.New("factor", "factor-01", "", "")
	state.SnapshotFunc = factorHealthSnapshot(cfg, dbm, sched, pythonExec, consumer, state)
	if s == nil {
		return fmt.Errorf("factor health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.factor.Health"), state); err != nil {
		return fmt.Errorf("factor health server failed to start: %w", err)
	}
	return nil
}

func factorHealthSnapshot(cfg *Config, dbm *store.Store, sched *scheduler.Service, pythonExec *engine.PythonExecutor, consumer realtimeStatus, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		databaseReady := dbm != nil && dbm.Ping(ctx) == nil
		workerStatus := engine.ExecutorStatus{}
		if pythonExec != nil {
			workerStatus = pythonExec.Status()
		}
		workerReady := workerStatus.Ready && workerStatus.Workers > 0
		schedulerReady := sched != nil
		eventBusReady := realtimeConsumerReady(cfg, consumer)
		ready := databaseReady && workerReady && schedulerReady && eventBusReady
		state.SetReady(ready)
		rsp := healthz.Base("factor", "factor-01", "", "", factorStartedAt, ready)
		rsp.Details = map[string]any{
			"database":         databaseReady,
			"worker_ready":     workerReady,
			"worker_version":   workerStatus.WorkerVersion,
			"python_version":   workerStatus.PythonVersion,
			"scheduler_ready":  schedulerReady,
			"eventbus_ready":   eventBusReady,
			"worker_count":     cfg.Engine.Workers,
			"eventbus_enabled": len(cfg.EventBus.URLs) > 0,
			"storage_gateway":  cfg.Storage.GatewayTarget,
		}
		return rsp
	}
}

func realtimeConsumerReady(cfg *Config, consumer realtimeStatus) bool {
	if cfg == nil || len(cfg.EventBus.URLs) == 0 {
		return true
	}
	return consumer != nil && consumer.Ready()
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

func listExecutableBindings(ctx context.Context, repo *store.BindingRepository) ([]domain.FactorBinding, error) {
	return repo.ListExecutable(ctx)
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
		defer eventBatchTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-eventBatchTicker.C:
				bindings, err := listExecutableBindings(ctx, deps.bindings)
				if err != nil {
					log.WarnContextf(ctx, "刷新 factor executable binding 快照失败: %v", err)
					continue
				}
				deps.eventBatcher.SetBindings(bindings)
				drainEventBatch(ctx, deps)
			}
		}
	}()
	return wg.Wait
}

func drainEventBatch(ctx context.Context, deps realtimeLoopDeps) {
	tasks := deps.eventBatcher.Flush(time.Now())
	for _, task := range tasks {
		schedTask, ok, err := buildSchedulerTask(ctx, deps.factors, deps.factorsDir, task)
		if err != nil {
			log.WarnContextf(ctx, "factor realtime task lost while building: %v", err)
			continue
		}
		if !ok {
			continue
		}
		if err := deps.scheduler.Enqueue(ctx, schedTask); err != nil {
			log.WarnContextf(ctx, "factor realtime task lost while enqueueing: %v", err)
		}
	}
}

func buildSchedulerTask(ctx context.Context, repo *store.FactorRepository, factorsDir string, task trigger.Task) (scheduler.Task, bool, error) {
	factors := make([]domain.FactorDef, 0, len(task.FactorIDs))
	for _, factorID := range task.FactorIDs {
		factor, err := repo.Get(ctx, factorID)
		if err != nil {
			return scheduler.Task{}, false, fmt.Errorf("load factor %s: %w", factorID, err)
		}
		if factor.Status == domain.FactorStatusEnabled {
			factors = append(factors, *factor)
		}
	}
	if len(factors) == 0 {
		return scheduler.Task{}, false, nil
	}
	built, err := scheduler.BuildTask(scheduler.TaskScope{
		TaskID: deterministicTaskID(task), TriggerType: "event",
		SpaceID: task.SpaceID, SourceDataset: task.SourceDataset,
		TargetDataset: task.TargetDataset, SubjectID: task.SubjectID, Freq: task.Freq,
		StartTime: task.BarTime, EndTime: task.BarTime.Add(time.Nanosecond),
	}, factors, factorsDir)
	return built, err == nil, err
}

func deterministicTaskID(task trigger.Task) string {
	factorIDs := append([]string(nil), task.FactorIDs...)
	sort.Strings(factorIDs)
	h := sha256.New()
	write := func(value string) {
		_, _ = h.Write([]byte(fmt.Sprintf("%d:%s;", len(value), value)))
	}
	for _, value := range []string{
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
	return fmt.Sprintf("ft-%x", h.Sum(nil)[:16])
}
