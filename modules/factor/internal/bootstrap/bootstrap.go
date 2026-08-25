package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	factorobservability "github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	factorsvc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
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
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/prometheus/client_golang/prometheus"
	"gorm.io/gorm"
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
	var pythonPool *engine.PythonWorkerPool
	var runner *taskrunner.Service
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
			if pythonPool != nil {
				_ = pythonPool.Close()
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
	startupRegistry := registry.NewService(
		factorRepo,
		meta,
		registry.Options{FactorsDir: cfg.Engine.FactorsDir},
	).WithBindings(bindingRepo)
	if err := startupRegistry.EnsureSourceArtifacts(ctx); err != nil {
		return nil, fmt.Errorf("restore factor source artifacts: %w", err)
	}
	if err := validateStartupFactorContracts(ctx, startupRegistry); err != nil {
		return nil, err
	}
	manifests := dbm.OutputManifests()
	storage := storageio.NewClientWithCredentials(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, storageCredentials, authInfo).
		WithViewAuth(factorViewAuthInfo()).
		WithOutputManifests(manifests)
	pythonPool, err = engine.NewPythonWorkerPool(ctx, cfg.Engine.PythonWorkers, process.Config{
		PythonBin: cfg.Engine.PythonBin, WorkerPath: cfg.Engine.WorkerPath,
		Args:        []string{"--factors-dir", cfg.Engine.FactorsDir},
		TaskTimeout: time.Duration(cfg.Engine.TaskTimeoutMS) * time.Millisecond,
		Limits:      process.DefaultLimits(),
	})
	if err != nil {
		log.ErrorContextf(ctx, "启动 factor Python worker 失败: %v", err)
		return nil, err
	}
	datasetMetrics, err := factorobservability.NewDatasetMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return nil, fmt.Errorf("initialize factor dataset metrics: %w", err)
	}
	moduleMetrics, err := report.NewModuleMetrics(
		prometheus.DefaultRegisterer,
		"factor",
		report.HealthCheckIDsForModule("factor"),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize factor module metrics: %w", err)
	}
	runMetrics, err := report.NewDatasetModuleObserver(
		datasetMetrics,
		moduleMetrics,
		"calculate",
		"factor-calculation",
	)
	if err != nil {
		return nil, fmt.Errorf("initialize factor run metrics: %w", err)
	}
	periodMetrics, err := factorobservability.NewPeriodMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return nil, fmt.Errorf("initialize factor period metrics: %w", err)
	}
	realtimeInventory := factorobservability.NewRealtimeInventory(bindingRepo, datasetMetrics)
	if err := realtimeInventory.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initialize factor realtime dataset inventory: %w", err)
	}
	factorGate := taskrunner.NewFactorGate()
	operationGate := taskrunner.NewOperationGate()
	runner = taskrunner.NewService(cfg.Engine.PythonWorkers, storage, pythonPool,
		taskrunner.WithBatchExecution(cfg.Engine.BatchEnabled),
		taskrunner.WithViewReadConfig(
			cfg.Engine.ViewReadWorkers,
			time.Duration(cfg.Engine.ViewReadTimeoutMS)*time.Millisecond,
		),
		taskrunner.WithDatasetMetrics(runMetrics),
		taskrunner.WithFactorGate(factorGate),
		taskrunner.WithTaskValidator(newTaskValidator(factorRepo, bindingRepo)),
	)

	viewReadyRunner := trigger.NewViewReadyRunner(bindingRepo, factorRepo, runner, storage, cfg.Engine.FactorsDir,
		trigger.WithOperationGate(operationGate), trigger.WithPeriodMetrics(periodMetrics))
	if len(cfg.EventBus.URLs) == 0 {
		log.WarnContextf(ctx, "factor eventbus.urls is empty, realtime trigger startup skipped")
	} else {
		consumer = eventconsumer.New(eventconsumer.Config{
			URLs:           cfg.EventBus.URLs,
			FetchMaxWait:   cfg.EventBus.FetchMaxWait,
			CredentialFile: cfg.EventBus.CredentialFile,
		}, viewReadyRunner)
		if err := consumer.Start(ctx); err != nil {
			log.ErrorContextf(ctx, "启动 factor EventBus trigger 失败: %v", err)
			return nil, err
		}
	}
	registerMetricsReporter(s, realtimeInventory)

	factorService := factorsvc.NewWithRuntime(
		dbm,
		runner,
		factorsvc.WithFactorsDir(cfg.Engine.FactorsDir),
		factorsvc.WithMetadataSync(meta),
		factorsvc.WithRealtimeInventory(realtimeInventory),
		factorsvc.WithFactorGate(factorGate),
		factorsvc.WithOperationGate(operationGate),
		factorsvc.WithBindingOutputCleaner(bindingOutputCleaner{storage: storage, manifests: dbm.OutputManifests()}),
		factorsvc.WithBindingSchemaCleaner(meta),
		factorsvc.WithViewReadyExecutor(viewReadyRunner, storage),
	)
	reconcileCtx, cancelReconcile := context.WithCancel(ctx)
	stopRealtime = cancelReconcile
	waitRealtime = startPendingBindingReconciler(reconcileCtx, factorService)
	startManifestRetention(reconcileCtx, manifests, time.Hour, factorManifestRetention())
	registered := false
	for _, name := range []string{"trpc.moox.factor.FactorMgr", "trpc.moox.factor.FactorMgr.trpc"} {
		if service := s.Service(name); service != nil {
			factorpb.RegisterFactorMgrService(service, factorService)
			registered = true
		}
	}
	if !registered {
		log.WarnContextf(ctx, "FactorMgr service is not configured, skip register")
	}
	if err := registerHealth(s, cfg, dbm, runner, pythonPool, consumer); err != nil {
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

type bindingOutputCleaner struct {
	storage interface {
		ClearFactorOutputs(context.Context, *engine.FactorTask) error
	}
	manifests *store.OutputManifestRepository
}

func (c bindingOutputCleaner) ClearBindingOutputs(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	return c.clearBindingOutputs(ctx, binding, factor, func(string) bool { return true })
}

// ClearBindingOutputsOutsideScope removes only subjects no longer allowed by
// the updated binding, preserving historical rows for subjects that remain in
// its include scope.
func (c bindingOutputCleaner) ClearBindingOutputsOutsideScope(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	return c.clearBindingOutputs(ctx, binding, factor, func(subjectID string) bool {
		return !domain.BindingAllowsSubject(binding, subjectID)
	})
}

func (c bindingOutputCleaner) clearBindingOutputs(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef, shouldClear func(string) bool) error {
	if c.storage == nil || c.manifests == nil {
		return nil
	}
	keys, err := c.manifests.ListByBinding(ctx, binding.BindingID)
	if err != nil {
		return err
	}
	// A lifecycle cleanup is a new mutation even when it targets the same
	// period as an earlier cleanup. Keep this invocation's IDs stable while
	// preventing a later disable/re-enable cycle from reusing old outbox IDs.
	cleanupID := fmt.Sprintf("binding-cleanup-%s-%d", binding.BindingID, time.Now().UnixNano())
	for _, key := range keys {
		if !shouldClear(key.SubjectID) {
			continue
		}
		period := key.PeriodTime.UTC()
		task := &engine.FactorTask{
			TaskID: cleanupID + "-" + period.Format(time.RFC3339Nano), BindingID: binding.BindingID,
			SpaceID: binding.SpaceID, SourceViewID: binding.SourceViewID, ResultDatasetID: binding.ResultDatasetID,
			SubjectID: key.SubjectID, Freq: key.Frequency, PeriodTime: period.Unix(), TriggerEventID: cleanupID,
			TriggeredAt: period, Factor: engine.FactorSpec{FactorID: factor.FactorID, Name: factor.Name, SourceHash: factor.SourceHash, Outputs: append([]string(nil), factor.Outputs...)},
		}
		if err := c.storage.ClearFactorOutputs(ctx, task); err != nil {
			return fmt.Errorf("clear binding %s subject %s period %s: %w", binding.BindingID, key.SubjectID, period.Format(time.RFC3339), err)
		}
	}
	return nil
}

type pendingBindingReconciler interface{ ReconcilePendingBindings(context.Context) error }

func startPendingBindingReconciler(ctx context.Context, reconciler pendingBindingReconciler) func() {
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := reconciler.ReconcilePendingBindings(ctx); err != nil {
					log.WarnContextf(ctx, "reconcile pending factor bindings failed: %v", err)
				}
			}
		}
	}()
	return wg.Wait
}

