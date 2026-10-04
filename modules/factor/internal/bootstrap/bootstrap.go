package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/pyexec"
	"github.com/mooyang-code/moox/modules/factor/internal/recalc"
	factorrpc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const factorHealthService = "trpc.moox.factor.Health"

// Runtime owns every long-lived resource created while assembling moox-factor.
type Runtime struct {
	store       *store.Store
	python      *pyexec.Pool
	consumer    *eventconsumer.Consumer
	stopRecalc  func() error
	stopCatalog func() error
	stopMetrics func() error
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
	if s.Service(factorHealthService) == nil {
		return nil, fmt.Errorf("factor health service %q is required", factorHealthService)
	}
	if s.Service("trpc.moox.factor.FactorMgr") == nil && s.Service("trpc.moox.factor.FactorMgr.trpc") == nil {
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

	credentials, err := gatewayauth.ResolveCredentials(cfg.Storage.KeyID, cfg.Storage.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load factor Storage gateway credentials: %w", err)
	}
	auth := factorAuthInfo()
	storage := storageio.NewClientWithCredentials(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, credentials, auth)
	metadataSubjects := storagepb.NewMetadataClientProxy(gatewayauth.NewTRPCClientOptions(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, credentials)...)
	subjectProvider := datasetSubjectProvider{client: metadataSubjects, auth: auth}

	pythonPool, err := pyexec.New(appCtx, cfg.Python.Workers, process.Config{
		PythonBin: cfg.Python.Bin, WorkerPath: cfg.Python.WorkerPath,
		Args: []string{"--factors-dir", cfg.Python.FactorsDir}, TaskTimeout: cfg.Python.TaskTimeout,
		Limits: process.DefaultLimits(),
	})
	if err != nil {
		return nil, fmt.Errorf("start factor Python pool: %w", err)
	}
	runtime.python = pythonPool

	metrics, err := observability.NewMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return nil, fmt.Errorf("register factor pipeline metrics: %w", err)
	}
	monitor := NewHealth(cfg.Pipeline.PeriodBudgetMax)
	observedStorage := &observedStore{Store: storage, health: monitor}
	baseRunner := pipeline.NewRunner(observedStorage, pythonPool, mustCryptoClock(), pipeline.Config{
		ReadBatchSubjects: cfg.Pipeline.ReadBatchSubjects, ReadWorkers: cfg.Pipeline.ReadWorkers,
		ReadTimeout: cfg.Pipeline.ReadTimeout, WriteBatchRows: cfg.Pipeline.WriteBatchRows,
		WriteRetryBackoff: 300 * time.Millisecond,
		PythonWorkers:     cfg.Python.Workers, FactorsDir: cfg.Python.FactorsDir,
	})
	runs := newRunTracker()
	measuredRunner := &measuredRunner{inner: baseRunner, metrics: metrics, health: monitor, runs: runs}

	locator, err := trigger.NewStoreSetLocator(db)
	if err != nil {
		return nil, fmt.Errorf("initialize factor set locator: %w", err)
	}
	setNotifier := &consumerNotifier{}
	var recalcService *recalc.Service
	catalogService := catalog.NewService(db, storage,
		catalog.WithFactorsDir(cfg.Python.FactorsDir),
		catalog.WithLockDir(cfg.Database.Path+".locks"),
		catalog.WithSourceChecker(sourceChecker{python: cfg.Python}),
		catalog.WithRecalcSubmitter(recalcSubmitter{service: func() *recalc.Service { return recalcService }}),
		catalog.WithEarliestPeriodProvider(datasetEarliestPeriodProvider{storage: storage, subjects: subjectProvider}),
		catalog.WithNotifier(setNotifier),
	)
	recalcService = recalc.NewService(db, measuredRunner,
		recalc.WithChunkPeriods(cfg.Recalc.ChunkPeriods),
		recalc.WithLocks(catalogService.Locks()),
		recalc.WithColumnProvider(storage),
		recalc.WithSubjectProvider(subjectProvider),
	)

	if err := reconcileAtStartup(appCtx, catalogService, startupReconcileAttempts, startupReconcileBackoff); err != nil {
		return nil, fmt.Errorf("reconcile factor result datasets at startup: %w", err)
	}
	stopCatalog, err := startCatalogReconciler(appCtx, catalogService, 5*time.Minute)
	if err != nil {
		return nil, err
	}
	runtime.stopCatalog = stopCatalog

	stopRecalc, err := recalcService.Start(appCtx)
	if err != nil {
		return nil, fmt.Errorf("start factor recalc worker: %w", err)
	}
	runtime.stopRecalc = stopRecalc

	consumer, err := eventconsumer.NewConsumer(appCtx, eventconsumer.ConsumerConfig{
		URLs: cfg.EventBus.URLs, CredentialFile: cfg.EventBus.CredentialFile,
		FetchMaxWait: cfg.EventBus.FetchMaxWait, PeriodBudgetMin: cfg.Pipeline.PeriodBudgetMin,
		PeriodBudgetMax: cfg.Pipeline.PeriodBudgetMax,
	}, locator, observedStorage, measuredRunner, catalogService.Locks())
	if err != nil {
		return nil, fmt.Errorf("start factor period consumer: %w", err)
	}
	runtime.consumer = consumer
	setNotifier.setConsumer(consumer)
	runtime.stopMetrics = startMetricsReporter(appCtx, consumer, pythonPool, metrics)

	status := runtimeStatus{consumer: consumer, python: pythonPool, workers: cfg.Python.Workers, metrics: metrics, runs: runs}
	factorService := factorrpc.NewService(catalogService, recalcRPCAdapter{service: recalcService}, factorrpc.WithStatusAPI(status), factorrpc.WithRunSummaryAPI(status))
	registered := false
	for _, name := range []string{"trpc.moox.factor.FactorMgr", "trpc.moox.factor.FactorMgr.trpc"} {
		if service := s.Service(name); service != nil {
			factorpb.RegisterFactorMgrService(service, factorService)
			registered = true
		}
	}
	if !registered {
		return nil, errors.New("FactorMgr tRPC or HTTP service is required")
	}
	runtime.health.SnapshotFunc = runtime.healthSnapshot(db, consumer, pythonPool, monitor)
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
		if r.stopMetrics != nil {
			r.err = errors.Join(r.err, r.stopMetrics())
		}
		if r.consumer != nil {
			r.err = errors.Join(r.err, r.consumer.Close())
		}
		if r.stopRecalc != nil {
			r.err = errors.Join(r.err, r.stopRecalc())
		}
		if r.stopCatalog != nil {
			r.err = errors.Join(r.err, r.stopCatalog())
		}
		if r.python != nil {
			r.err = errors.Join(r.err, r.python.Close())
		}
		if r.store != nil {
			r.err = errors.Join(r.err, r.store.Close())
		}
	})
	return r.err
}

