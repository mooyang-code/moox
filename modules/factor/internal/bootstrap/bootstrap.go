package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/enginehub"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/preview"
	"github.com/mooyang-code/moox/modules/factor/internal/recalc"
	factorrpc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/udfhost"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	factorHealthService    = "trpc.moox.factor.Health"
	factorMgrService       = "trpc.moox.factor.FactorMgr"
	factorEngineService    = "trpc.moox.factor.FactorEngine"
	catalogReconcilePeriod = 5 * time.Minute
)

// Runtime owns every long-lived resource created while assembling
// moox-factor-mgr: the SQLite catalog, result-dataset reconciliation and the
// engine hub. Factor computation runs in moox-factor-engine.
type Runtime struct {
	gateway     *gatewayclient.Client
	store       *store.Store
	stopCatalog func() error
	cancel      context.CancelFunc
	udfRuntimes []*factorudf.Runtime
	health      *factorhealth.State
	once        sync.Once
	err         error
}

func Initialize(ctx context.Context, s *server.Server, cfg *Config) (_ *Runtime, err error) {
	if ctx == nil || s == nil || cfg == nil {
		return nil, errors.New("factor context, server and config are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	for _, name := range []string{factorHealthService, factorEngineService, factorMgrService} {
		if s.Service(name) == nil {
			return nil, fmt.Errorf("factor service %q is required", name)
		}
	}

	runtime := &Runtime{health: factorhealth.New("factor", "factor", "", "")}
	appCtx, cancel := context.WithCancel(ctx)
	runtime.cancel = cancel
	defer func() {
		if err != nil {
			err = errors.Join(err, runtime.Close())
		}
	}()

	db, err := store.Open(&store.Options{Path: cfg.Database.Path})
	if err != nil {
		return nil, fmt.Errorf("open factor database: %w", err)
	}
	runtime.store = db
	life, err := newMgrLifecycle(db, cfg.Database.Path)
	if err != nil {
		return nil, fmt.Errorf("open factor-mgr lifecycle: %w", err)
	}
	go func() {
		life.Recover(appCtx)
		life.Run(appCtx, lifecycleRefreshEvery)
	}()

	runtime.gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(appCtx, "factor-mgr gateway directory refresh: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("open factor Storage gateway client: %w", err)
	}
	storage := storageio.NewGatewayClient(runtime.gateway, storageio.AuthInfo(fmt.Sprintf("factor-mgr-%d", time.Now().UnixNano())))

	hub := enginehub.New(db, enginehub.WithEngineLeaseTTL(cfg.Engine.LeaseTTL), enginehub.WithJobLeaseTTL(cfg.Engine.JobLeaseTTL), enginehub.WithHeartbeatStore(db))
	recalcService := recalc.NewService(db, recalc.WithSubjectProvider(storage))
	// 自定义算子（wasm UDF）：发布复核与试算各有自己的运行时、内存预算与时限，与引擎分开计算；管理端不常驻实例。
	verifyRuntime, err := factorudf.NewRuntime(appCtx, factorudf.Options{
		Budget: udfhost.NewBudget(udfVerifyMemory), Limits: factorudf.Limits{PoolSize: 2, MemoryLimitPages: udfInstancePages},
	})
	if err != nil {
		return nil, fmt.Errorf("create udf verify runtime: %w", err)
	}
	runtime.udfRuntimes = append(runtime.udfRuntimes, verifyRuntime)
	previewRuntime, err := factorudf.NewRuntime(appCtx, factorudf.Options{
		Budget: udfhost.NewBudget(udfPreviewMemory), Limits: factorudf.Limits{PoolSize: previewWorkers, MemoryLimitPages: udfInstancePages},
	})
	if err != nil {
		return nil, fmt.Errorf("create udf preview runtime: %w", err)
	}
	runtime.udfRuntimes = append(runtime.udfRuntimes, previewRuntime)
	catalogService := catalog.NewService(db, storage,
		catalog.WithLockDir(cfg.Database.Path+".locks"),
		catalog.WithReadyRecorder(hub.SetResultReady),
		catalog.WithUDFVerifier(catalog.UDFVerifier{Runtime: verifyRuntime, Conformance: factorudf.ConformanceOptions{Timeout: udfVerifyTimeout}}),
		catalog.WithEngineView(hub.Engine),
	)
	fetchModule := func(ctx context.Context, hash string) ([]byte, error) {
		_, wasm, err := catalogService.UdfModule(ctx, hash)
		return wasm, err
	}

	if err := reconcileAtStartup(appCtx, catalogService, startupReconcileAttempts, startupReconcileBackoff); err != nil {
		return nil, fmt.Errorf("reconcile factor result datasets at startup: %w", err)
	}
	stopCatalog, err := startCatalogReconciler(appCtx, catalogService, catalogReconcilePeriod)
	if err != nil {
		return nil, err
	}
	runtime.stopCatalog = stopCatalog

	filled := factorrpc.NewFilledInbox()
	factorService := factorrpc.NewService(catalogService, recalcRPCAdapter{service: recalcService}, factorrpc.WithEngineAPI(hub), factorrpc.WithMutationGate(mutationGate(life)),
		factorrpc.WithPreviewer(preview.New(storage, previewWorkers, preview.WithUDF(udfhost.New(previewRuntime), fetchModule))), factorrpc.WithFilledInbox(filled))
	factorpb.RegisterFactorMgrService(s.Service(factorMgrService), factorService)
	factorpb.RegisterFactorEngineService(s.Service(factorEngineService), factorrpc.NewEngineService(hub, catalogService, filled))

	runtime.health.SnapshotFunc = withLifecycle(runtime.healthSnapshot(db), life)
	if err := registerMetricsReporter(s, hub); err != nil {
		return nil, err
	}
	if err := factorhealth.Register(s.Service(factorHealthService), runtime.health, lifecycleRoutes(life)...); err != nil {
		return nil, fmt.Errorf("register factor health service: %w", err)
	}
	runtime.health.SetReady(true)
	return runtime, nil
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
		if r.stopCatalog != nil {
			r.err = errors.Join(r.err, r.stopCatalog())
		}
		if r.gateway != nil {
			r.err = errors.Join(r.err, r.gateway.Close())
		}
		for _, rt := range r.udfRuntimes {
			r.err = errors.Join(r.err, rt.Close(context.Background()))
		}
		if r.store != nil {
			r.err = errors.Join(r.err, r.store.Close())
		}
	})
	return r.err
}