func startManifestRetention(ctx context.Context, manifests *store.OutputManifestRepository, interval, retention time.Duration) {
	if manifests == nil || retention <= 0 {
		return
	}
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	cleanup := func() {
		if _, err := manifests.DeleteBefore(ctx, time.Now().UTC().Add(-retention)); err != nil {
			log.WarnContextf(ctx, "factor output manifest retention cleanup failed: %v", err)
		}
	}
	cleanup()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()
}

func factorManifestRetention() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("MOOX_FACTOR_MANIFEST_RETENTION")); raw != "" {
		if retention, err := time.ParseDuration(raw); err == nil && retention > 0 {
			return retention
		}
		log.Warnf("invalid MOOX_FACTOR_MANIFEST_RETENTION=%q; manifest cleanup disabled", raw)
	}
	// Keep manifests by default. They are the only durable ownership index for
	// lifecycle cleanup, and Result Views may legally retain facts longer than
	// any single global window. Operators can opt into cleanup after choosing a
	// cutoff no shorter than their longest managed Result View retention.
	return 0
}

type realtimeInventoryReconciler interface {
	Due(time.Time) bool
	Refresh(context.Context) error
}

type metricsReporter interface {
	Handle(context.Context) error
}

type startupContractValidator interface {
	ReconcileAllEnabledBindings(context.Context) error
}