func (r *Runtime) healthSnapshot(db *store.Store, consumer *eventconsumer.Consumer, python *pyexec.Pool, monitor *Health) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		if ctx == nil {
			ctx = context.Background()
		}
		dbReady := db != nil && db.Ping(ctx) == nil
		pythonReady := python.Ready()
		consumerReady := consumer != nil && consumer.Ready()
		monitor.SetDependency("sqlite", dbReady)
		monitor.SetDependency("python", pythonReady)
		monitor.SetDependency("eventbus", consumerReady)
		status := monitor.Check(time.Now())
		state := "ok"
		if !status.Healthy {
			state = "error"
		}
		return healthz.Response{
			Module: "factor", Ready: status.Healthy, Status: state, Time: time.Now().UTC(),
			Details: map[string]any{"reasons": status.Reasons, "consumer_ready": consumerReady, "python_busy": python.Busy()},
		}
	}
}

const (
	startupReconcileAttempts = 5
	startupReconcileBackoff  = 2 * time.Second
)

// reconcileAtStartup tolerates a Storage that is still coming up: result
// datasets must exist before the first period is written (a missing dataset is
// a permanent write error), so the consumer may only start after one success.
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

func startMetricsReporter(ctx context.Context, consumer *eventconsumer.Consumer, python *pyexec.Pool, metrics *observability.Metrics) func() error {
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		knownSets := make(map[string]struct{})
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				metrics.PythonBusy.Set(float64(python.Busy()))
				currentSets := make(map[string]struct{})
				for _, lane := range consumer.LaneStatuses() {
					currentSets[lane.SetID] = struct{}{}
					metrics.LaneBacklog.WithLabelValues(lane.SetID).Set(float64(lane.Queued))
				}
				for setID := range knownSets {
					if _, ok := currentSets[setID]; !ok {
						metrics.LaneBacklog.WithLabelValues(setID).Set(0)
					}
				}
				knownSets = currentSets
			}
		}
	}()
	return func() error {
		<-done
		return nil
	}
}

func mustCryptoClock() periodclock.Clock { return periodclock.Continuous{} }

func factorAuthInfo() *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{AppId: "moox-factor", Operator: "moox-factor", RequestId: fmt.Sprintf("factor-%d", time.Now().UnixNano())}
	if secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")); secret != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}

type observedStore struct {
	storageio.Store
	health *Health
}