func (r *Runtime) healthSnapshot(db *store.Store) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		if ctx == nil {
			ctx = context.Background()
		}
		dbReady := db != nil && db.Ping(ctx) == nil
		state := "ok"
		var reasons []string
		if !dbReady {
			state = "error"
			reasons = append(reasons, "sqlite unavailable")
		}
		return healthz.Response{
			Module: "factor", Ready: dbReady, Status: state, Time: time.Now().UTC(),
			Details: map[string]any{"reasons": reasons},
		}
	}
}

const (
	startupReconcileAttempts = 5
	startupReconcileBackoff  = 2 * time.Second
	// previewWorkers 是 PreviewFactor 试算的并行度：只算少量标的，管理端所在的 control 主机核数少。
	previewWorkers = 2
	// 自定义算子在管理端的限额：发布复核（一致性测试，串行执行）与试算各自的实例内存预算、单实例内存页数上限（64 KiB 一页）
	// 与一致性测试的总时限。
	udfVerifyMemory  = 512 << 20
	udfPreviewMemory = 256 << 20
	udfInstancePages = 2048 // 128 MiB
	udfVerifyTimeout = 2 * time.Minute
)

// reconcileAtStartup tolerates a Storage that is still coming up: a set is
// only marked ready for the engine after its result dataset accepted the
// columns, so the first reconciliation runs before the engine is served.
func reconcileAtStartup(ctx context.Context, service *catalog.Service, attempts int, backoff time.Duration) error {
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err = service.Reconcile(ctx); err == nil {
			return nil
		}
		log.ErrorContextf(ctx, "factor startup reconcile attempt %d/%d failed: %v", attempt, attempts, err)
		if attempt == attempts {
			break
		}
		timer := time.NewTimer(time.Duration(attempt) * backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
	}
	return err
}

func startCatalogReconciler(ctx context.Context, service *catalog.Service, interval time.Duration) (func() error, error) {
	if ctx == nil || service == nil || interval <= 0 {
		return nil, errors.New("catalog reconciliation context, service and interval are required")
	}
	reconcileCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-reconcileCtx.Done():
				return
			case <-ticker.C:
				if err := service.Reconcile(reconcileCtx); err != nil && reconcileCtx.Err() == nil {
					log.ErrorContextf(reconcileCtx, "factor catalog reconciliation failed: %v", err)
				}
			}
		}
	}()
	return func() error {
		cancel()
		<-done
		return nil
	}, nil
}

type recalcRPCAdapter struct{ service *recalc.Service }

func (a recalcRPCAdapter) Submit(ctx context.Context, taskID string, members, subjects []string, requestID string, start, end time.Time) (factorrpc.RecalcJob, error) {
	job, err := a.service.Submit(ctx, taskID, members, subjects, requestID, start, end)
	return factorrpc.JobFromStore(job), err
}

func (a recalcRPCAdapter) List(ctx context.Context, taskID string, statuses []string) ([]factorrpc.RecalcJob, error) {
	jobs, err := a.service.List(ctx, taskID, statuses)
	if err != nil {
		return nil, err
	}
	out := make([]factorrpc.RecalcJob, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, factorrpc.JobFromStore(job))
	}
	return out, nil
}

func (a recalcRPCAdapter) Get(ctx context.Context, jobID string) (factorrpc.RecalcJob, error) {
	job, err := a.service.Get(ctx, jobID)
	return factorrpc.JobFromStore(job), err
}

func (a recalcRPCAdapter) Cancel(ctx context.Context, jobID string) (factorrpc.RecalcJob, error) {
	job, err := a.service.Cancel(ctx, jobID)
	return factorrpc.JobFromStore(job), err
}

var _ factorrpc.RecalcAPI = recalcRPCAdapter{}