func validateStartupFactorContracts(ctx context.Context, validator startupContractValidator) error {
	if validator == nil {
		return fmt.Errorf("factor startup contract validator is required")
	}
	if err := validator.ReconcileAllEnabledBindings(ctx); err != nil {
		return fmt.Errorf("reconcile persisted factor contracts: %w", err)
	}
	return nil
}

func registerMetricsReporter(s *server.Server, inventory realtimeInventoryReconciler) {
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
	timer.RegisterHandlerService(service, metricsTimerHandler(inventory, h, time.Now))
}

func metricsTimerHandler(inventory realtimeInventoryReconciler, reporter metricsReporter, now func() time.Time) func(context.Context) error {
	return func(ctx context.Context) error {
		if inventory != nil && inventory.Due(now()) {
			if err := inventory.Refresh(ctx); err != nil {
				log.WarnContextf(ctx, "factor realtime dataset inventory refresh failed: %v", err)
			}
		}
		return reporter.Handle(ctx)
	}
}

type realtimeStatus interface {
	Ready() bool
}

func registerHealth(s *server.Server, cfg *Config, dbm *store.Store, runner *taskrunner.Service, pythonPool *engine.PythonWorkerPool, consumer realtimeStatus) error {
	if cfg == nil {
		return nil
	}
	state := health.New("factor", "factor-01", "", "")
	state.SnapshotFunc = factorHealthSnapshot(cfg, dbm, runner, pythonPool, consumer, state)
	if s == nil {
		return fmt.Errorf("factor health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.factor.Health"), state); err != nil {
		return fmt.Errorf("factor health server failed to start: %w", err)
	}
	return nil
}

