package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/enginehub"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/recalc"
	factorrpc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const (
	factorHealthService    = "trpc.moox.factor.Health"
	factorMgrService       = "trpc.moox.factor.FactorMgr"
	factorMgrTRPCService   = "trpc.moox.factor.FactorMgr.trpc"
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
	for _, name := range []string{factorHealthService, factorEngineService} {
		if s.Service(name) == nil {
			return nil, fmt.Errorf("factor service %q is required", name)
		}
	}
	if s.Service(factorMgrService) == nil && s.Service(factorMgrTRPCService) == nil {
		return nil, errors.New("FactorMgr tRPC or HTTP service is required")
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

	runtime.gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(appCtx, "factor-mgr gateway directory refresh: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("open factor Storage gateway client: %w", err)
	}
	storage := storageio.NewGatewayClient(runtime.gateway, storageio.AuthInfo(fmt.Sprintf("factor-mgr-%d", time.Now().UnixNano())))

	hub := enginehub.New(db, enginehub.WithEngineLeaseTTL(cfg.Engine.LeaseTTL), enginehub.WithJobLeaseTTL(cfg.Engine.JobLeaseTTL))
	recalcService := recalc.NewService(db, recalc.WithSubjectProvider(storage))
	catalogService := catalog.NewService(db, storage,
		catalog.WithLockDir(cfg.Database.Path+".locks"),
		catalog.WithSourceChecker(sourceChecker{python: cfg.Python}),
		catalog.WithRecalcSubmitter(recalcSubmitter{service: recalcService}),
		catalog.WithEarliestPeriodProvider(datasetEarliestPeriodProvider{storage: storage}),
		catalog.WithReadyRecorder(hub.SetResultReady),
	)

	if err := reconcileAtStartup(appCtx, catalogService, startupReconcileAttempts, startupReconcileBackoff); err != nil {
		return nil, fmt.Errorf("reconcile factor result datasets at startup: %w", err)
	}
	stopCatalog, err := startCatalogReconciler(appCtx, catalogService, catalogReconcilePeriod)
	if err != nil {
		return nil, err
	}
	runtime.stopCatalog = stopCatalog

	factorService := factorrpc.NewService(catalogService, recalcRPCAdapter{service: recalcService}, factorrpc.WithEngineAPI(hub))
	for _, name := range []string{factorMgrService, factorMgrTRPCService} {
		if service := s.Service(name); service != nil {
			factorpb.RegisterFactorMgrService(service, factorService)
		}
	}
	factorpb.RegisterFactorEngineService(s.Service(factorEngineService), factorrpc.NewEngineService(hub))

	runtime.health.SnapshotFunc = runtime.healthSnapshot(db)
	if err := registerMetricsReporter(s, hub); err != nil {
		return nil, err
	}
	if err := factorhealth.Register(s.Service(factorHealthService), runtime.health); err != nil {
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

type sourceChecker struct{ python PythonConfig }

func (s sourceChecker) CheckSource(ctx context.Context, _ domain.FactorDef, sourcePath string) error {
	return pyexec.ValidateSource(ctx, s.python.Bin, sourcePath)
}

type recalcSubmitter struct{ service *recalc.Service }

func (s recalcSubmitter) PrepareEnableBackfill(ctx context.Context, set domain.FactorSet, factor domain.FactorDef, start, end time.Time) (store.RecalcJob, error) {
	requestID, err := newRequestID()
	if err != nil {
		return store.RecalcJob{}, err
	}
	return s.service.PrepareEnableBackfill(ctx, set.SetID, factor.FactorID, requestID, start, end)
}

type recalcRPCAdapter struct{ service *recalc.Service }

func (a recalcRPCAdapter) Submit(ctx context.Context, setID string, factorIDs, subjects []string, requestID string, start, end time.Time) (factorrpc.RecalcJob, error) {
	job, err := a.service.Submit(ctx, setID, factorIDs, subjects, requestID, start, end)
	return factorrpc.JobFromStore(job), err
}

func (a recalcRPCAdapter) List(ctx context.Context, setID string, statuses []string) ([]factorrpc.RecalcJob, error) {
	jobs, err := a.service.List(ctx, setID, statuses)
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

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate recalc request id: %w", err)
	}
	return "factor-enable-" + hex.EncodeToString(value[:]), nil
}

type datasetEarliestPeriodProvider struct {
	storage *storageio.Client
}

func (p datasetEarliestPeriodProvider) EarliestDatasetPeriod(ctx context.Context, spaceID, datasetID, freq string) (time.Time, bool, error) {
	subjects, err := p.storage.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(subjects) == 0 {
		return time.Time{}, false, nil
	}
	columns, err := p.storage.DatasetColumns(ctx, spaceID, datasetID)
	if err != nil {
		return time.Time{}, false, err
	}
	if len(columns) == 0 {
		return time.Time{}, false, fmt.Errorf("source Dataset %s/%s has no readable columns", spaceID, datasetID)
	}
	return p.storage.EarliestPeriod(ctx, spaceID, datasetID, freq, subjects, columns)
}

var _ catalog.SourceChecker = sourceChecker{}
var _ catalog.RecalcSubmitter = recalcSubmitter{}
var _ factorrpc.RecalcAPI = recalcRPCAdapter{}