func (s *observedStore) WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []storageio.ResultRow) error {
	err := s.Store.WriteRows(ctx, spaceID, datasetID, commitID, rows)
	s.health.RecordStorageWrite(err == nil)
	return err
}

type measuredRunner struct {
	inner interface {
		Run(context.Context, pipeline.Plan) (pipeline.Outcome, error)
	}
	metrics *observability.Metrics
	health  *Health
	runs    *runTracker
}

func (r *measuredRunner) Run(ctx context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
	started := time.Now()
	periodTime := plan.PeriodTime
	if periodTime.IsZero() {
		periodTime = plan.TargetStart
	}
	r.health.StartLane(plan.Set.SetID, started)
	defer r.health.EndLane(plan.Set.SetID)
	log.InfoContextf(ctx, "factor_period_start set_id=%s period_time=%s mode=%d", plan.Set.SetID, periodTime.UTC().Format(time.RFC3339), plan.Mode)
	outcome, err := r.inner.Run(ctx, plan)
	setID := plan.Set.SetID
	for stage, duration := range outcome.StageDurations {
		r.metrics.PeriodDuration.WithLabelValues(setID, stage).Observe(duration.Seconds())
	}
	status := outcome.Status
	if err != nil {
		status = "failed"
	}
	if status == "" {
		status = "complete"
	}
	if plan.Mode == pipeline.ModeLive {
		r.metrics.PeriodTotal.WithLabelValues(setID, status).Inc()
		r.metrics.PeriodLag.WithLabelValues(setID).Set(max(0, time.Since(periodTime).Seconds()))
		if !periodTime.IsZero() && (err == nil || outcome.Status != "") {
			r.metrics.LastPeriodTime.WithLabelValues(setID).Set(float64(periodTime.Unix()))
			r.runs.record(setID, periodTime, status)
		}
	}
	for _, factor := range outcome.Factors {
		if factor.Status != "complete" {
			reason := factor.Status
			if reason == "" {
				reason = "failed"
			}
			r.metrics.Failures.WithLabelValues(setID, factor.FactorID, reason).Inc()
			log.ErrorContextf(ctx, "factor_compute_failed set_id=%s factor_id=%s reason=%s", setID, factor.FactorID, reason)
		}
	}
	if err != nil {
		r.metrics.Failures.WithLabelValues(setID, periodFailureFactor, periodFailureReason(err)).Inc()
		log.ErrorContextf(ctx, "factor_period_failed set_id=%s period_time=%s error=%v", setID, periodTime.UTC().Format(time.RFC3339), err)
	} else {
		log.InfoContextf(ctx, "factor_period_done set_id=%s period_time=%s status=%s rows=%d duration=%s", setID, periodTime.UTC().Format(time.RFC3339), status, outcome.RowsWritten, time.Since(started))
	}
	return outcome, err
}

// periodFailureFactor labels failures that abort a whole period before any
// individual factor outcome exists.
const periodFailureFactor = "*"

func periodFailureReason(err error) string {
	switch {
	case errors.Is(err, storageio.ErrInfra):
		return "storage_unavailable"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "timeout"
	default:
		return "internal"
	}
}

type consumerNotifier struct {
	mu       sync.RWMutex
	consumer *eventconsumer.Consumer
}

func (n *consumerNotifier) setConsumer(consumer *eventconsumer.Consumer) {
	n.mu.Lock()
	n.consumer = consumer
	n.mu.Unlock()
}

func (n *consumerNotifier) SetsChanged() {
	n.mu.RLock()
	consumer := n.consumer
	n.mu.RUnlock()
	if consumer != nil {
		consumer.SetsChanged()
	}
}

type sourceChecker struct{ python PythonConfig }

func (s sourceChecker) CheckSource(ctx context.Context, factor domain.FactorDef, sourcePath string) error {
	return pyexec.ValidateSource(ctx, s.python.Bin, sourcePath)
}

type recalcSubmitter struct{ service func() *recalc.Service }

func (s recalcSubmitter) PrepareEnableBackfill(ctx context.Context, set domain.FactorSet, factor domain.FactorDef, start, end time.Time) (store.RecalcJob, error) {
	service := s.service()
	if service == nil {
		return store.RecalcJob{}, errors.New("factor recalc service is not initialized")
	}
	requestID, err := newRequestID()
	if err != nil {
		return store.RecalcJob{}, err
	}
	return service.PrepareEnableBackfill(ctx, set.SetID, factor.FactorID, requestID, start, end)
}

type recalcRPCAdapter struct{ service *recalc.Service }