func factorHealthSnapshot(cfg *Config, dbm *store.Store, runner *taskrunner.Service, pythonPool *engine.PythonWorkerPool, consumer realtimeStatus, state *health.State) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		workerStatus := engine.ExecutorStatus{}
		if pythonPool != nil {
			workerStatus = pythonPool.Status()
		}
		workerReady := workerStatus.Ready && workerStatus.Workers > 0
		taskRunnerReady := runner != nil
		runnerStatus := taskrunner.Status{}
		if runner != nil {
			runnerStatus = runner.Status()
		}
		// A busy SQLite writer must not block /readyz behind the 5s busy
		// timeout. Active task failures still surface through the dataset
		// checks; probe the catalog when the runner is idle.
		databaseReady := dbm != nil
		if databaseReady && runnerStatus.ActiveTasks == 0 && runnerStatus.PendingTasks == 0 {
			pingCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
			databaseReady = dbm.Ping(pingCtx) == nil
			cancel()
		}
		eventBusReady := realtimeConsumerReady(cfg, consumer)
		ready := databaseReady && workerReady && taskRunnerReady && eventBusReady
		state.SetReady(ready)
		rsp := healthz.Base("factor", "factor-01", "", "", factorStartedAt, ready)
		rsp.Details = map[string]any{
			"database":          databaseReady,
			"worker_ready":      workerReady,
			"worker_version":    workerStatus.WorkerVersion,
			"python_version":    workerStatus.PythonVersion,
			"task_runner_ready": taskRunnerReady,
			"eventbus_ready":    eventBusReady,
			"python_workers":    cfg.Engine.PythonWorkers,
			"active_tasks":      runnerStatus.ActiveTasks,
			"pending_tasks":     runnerStatus.PendingTasks,
			"eventbus_enabled":  len(cfg.EventBus.URLs) > 0,
			"storage_gateway":   cfg.Storage.GatewayTarget,
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

func (c *metadataClientAdapter) UpdateFactor(ctx context.Context, req *storagepb.UpdateFactorReq) (*storagepb.UpdateFactorRsp, error) {
	return c.client.UpdateFactor(ctx, req)
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

func (c *metadataClientAdapter) ListViews(ctx context.Context, req *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error) {
	return c.client.ListViews(ctx, req)
}

func (c *metadataClientAdapter) ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq) (*storagepb.ListDatasetSubjectsRsp, error) {
	return c.client.ListDatasetSubjects(ctx, req)
}

func (c *metadataClientAdapter) BindDatasetSubject(ctx context.Context, req *storagepb.BindDatasetSubjectReq) (*storagepb.BindDatasetSubjectRsp, error) {
	return c.client.BindDatasetSubject(ctx, req)
}

func (c *metadataClientAdapter) CreateView(ctx context.Context, req *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	return c.client.CreateView(ctx, req)
}

func (c *metadataClientAdapter) UpdateView(ctx context.Context, req *storagepb.UpdateViewReq) (*storagepb.UpdateViewRsp, error) {
	return c.client.UpdateView(ctx, req)
}

func (c *metadataClientAdapter) GetView(ctx context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	return c.client.GetView(ctx, req)
}

func (c *metadataClientAdapter) ListViewColumns(ctx context.Context, req *storagepb.ListViewColumnsReq) (*storagepb.ListViewColumnsRsp, error) {
	return c.client.ListViewColumns(ctx, req)
}

func (c *metadataClientAdapter) UpsertViewColumn(ctx context.Context, req *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return c.client.UpsertViewColumn(ctx, req)
}

func factorAuthInfo() *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{
		AppId:     "moox-factor",
		Operator:  "moox-factor",
		RequestId: fmt.Sprintf("factor-%d", time.Now().UnixNano()),
	}
	if secret := os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"); strings.TrimSpace(secret) != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}

func factorViewAuthInfo() *commonpb.AuthInfo {
	auth := factorAuthInfo()
	if secret := os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET"); strings.TrimSpace(secret) != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}

func listExecutableBindings(ctx context.Context, repo *store.BindingRepository) ([]domain.FactorBinding, error) {
	return repo.ListExecutable(ctx)
}

type factorTaskRepository interface {
	Get(context.Context, string) (*domain.FactorDef, error)
}

type bindingTaskRepository interface {
	ListExecutable(context.Context) ([]domain.FactorBinding, error)
}

func newTaskValidator(
	factors factorTaskRepository,
	bindings bindingTaskRepository,
) taskrunner.TaskValidator {
	return func(ctx context.Context, task taskrunner.Task) error {
		if factors == nil || bindings == nil {
			return fmt.Errorf("factor task repositories are unavailable")
		}
		factor, err := factors.Get(ctx, task.Factor.FactorID)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf(
					"%w: factor %q no longer exists: %w",
					taskrunner.ErrStaleTask, task.Factor.FactorID, err,
				)
			}
			return fmt.Errorf("load factor %q: %w", task.Factor.FactorID, err)
		}
		if factor.Status != domain.FactorStatusEnabled {
			return fmt.Errorf("%w: factor %q is not enabled", taskrunner.ErrStaleTask, factor.FactorID)
		}
		if factor.SourceHash != task.Factor.SourceHash {
			return fmt.Errorf("%w: factor %q source hash changed", taskrunner.ErrStaleTask, factor.FactorID)
		}
		if !slices.Equal(factor.InputColumns, task.Factor.InputColumns) ||
			factor.ParamsJSON != task.Factor.ParamsJSON ||
			factor.LookbackPeriods != task.LookbackPeriods {
			return fmt.Errorf("%w: factor %q definition changed", taskrunner.ErrStaleTask, factor.FactorID)
		}
		executable, err := bindings.ListExecutable(ctx)
		if err != nil {
			return fmt.Errorf("list executable bindings: %w", err)
		}
		taskSource := task.SourceViewID
		if taskSource == "" {
			taskSource = task.SourceDataset
		}
		taskResult := task.ResultDatasetID
		if taskResult == "" {
			taskResult = task.TargetDataset
		}
		for _, binding := range executable {
			resultMatches := binding.ResultDatasetID == taskResult || binding.ResultDatasetID == ""
			if binding.FactorID == task.Factor.FactorID &&
				(task.BindingID == "" || binding.BindingID == task.BindingID) &&
				binding.SpaceID == task.SpaceID &&
				binding.SourceViewID == taskSource &&
				resultMatches &&
				binding.Freq == task.Freq &&
				domain.BindingAllowsSubject(binding, task.SubjectID) {
				return nil
			}
		}
		return fmt.Errorf("%w: no executable binding matches task scope", taskrunner.ErrStaleTask)
	}
}