func (a recalcRPCAdapter) Submit(ctx context.Context, setID string, factorIDs, subjects []string, requestID string, start, end time.Time) (factorrpc.RecalcJob, error) {
	job, err := a.service.Submit(ctx, setID, factorIDs, subjects, requestID, start, end)
	return rpcJob(job), err
}

func (a recalcRPCAdapter) Get(ctx context.Context, jobID string) (factorrpc.RecalcJob, error) {
	job, err := a.service.Get(ctx, jobID)
	return rpcJob(job), err
}

func (a recalcRPCAdapter) Cancel(ctx context.Context, jobID string) (factorrpc.RecalcJob, error) {
	job, err := a.service.Cancel(ctx, jobID)
	return rpcJob(job), err
}

func rpcJob(job store.RecalcJob) factorrpc.RecalcJob {
	return factorrpc.RecalcJob{
		JobID: job.JobID, RequestID: job.RequestID, SetID: job.SetID,
		FactorIDs: append([]string(nil), job.FactorIDs...), Subjects: append([]string(nil), job.Subjects...),
		StartTime: unixTime(job.StartTime), EndTime: unixTime(job.EndTime), ProgressTime: unixTime(job.ProgressTime),
		Status: job.Status, Error: job.Error, CreatedAt: job.CreatedAt, UpdatedAt: job.UpdatedAt,
	}
}

func unixTime(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.Unix(value, 0).UTC()
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate recalc request id: %w", err)
	}
	return "factor-enable-" + hex.EncodeToString(value[:]), nil
}

type datasetSubjectProvider struct {
	client storagepb.MetadataClientProxy
	auth   *commonpb.AuthInfo
}

type datasetEarliestPeriodProvider struct {
	storage  *storageio.Client
	subjects datasetSubjectProvider
}

func (p datasetEarliestPeriodProvider) EarliestDatasetPeriod(ctx context.Context, spaceID, datasetID, freq string) (time.Time, bool, error) {
	if p.storage == nil {
		return time.Time{}, false, errors.New("Storage history client is required")
	}
	subjects, err := p.subjects.ListDatasetSubjects(ctx, spaceID, datasetID)
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

func (p datasetSubjectProvider) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	if p.client == nil {
		return nil, errors.New("Storage Metadata client is required")
	}
	const pageSize = 1000
	seen := make(map[string]struct{})
	for page := uint32(1); ; page++ {
		response, err := p.client.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			AuthInfo: p.auth, SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: pageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list dataset subjects: %w", err)
		}
		if response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, errors.New("list dataset subjects returned an unsuccessful response")
		}
		for _, item := range response.GetDatasetSubjects() {
			if item != nil && item.GetSubjectId() != "" && item.GetStatus() == "active" {
				seen[item.GetSubjectId()] = struct{}{}
			}
		}
		result := response.GetPageResult()
		if result == nil || !result.GetHasMore() || len(response.GetDatasetSubjects()) == 0 {
			break
		}
		if page >= 100000 {
			return nil, errors.New("dataset subject pagination exceeded the page limit")
		}
	}
	subjects := make([]string, 0, len(seen))
	for subject := range seen {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	return subjects, nil
}

type runtimeStatus struct {
	consumer *eventconsumer.Consumer
	python   *pyexec.Pool
	workers  int
	metrics  *observability.Metrics
	runs     *runTracker
}

func (s runtimeStatus) GetStatus(context.Context) (factorrpc.RuntimeStatus, error) {
	status := factorrpc.RuntimeStatus{PythonWorkers: int32(s.workers), RecentRuns: s.runs.all(time.Now())}
	if s.consumer != nil {
		status.ConsumerRunning = s.consumer.Ready()
		for _, lane := range s.consumer.LaneStatuses() {
			status.Lanes = append(status.Lanes, factorrpc.FactorLaneStatus{SetID: lane.SetID, Queued: int32(lane.Queued), Active: lane.Active})
		}
	}
	if s.python != nil {
		status.PythonBusy = int32(s.python.Busy())
		s.metrics.PythonBusy.Set(float64(s.python.Busy()))
	}
	return status, nil
}

func (s runtimeStatus) LatestRun(_ context.Context, setID string) (factorrpc.SetRunSummary, error) {
	return s.runs.latest(setID, time.Now()), nil
}

var _ catalog.Notifier = (*consumerNotifier)(nil)
var _ catalog.SourceChecker = sourceChecker{}
var _ catalog.RecalcSubmitter = recalcSubmitter{}
var _ factorrpc.RecalcAPI = recalcRPCAdapter{}
var _ factorrpc.StatusAPI = runtimeStatus{}
var _ factorrpc.RunSummaryAPI = runtimeStatus{}
