// Package bootstrap wires the independent moox-collector service process.
package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/dnscache"
	collectordns "github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	stockmarket "github.com/mooyang-code/moox/modules/collector/internal/markets/stockcn"
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	"github.com/mooyang-code/moox/modules/collector/internal/marketwiring"
	collectorobservability "github.com/mooyang-code/moox/modules/collector/internal/observability"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresult "github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/storageio"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var collectorStartedAt = time.Now()

const (
	marketFetchScheduleTimeout  = 30 * time.Second
	marketFetchReconcileTimeout = 30 * time.Second
)

// Initialize loads config, initializes persistence, and registers RPC services.
func Initialize(ctx context.Context, s *server.Server) (*Runtime, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	process := newRuntime(ctx, s)
	ctx = process.ctx
	keepRuntime := false
	defer func() {
		if !keepRuntime {
			_ = process.Close()
		}
	}()
	log.InfoContextf(ctx, "开始初始化 moox-collector...")

	cfg, err := Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 collector 配置失败: %v", err)
		return nil, err
	}
	if spaceID := marketFetchSpaceID(); spaceID == "" {
		return nil, fmt.Errorf("MOOX_SPACE_ID is required for collector market scheduling")
	}
	log.InfoContextf(ctx, "collector stockcn runtime config expected_timer_function_count=%d measured_safe_group_size=%d stagger_start_second=%d stagger_window_seconds=%d stagger_max_starts_per_second=%d", cfg.StockCN.ExpectedTimerFunctionCount, cfg.StockCN.MeasuredSafeGroupSize, cfg.StockCN.StaggerStartSecond, cfg.StockCN.StaggerWindowSeconds, cfg.StockCN.StaggerMaxStartsPerSecond)
	dbm, err := store.Open(&store.Options{
		Path:            cfg.Database.Path,
		MaxIdleConns:    cfg.Database.MaxIdleConns,
		MaxOpenConns:    cfg.Database.MaxOpenConns,
		ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
		ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
	})
	if err != nil {
		log.ErrorContextf(ctx, "初始化 collector 数据库失败: %v", err)
		return nil, err
	}
	process.closeDatabase = dbm.Close
	if err := dbm.ApplySchema(collectorschema.AllSQL()); err != nil {
		log.ErrorContextf(ctx, "初始化 collector schema 失败: %v", err)
		return nil, err
	}
	process.gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "collector directory refresh failed: %v", err) })
	if err != nil {
		return nil, fmt.Errorf("initialize collector gateway: %w", err)
	}

	datasetMetrics, err := report.NewDatasetMetrics(prometheus.DefaultRegisterer, "collector")
	if err != nil {
		return nil, fmt.Errorf("initialize collector dataset metrics: %w", err)
	}
	moduleMetrics, err := report.NewModuleMetrics(
		prometheus.DefaultRegisterer,
		"collector",
		report.HealthCheckIDsForModule("collector"),
	)
	if err != nil {
		return nil, fmt.Errorf("initialize collector module metrics: %w", err)
	}
	datasetRunObserver, err := report.NewDatasetModuleObserver(
		datasetMetrics,
		moduleMetrics,
		"collect",
		"collector-market-data",
	)
	if err != nil {
		return nil, fmt.Errorf("initialize collector run metrics: %w", err)
	}
	realtimeInventory := collectorobservability.NewRealtimeInventory(dbm.Tasks(), datasetMetrics)
	realtimeInventory.SetResampleEnabled(cfg.KlineResample.Enabled)
	if err := realtimeInventory.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initialize collector realtime dataset inventory: %w", err)
	}
	resultMetadata, resultMetadataErr := marketstorage.NewGatewayResampleMetadataClient(process.gateway, marketstorage.InstTypeSPOT)
	if resultMetadataErr != nil {
		return nil, fmt.Errorf("initialize collector result metadata client: %w", resultMetadataErr)
	}
	resultManager := collectorresult.NewManagerWithCleaner(resultMetadata.Client, resultMetadata.Primary, resultMetadata.Auth)
	if err := ensureTaskResultMetadata(ctx, dbm.Tasks(), resultManager, cfg.Storage.ResultDataNodeID); err != nil {
		return nil, fmt.Errorf("initialize collector task results: %w", err)
	}
	svc := collectsvc.New(dbm, collectsvc.Dependencies{
		DatasetSource:              storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway)),
		RealtimeInventory:          realtimeInventory,
		DefaultResampleSettleDelay: cfg.KlineResample.DefaultSettleDelay,
		ResultManager:              resultManager,
		ResultDataNodeID:           cfg.Storage.ResultDataNodeID,
	})
	collectorpb.RegisterCollectMgrService(s.Service("trpc.moox.collector.CollectMgr"), svc)
	if err := registerMarketFetchRuntime(s, collectsvc.NewMarketFetchRuntime(&marketfetch.TimerBatchClaimer{Batches: dbm.TimerPeriodBatches()})); err != nil {
		return nil, err
	}
	marketFetchMetrics := marketfetch.NewMetrics(prometheus.DefaultRegisterer)
	marketFetchMetrics.SetDatasetRunObserver(datasetRunObserver)
	dnsDomains := append([]string(nil), cfg.DNS.Domains...)
	if cfg.EgressProxy.DNS.Enabled() {
		dnsDomains = append(dnsDomains, cfg.EgressProxy.DNS.Domains...)
	}
	localDNS := dnscache.New(dnscache.Config{Domains: dnsDomains, RefreshInterval: cfg.DNS.RefreshInterval, ResolveTimeout: cfg.DNS.ResolveTimeout, Nameservers: cfg.DNS.Nameservers})
	var remoteDNS collectordns.DomainResolver
	if cfg.EgressProxy.DNS.Enabled() {
		remoteDNS = collectordns.NewEgressClient(process.gateway, cfg.EgressProxy.DNS.RequestTimeout)
	}
	refreshInterval, cacheTTL := cfg.DNS.RefreshInterval, cfg.DNS.RefreshInterval
	if cfg.EgressProxy.DNS.Enabled() {
		refreshInterval, cacheTTL = cfg.EgressProxy.DNS.RefreshInterval, cfg.EgressProxy.DNS.CacheTTL
	} else {
		cacheTTL = 0 // preserve dnscache's last-good local snapshot semantics
	}
	var dnsMetrics *collectordns.Metrics
	if metrics, metricsErr := collectordns.NewMetrics(prometheus.DefaultRegisterer); metricsErr != nil {
		log.WarnContextf(ctx, "collector DNS resolver metrics disabled: %v", metricsErr)
	} else {
		dnsMetrics = metrics
	}
	dnsPersistencePath := ""
	if cfg.EgressProxy.DNS.Enabled() {
		dnsPersistencePath = filepath.Join(filepath.Dir(cfg.Database.Path), "dns_resolver_snapshot.json")
	}
	dnsSnapshot := collectordns.NewCoordinator(collectordns.CoordinatorConfig{Local: localDNS, Remote: remoteDNS, RemoteDomains: cfg.EgressProxy.DNS.Domains, Domains: dnsDomains, Interval: refreshInterval, CacheTTL: cacheTTL, Metrics: dnsMetrics, PersistencePath: dnsPersistencePath})
	if err := dnsSnapshot.RestoreLastGoodSnapshot(); err != nil {
		log.WarnContextf(ctx, "restore collector DNS last-good snapshot failed: %v", err)
	}
	if err := dnsSnapshot.Refresh(ctx); err != nil {
		log.WarnContextf(ctx, "collector initial DNS snapshot refresh failed: %v", err)
	}
	registerDNSRefreshSchedule(s, dnsSnapshot, process)
	registerMarketFetchSchedule(ctx, s, cfg, dbm, dnsSnapshot, marketFetchMetrics, process)
	if err := registerHealth(s, cfg, dbm, dnsSnapshot); err != nil {
		return nil, err
	}
	registerMetricsReporter(s, realtimeInventory)

	keepRuntime = true
	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return process, nil
}

const marketFetchRuntimeNativeService = "trpc.moox.collector.MarketFetchRuntime.native"

func registerMarketFetchRuntime(s *server.Server, implementation collectorpb.MarketFetchRuntimeService) error {
	if s == nil || implementation == nil {
		return fmt.Errorf("collector MarketFetchRuntime server is not initialized")
	}
	service := s.Service(marketFetchRuntimeNativeService)
	if service == nil {
		return fmt.Errorf("collector MarketFetchRuntime listener %q is not configured", marketFetchRuntimeNativeService)
	}
	if err := service.Register(&collectorpb.MarketFetchRuntimeServer_ServiceDesc, implementation); err != nil {
		return fmt.Errorf("register MarketFetchRuntime: %w", err)
	}
	return nil
}

func ensureTaskResultMetadata(ctx context.Context, repo *store.TaskRepository, manager *collectorresult.Manager, dataNodeID string) error {
	if repo == nil || manager == nil {
		return fmt.Errorf("task result metadata dependencies are not configured")
	}
	for page := 1; ; page++ {
		// Provision the result identity for disabled tasks too. A disabled task
		// can be enabled later without requiring an operator-only metadata repair;
		// the scheduler still considers only enabled tasks for execution.
		tasks, total, err := repo.List(ctx, store.TaskFilter{Page: page, PageSize: store.MaxEnabledTasks})
		if err != nil {
			return fmt.Errorf("list collector tasks: %w", err)
		}
		for _, task := range tasks {
			params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
			if err != nil {
				return fmt.Errorf("parse task %s/%s result config: %w", task.SpaceID, task.TaskID, err)
			}
			var ids collectorresult.IDs
			if strings.EqualFold(strings.TrimSpace(task.DataType), "kline_resample") {
				// Resample targets have a stricter immutable lineage contract and are
				// provisioned by resample.Preparer after it has resolved the source
				// Dataset. The generic result manager must not claim that Dataset as a
				// raw collection result.
				ids = collectorresult.PersistedResultIDs(task.SpaceID, task.TaskID, task.ResultDatasetID, task.ResultViewID)
			} else {
				if len(task.TagIDs) == 0 {
					return fmt.Errorf("task %s/%s has no bound tags", task.SpaceID, task.TaskID)
				}
				logicalIDs := collectorresult.ResultIDsForTask(task.SpaceID, task.TaskID, task.DataType, params.Frequency)
				ids, err = manager.Ensure(ctx, task.SpaceID, task.TaskID, task.DataType, "", collectorresult.Config{
					ViewID:       firstNonEmptyResultID(task.ResultViewID, logicalIDs.ViewID),
					DataNodeID:   dataNodeID,
					Name:         task.TaskName,
					Description:  task.Description,
					Frequency:    taskResultFrequency(*params),
					SubjectTags:  append([]string(nil), task.TagIDs...),
					OutputFields: params.OutputFields,
				})
				if err != nil {
					return fmt.Errorf("ensure task %s/%s result: %w", task.SpaceID, task.TaskID, err)
				}
			}
			if err := persistTaskResultIDs(ctx, repo, &task, ids); err != nil {
				return err
			}
		}
		if int64(page*store.MaxEnabledTasks) >= total || len(tasks) == 0 {
			break
		}
	}
	return nil
}

// persistTaskResultIDs records the result identities on the task row and in
// its collect params when either differs from what is stored.
func persistTaskResultIDs(ctx context.Context, repo *store.TaskRepository, task *domain.CollectionTask, ids collectorresult.IDs) error {
	needsUpdate := task.ResultDatasetID != ids.DatasetID || task.ResultViewID != ids.ViewID
	task.ResultDatasetID, task.ResultViewID = ids.DatasetID, ids.ViewID
	rawParams := map[string]any{}
	if err := json.Unmarshal([]byte(task.CollectParams), &rawParams); err != nil {
		return fmt.Errorf("decode task %s/%s collect params: %w", task.SpaceID, task.TaskID, err)
	}
	if rawParams["target_dataset_id"] != ids.DatasetID {
		rawParams["target_dataset_id"] = ids.DatasetID
		needsUpdate = true
	}
	if !needsUpdate {
		return nil
	}
	encoded, err := json.Marshal(rawParams)
	if err != nil {
		return fmt.Errorf("encode task %s/%s collect params: %w", task.SpaceID, task.TaskID, err)
	}
	task.CollectParams = string(encoded)
	if _, err := repo.UpdateByTaskID(ctx, task.SpaceID, task.TaskID, *task); err != nil {
		return fmt.Errorf("persist task %s/%s result: %w", task.SpaceID, task.TaskID, err)
	}
	return nil
}

func firstNonEmptyResultID(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func taskResultFrequency(params domain.CollectParams) string {
	if strings.EqualFold(params.Collector.DataType, "kline_resample") && strings.TrimSpace(params.TargetFrequency) != "" {
		return params.TargetFrequency
	}
	if strings.TrimSpace(params.Frequency) != "" {
		return params.Frequency
	}
	return params.TargetFrequency
}

type realtimeInventoryReconciler interface {
	Due(time.Time) bool
	Refresh(context.Context) error
}

type metricsReporter interface {
	Handle(context.Context) error
}

func registerMetricsReporter(s *server.Server, inventory realtimeInventoryReconciler) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("collector", "moox_collector"))
	if err != nil {
		log.Warnf("collector metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.collector.metrics.timer")
	if service == nil {
		log.Warn("collector metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, metricsTimerHandler(inventory, h, time.Now))
}

func metricsTimerHandler(inventory realtimeInventoryReconciler, reporter metricsReporter, now func() time.Time) func(context.Context) error {
	return func(ctx context.Context) error {
		if inventory != nil && inventory.Due(now()) {
			if err := inventory.Refresh(ctx); err != nil {
				log.WarnContextf(ctx, "collector realtime dataset inventory refresh failed: %v", err)
			}
		}
		return reporter.Handle(ctx)
	}
}

type dnsStatusProvider interface {
	Status() collectordns.Status
}

func registerHealth(s *server.Server, cfg *Config, dbm *store.Store, dns ...dnsStatusProvider) error {
	if cfg == nil {
		return nil
	}
	state := health.New("collector", "collector", "", "")
	state.SnapshotFunc = collectorHealthSnapshot(cfg, dbm, state, dns...)
	if s == nil {
		return fmt.Errorf("collector health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.collector.Health"), state); err != nil {
		return fmt.Errorf("collector health server failed to start: %w", err)
	}
	return nil
}

func collectorHealthSnapshot(cfg *Config, dbm *store.Store, state *health.State, dns ...dnsStatusProvider) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		// Do not synchronously ping SQLite from /readyz. The collector's single
		// SQLite connection is intentionally shared by the scheduler and the
		// completion consumer; during a large batch, a health probe can otherwise
		// wait behind a writer and make a healthy process appear dead to cron.
		databaseReady := dbm != nil
		state.SetReady(databaseReady)
		rsp := healthz.Base("collector", "collector", "", "", collectorStartedAt, databaseReady)
		rsp.Details = map[string]any{
			"database":       databaseReady,
			"gateway_caller": cfg.GatewayClient.Caller,
		}
		if cfg.EgressProxy.DNS.Enabled() {
			if len(dns) > 0 && dns[0] != nil {
				status := dns[0].Status()
				rsp.Details["dns_resolver"] = map[string]any{
					"enabled":             true,
					"source":              status.Source,
					"hash":                status.Hash,
					"managed_hash":        status.ManagedHash,
					"route_count":         status.RouteCount,
					"route_age_seconds":   status.RouteAgeSeconds,
					"last_refresh_at":     formatHealthTime(status.LastRefreshAt),
					"last_success_at":     formatHealthTime(status.LastSuccessAt),
					"last_error_category": status.LastErrorCategory,
				}
			} else {
				rsp.Details["dns_resolver"] = map[string]any{"enabled": true, "source": "unavailable"}
			}
		} else {
			rsp.Details["dns_resolver"] = map[string]any{"enabled": false, "source": "local"}
		}
		return rsp
	}
}

func formatHealthTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

type dnsSnapshotter interface {
	Due(time.Time) bool
	Refresh(context.Context) error
	Snapshot() map[string]sources.DNSResolution
}

func registerDNSRefreshSchedule(s *server.Server, cache dnsSnapshotter, process *Runtime) {
	if s == nil || cache == nil {
		return
	}
	service := s.Service("trpc.moox.collector.dns.timer")
	if service == nil {
		log.Warn("collector DNS timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		if !cache.Due(time.Now().UTC()) {
			return nil
		}
		process.launch(func() {
			if err := cache.Refresh(process.ctx); err != nil {
				log.WarnContextf(ctx, "collector DNS snapshot refresh failed: %v", err)
			}
		})
		return nil
	})
}

func registerMarketFetchSchedule(ctx context.Context, s *server.Server, cfg *Config, dbm *store.Store, dnsCache dnsSnapshotter, metrics *marketfetch.Metrics, process *Runtime) {
	if s == nil || cfg == nil || dbm == nil {
		return
	}
	service := s.Service("trpc.moox.collector.schedule.timer")
	if service == nil {
		log.Warn("collector market fetch timer service is not configured, scheduler registration skipped")
	}
	invoker := scfinvoker.New(scfinvoker.Config{Gateway: process.gateway})
	metadataSource := storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway))
	plannerSource := metadataSource
	newStorage := func(marketType, writeSource string) (marketfetch.Storage, error) {
		if strings.EqualFold(strings.TrimSpace(marketType), "equity") || strings.EqualFold(strings.TrimSpace(marketType), marketfetch.StockCNSpaceID) {
			marketType = marketstorage.InstTypeSPOT
		}
		return marketstorage.NewGatewayBatchStorage(process.gateway, marketType, writeSource)
	}
	spaceIDs := marketFetchSpaceIDs()
	if len(spaceIDs) == 0 {
		log.Warn("collector market fetch scheduler has no configured spaces")
		return
	}
	var resampleRunner *collectorresample.Runner
	var resampleRunners []*collectorresample.Runner
	var resamplePreparer *collectorresample.Preparer
	var resampleMetrics *collectorresample.Metrics
	if cfg.KlineResample.Enabled {
		resampleMetrics = collectorresample.NewMetrics(prometheus.DefaultRegisterer)
		metadataClient, metadataErr := marketstorage.NewGatewayResampleMetadataClient(process.gateway, marketstorage.InstTypeSPOT)
		localStorage, storageErr := marketstorage.NewGatewayResampleStorage(process.gateway, marketstorage.InstTypeSPOT, "collector:kline_resample")
		if metadataErr != nil || storageErr != nil {
			log.WarnContextf(process.ctx, "collector kline resample disabled: metadata=%v storage=%v", metadataErr, storageErr)
		} else {
			catalog := &collectorresample.Catalog{Metadata: metadataClient.Client, Auth: metadataClient.Auth}
			resamplePreparer = &collectorresample.Preparer{Tasks: dbm.Tasks(), Source: metadataSource, Catalog: catalog, Limit: cfg.KlineResample.WorkerSubjectBatchSize}
			if waiter, ok := localStorage.(marketstorage.ResampleViewSyncWaiter); ok {
				catalog.ViewSync = waiter
			} else {
				log.Warn("collector kline resample disabled: Storage adapter has no View sync waiter")
			}
			for _, spaceID := range spaceIDs {
				resampleRunner = &collectorresample.Runner{Tasks: dbm.Tasks(), Instances: dbm.TaskInstances(), Readiness: dbm.PeriodReadiness(), Source: metadataSource, Primary: localStorage, Config: collectorresample.RunnerConfig{
					SpaceID: spaceID, ScanTimeout: cfg.KlineResample.ScanTimeout, WorkerConcurrency: cfg.KlineResample.WorkerConcurrency, MaxClaimsPerTick: cfg.KlineResample.MaxClaimsPerTick, WorkerJobTimeout: cfg.KlineResample.WorkerJobTimeout,
					WorkerPollInterval: cfg.KlineResample.WorkerPollInterval, WorkerMaxSourceKeys: cfg.KlineResample.WorkerMaxSourceKeysPerClaim,
					StaleRunningAfter: cfg.KlineResample.StaleRunningAfter, DefaultSettleDelay: cfg.KlineResample.DefaultSettleDelay, RepairLookbackBuckets: cfg.KlineResample.RepairLookbackBuckets,
				}, Metrics: resampleMetrics}
				resampleRunners = append(resampleRunners, resampleRunner)
			}
			prepareCtx, prepareCancel := context.WithTimeout(process.ctx, cfg.KlineResample.ScanTimeout)
			if err := resamplePreparer.RunOnce(prepareCtx); err != nil {
				log.WarnContextf(process.ctx, "collector kline resample initial preparation failed: %v", err)
			}
			prepareCancel()
		}
	}
	type marketFetchRuntime struct {
		spaceID              string
		timerOwned           bool
		reconciler           *marketfetch.Reconciler
		scheduler            *marketfetch.Scheduler
		periodStorageCleanup *marketfetch.PeriodStorageReconciler
		tickRunning          atomic.Bool
	}
	runtimes := make([]marketFetchRuntime, 0, len(spaceIDs))
	for _, spaceID := range spaceIDs {
		stockCNTimerOwned := strings.EqualFold(spaceID, marketfetch.StockCNSpaceID)
		invokeConcurrency := marketFetchInvokeConcurrency(spaceID)
		maintenanceBatchLimit, recoveryBatchLimit := marketFetchMaintenanceLimits(spaceID, invokeConcurrency, cfg.StockCN.ExpectedTimerFunctionCount)
		reconciler := &marketfetch.Reconciler{
			SCFRegionBlacklists: cfg.SCFRegionBlacklists,
			ResolveSourceID:     marketwiring.DefaultSourceID,
			ResolveSymbol:       marketwiring.ResolveSymbol,
			AccessEnvironment:   cfg.scfAccessEnvironment,
			Tasks:               dbm.Tasks(), Symbols: plannerSource, Nodes: invoker, Instances: dbm.TaskInstances(), DNS: dnsCache,
			Metrics: metrics, MaxSubjects: marketfetch.DefaultMaxSubjects(spaceID),
			ExpectedStockCNTimerFunctions: cfg.StockCN.ExpectedTimerFunctionCount,
			MeasuredSafeGroupSize:         cfg.StockCN.MeasuredSafeGroupSize,
			StockCNStagger: marketfetch.StockCNStaggerConfig{
				StartSecond:        cfg.StockCN.StaggerStartSecond,
				WindowSeconds:      cfg.StockCN.StaggerWindowSeconds,
				MaxStartsPerSecond: cfg.StockCN.StaggerMaxStartsPerSecond,
			},
			DisableTimerTriggers: !stockCNTimerOwned,
		}
		invokeScheduler := &marketfetch.Scheduler{
			SCFRegionBlacklists: cfg.SCFRegionBlacklists,
			ResolveSymbol:       marketwiring.ResolveSymbol,
			ResolveSourceID:     marketwiring.DefaultSourceID,
			Tasks:               dbm.Tasks(), Instances: dbm.TaskInstances(), Batches: dbm.FetchBatches(), Runs: dbm.Runs(), Retries: dbm.FetchRetries(), PeriodSeriesSnapshot: dbm.PeriodSeriesSnapshot(), PeriodStorageStates: dbm.PeriodStorageStates(),
			// Internal Storage uses the process gateway client. The external
			// SCF payload keeps its discovered public target until E2.
			Lifetime: ctx, Invoker: invoker, Storage: newStorage,
			InvokeConcurrency: invokeConcurrency, MaintenanceBatchLimit: maintenanceBatchLimit, MaintenanceRecoveryBatchLimit: recoveryBatchLimit,
			MaxRetryAttempts: 3, Metrics: metrics, SpaceID: spaceID, DNSCache: dnsCache,
			Symbols:                    plannerSource,
			InvokeNonRealtimeOnly:      stockCNTimerOwned,
			TimerMeasuredSafeGroupSize: cfg.StockCN.MeasuredSafeGroupSize,
		}
		if stockCNTimerOwned {
			calendar, calendarErr := stockmarket.LoadCalendar("./config/markets/stockcn/calendar.yaml")
			if calendarErr != nil {
				log.ErrorContextf(process.ctx, "collector StockCN Timer planning is fail-closed because the market calendar could not be loaded: %v", calendarErr)
			}
			timerPlanner := &marketfetch.TimerPeriodPlanner{
				Snapshots: dbm.PeriodSeriesSnapshot(), States: dbm.PeriodStorageStates(), Batches: dbm.TimerPeriodBatches(),
				ValidTargetDataTime: stockCNTargetDataTimeValidator(calendar, cfg.KlineResample.DefaultSettleDelay, nil),
			}
			invokeScheduler.TimerPeriodBatches = dbm.TimerPeriodBatches()
			invokeScheduler.TimerPeriodPlanner = timerPlanner
			invokeScheduler.TimerAssignments = reconciler.TimerAssignments
		}
		failureReporter := marketfetch.NewPeriodFailureReporter(dbm.FetchRetries(), newStorage, spaceID)
		failureReporter.SetMetrics(metrics)
		invokeScheduler.WakePeriodFailureReporter = failureReporter.Wake
		process.beforeClose = append(process.beforeClose, invokeScheduler.Close)
		if err := process.join(marketfetch.StartPeriodFailureReporterWithDone(ctx, failureReporter, spaceID, time.Second)); err != nil {
			log.WarnContextf(ctx, "collector permanent period failure reporter disabled space=%s: %v", spaceID, err)
		}
		if err := process.join(marketfetch.StartCompletionConsumerWithDone(ctx, spaceID, dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), metrics, failureReporter.Wake)); err != nil {
			log.WarnContextf(process.ctx, "collector market fetch completion consumer disabled space=%s: %v", spaceID, err)
		}
		if err := process.join(marketfetch.StartStorageWriteConsumerWithDone(ctx, spaceID, dbm.TaskInstances())); err != nil {
			log.WarnContextf(process.ctx, "collector storage write consumer disabled space=%s: %v", spaceID, err)
		}
		runtimes = append(runtimes, marketFetchRuntime{spaceID: spaceID, timerOwned: stockCNTimerOwned, reconciler: reconciler, scheduler: invokeScheduler})
	}
	maintenanceSpaceCursor := 0
	executionSweeps := make(map[string]*executionSweep, len(runtimes))
	retentionConfig := cfg.CollectorRetention
	maintenanceRunner := marketfetch.NewMaintenanceRunner(retentionConfig.interval(), retentionConfig.offset(), retentionConfig.timeout(), func(passCtx context.Context) error {
		now := time.Now().UTC()
		var passErrors []error
		for index := range runtimes {
			metrics.ObserveMaintenanceDeletes(runtimes[index].spaceID, nil)
		}
		if len(runtimes) > 0 {
			budgets := collectorMaintenanceBudgets(retentionConfig.MaxRowsPerPass)
			start := maintenanceSpaceCursor % len(runtimes)
			maintenanceSpaceCursor = nextMaintenanceSpaceCursor(start, len(runtimes))
			for offset := 0; offset < len(runtimes); offset++ {
				if passCtx.Err() != nil {
					break
				}
				runtimeIndex := (start + offset) % len(runtimes)
				runtime := &runtimes[runtimeIndex]
				deletedRows := make(map[string]int64)
				writeTargetBudget := maintenanceBudget(budgets.writeTargets, len(runtimes), offset)
				if runtime.scheduler != nil {
					prunedTargets, err := runtime.scheduler.RunMaintenance(passCtx, runtime.spaceID, writeTargetBudget)
					deletedRows["write_targets"] += prunedTargets
					writeTargetBudget -= int(prunedTargets)
					if err != nil {
						passErrors = append(passErrors, fmt.Errorf("scheduler maintenance for space %s: %w", runtime.spaceID, err))
					}
				}
				if passCtx.Err() != nil {
					break
				}
				executionCutoff := now.Add(-retentionConfig.duration(retentionConfig.ExecutionDetailRetention))
				itemsDeleted, batchesDeleted, err := drainMaintenancePagePairs(passCtx,
					maintenanceBudget(budgets.batchItems, len(runtimes), offset), maintenanceBudget(budgets.batches, len(runtimes), offset),
					maintenancePageRows, maintenanceBatchPageRows,
					func(ctx context.Context, items, batches int) (int64, int64, error) {
						return dbm.FetchBatches().CleanupSpaceWithCounts(ctx, runtime.spaceID, executionCutoff, items, batches)
					})
				if err != nil {
					passErrors = append(passErrors, fmt.Errorf("cleanup terminal FetchBatches for space %s: %w", runtime.spaceID, err))
				}
				deletedRows["batch_items"], deletedRows["batches"] = itemsDeleted, batchesDeleted
				retriesDeleted, err := drainMaintenancePages(passCtx, maintenanceBudget(budgets.retries, len(runtimes), offset), maintenancePageRows,
					func(ctx context.Context, limit int) (int64, error) {
						return dbm.FetchRetries().CleanupSpaceWithCount(ctx, runtime.spaceID, now.Add(-retentionConfig.duration(retentionConfig.TerminalRetryRetention)), limit)
					})
				if err != nil {
					passErrors = append(passErrors, fmt.Errorf("cleanup terminal FetchRetries for space %s: %w", runtime.spaceID, err))
				}
				deletedRows["retry"] = retriesDeleted
				sweep := executionSweeps[runtime.spaceID]
				if sweep == nil {
					sweep = &executionSweep{}
					executionSweeps[runtime.spaceID] = sweep
				}
				targetsDeleted, instancesDeleted, err := sweepScheduledExecutionDetails(passCtx, dbm.TaskInstances(), sweep, runtime.spaceID,
					executionCutoff, executionSweepDeadline(passCtx, time.Now(), retentionConfig.timeout()/4, len(runtimes)-offset),
					writeTargetBudget, maintenanceBudget(budgets.instances, len(runtimes), offset))
				if err != nil {
					passErrors = append(passErrors, fmt.Errorf("cleanup scheduled execution details for space %s: %w", runtime.spaceID, err))
				} else {
					deletedRows["write_targets"] += targetsDeleted
					deletedRows["instances"] = instancesDeleted
					if targetsDeleted+instancesDeleted > 0 {
						log.Infof("cleaned scheduled execution details space=%s write_targets=%d task_instances=%d", runtime.spaceID, targetsDeleted, instancesDeleted)
					}
				}
				if runsDeleted, err := drainMaintenancePages(passCtx, maintenanceBudget(budgets.runs, len(runtimes), offset), maintenancePageRows,
					func(ctx context.Context, limit int) (int64, error) {
						return dbm.Runs().CleanupScheduledTerminalSpace(ctx, runtime.spaceID, now.Add(-retentionConfig.duration(retentionConfig.ScheduledRunSummaryRetention)), limit)
					}); err != nil {
					passErrors = append(passErrors, fmt.Errorf("cleanup terminal scheduled Runs for space %s: %w", runtime.spaceID, err))
				} else {
					deletedRows["runs"] = runsDeleted
				}
				readinessDeleted, err := dbm.PeriodReadiness().CleanupReportedRetentionInSpace(
					passCtx,
					runtime.spaceID,
					now.Add(-cfg.PeriodReadiness.ParentRetention),
					cfg.PeriodReadiness.ItemRetention,
					maintenanceBudget(budgets.periodReadiness, len(runtimes), offset),
				)
				if err != nil {
					passErrors = append(passErrors, fmt.Errorf("cleanup reported PeriodReadiness for space %s: %w", runtime.spaceID, err))
				} else {
					deletedRows["period_readiness"] = readinessDeleted
				}
				if runtime.periodStorageCleanup == nil {
					if err := observeCollectorMaintenanceSpace(passCtx, dbm, metrics, runtime.spaceID, now.Add(-retentionConfig.duration(retentionConfig.PeriodSnapshotRetention)), now, deletedRows); err != nil {
						log.WarnContextf(passCtx, "Collector store metrics refresh failed space=%s: %v", runtime.spaceID, err)
					}
					continue
				}
				periodDeleted, err := runtime.periodStorageCleanup.ReconcileWithCleanupCounts(
					passCtx, now, retentionConfig.duration(retentionConfig.PeriodSnapshotRetention),
					maintenanceBudget(budgets.periodSnapshots, len(runtimes), offset),
					maintenanceBudget(budgets.periodManifests, len(runtimes), offset),
				)
				deletedRows["period_snapshot"] = periodDeleted.SnapshotRows
				deletedRows["period_state"] = periodDeleted.StateRows
				deletedRows["period_manifest"] = periodDeleted.ManifestRows
				if err != nil {
					passErrors = append(passErrors, fmt.Errorf("reconcile Storage periods for space %s: %w", runtime.spaceID, err))
				} else {
					if periodDeleted.Total() > 0 {
						log.Infof("cleaned terminal Collector period rows space=%s snapshot_rows=%d state_rows=%d manifest_rows=%d", runtime.spaceID, periodDeleted.SnapshotRows, periodDeleted.StateRows, periodDeleted.ManifestRows)
					}
				}
				if err := observeCollectorMaintenanceSpace(passCtx, dbm, metrics, runtime.spaceID, now.Add(-retentionConfig.duration(retentionConfig.PeriodSnapshotRetention)), now, deletedRows); err != nil {
					log.WarnContextf(passCtx, "Collector store metrics refresh failed space=%s: %v", runtime.spaceID, err)
				}
			}
		}
		return errors.Join(passErrors...)
	})
	maintenanceRunner.Metrics = metrics
	// Resampling owns a dedicated minute timer. Its callback only schedules a
	// bounded background scan, so slow source/target Storage I/O cannot delay the
	// existing SCF coordination timer.
	resampleService := s.Service("trpc.moox.collector.kline_resample.timer")
	if resampleService == nil {
		if len(resampleRunners) > 0 || resamplePreparer != nil {
			log.Warn("collector kline resample timer service is not configured, skip register")
		}
	} else {
		var resampleTickRunning atomic.Bool
		timer.RegisterHandlerService(resampleService, func(ctx context.Context) error {
			if len(resampleRunners) == 0 && resamplePreparer == nil {
				return nil
			}
			if !resampleTickRunning.CompareAndSwap(false, true) {
				log.WarnContextf(ctx, "collector kline resample timer tick skipped because the previous tick is still running")
				return nil
			}
			process.launch(func() {
				defer resampleTickRunning.Store(false)
				tickCtx := process.ctx
				if resamplePreparer != nil {
					prepareCtx, prepareCancel := context.WithTimeout(tickCtx, cfg.KlineResample.ScanTimeout)
					if err := resamplePreparer.RunOnce(prepareCtx); err != nil {
						log.WarnContextf(tickCtx, "collector kline resample preparation failed: %v", err)
					}
					prepareCancel()
				}
				for _, runner := range resampleRunners {
					if err := runner.Tick(tickCtx, time.Now().UTC()); err != nil {
						log.WarnContextf(tickCtx, "collector kline resample tick failed space=%s: %v", runner.Config.SpaceID, err)
					}
				}
			})
			return nil
		})
	}
	for index := range runtimes {
		runtime := &runtimes[index]
		periodMarketType := "spot"
		if strings.EqualFold(runtime.spaceID, marketfetch.StockCNSpaceID) {
			periodMarketType = "equity"
		}
		periodStorage, storageErr := newStorage(periodMarketType, "collector")
		if storageErr != nil {
			log.WarnContextf(process.ctx, "collector Storage period reporter and cleanup disabled space=%s: %v", runtime.spaceID, storageErr)
			continue
		}
		if statusClient, ok := periodStorage.(marketfetch.PeriodStorageStatusClient); ok {
			runtime.periodStorageCleanup = marketfetch.NewPeriodStorageReconciler(dbm.PeriodSeriesSnapshot(), dbm.PeriodStorageStates(), statusClient, runtime.spaceID, dbm.TimerPeriodBatches())
			runtime.periodStorageCleanup.Metrics = metrics
		} else {
			log.WarnContextf(process.ctx, "collector Storage adapter does not support period status queries; cleanup disabled space=%s", runtime.spaceID)
		}
		reporter, ok := periodStorage.(marketfetch.DatasetPeriodReporter)
		if !ok {
			log.WarnContextf(process.ctx, "collector Storage adapter does not support period readiness reports space=%s", runtime.spaceID)
			continue
		}
		// The legacy readiness repository remains only for local resample jobs.
		// Direct market-fetch Dataset completion is finalized inside Storage
		// DataNode and no longer waits for DatasetRowsUpserted replay.
		periodReporter := marketfetch.NewPeriodReporter(dbm.PeriodReadiness(), reporter, runtime.spaceID)
		periodReporter.SetMetrics(metrics)
		if err := process.join(marketfetch.StartPeriodReporterWithDone(ctx, periodReporter, cfg.PeriodReadiness.ReportInterval)); err != nil {
			log.WarnContextf(process.ctx, "collector resample period reporter disabled space=%s: %v", runtime.spaceID, err)
		}
	}
	// The maintenance pass reads each runtime's periodStorageCleanup field.
	// Start only after all Space-specific Storage clients have been assigned.
	if err := process.join(maintenanceRunner.StartWithDone(ctx)); err != nil {
		return
	}
	if service == nil {
		return
	}
	timer.RegisterScheduler("collectorMarketFetch", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		for index := range runtimes {
			runtime := &runtimes[index]
			if !runtime.tickRunning.CompareAndSwap(false, true) {
				log.WarnContextf(ctx, "collector market fetch timer tick skipped because the previous tick is still running space=%s", runtime.spaceID)
				continue
			}
			process.launch(func() {
				defer runtime.tickRunning.Store(false)
				spaceID := runtime.spaceID
				if runtime.timerOwned {
					reconcileCtx, reconcileCancel := context.WithTimeout(process.ctx, marketFetchReconcileTimeout)
					if err := runtime.reconciler.Reconcile(reconcileCtx, spaceID); err != nil {
						if strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
							log.WarnContextf(reconcileCtx, "collector SCF timer reconciliation failed space=%s expected_timer_function_count=%d measured_safe_group_size=%d: %v", spaceID, cfg.StockCN.ExpectedTimerFunctionCount, cfg.StockCN.MeasuredSafeGroupSize, err)
						} else {
							log.WarnContextf(reconcileCtx, "collector SCF timer reconciliation failed space=%s: %v", spaceID, err)
						}
					}
					reconcileCancel()
				}
				scheduleCtx, scheduleCancel := context.WithTimeout(process.ctx, marketFetchScheduleTimeout)
				if err := runtime.scheduler.Tick(scheduleCtx, spaceID); err != nil {
					log.WarnContextf(scheduleCtx, "collector invoke scheduler failed space=%s: %v", spaceID, err)
				}
				scheduleCancel()
			})
		}
		return nil
	})
}

func observeCollectorMaintenanceSpace(ctx context.Context, db *store.Store, metrics *marketfetch.Metrics, spaceID string, before, now time.Time, deleted map[string]int64) error {
	metrics.ObserveMaintenanceDeletes(spaceID, deleted)
	statsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return metrics.RefreshOperationalStats(statsCtx, db, spaceID, before, now)
}

// executionSweepDeadline gives one Space an equal share of the pass time that
// is left, keeping `reserve` for the steps after the sweep (Runs, readiness,
// and Storage period reconciliation).
func executionSweepDeadline(ctx context.Context, now time.Time, reserve time.Duration, spacesLeft int) time.Time {
	passDeadline, ok := ctx.Deadline()
	if !ok || spacesLeft <= 0 {
		return now
	}
	available := passDeadline.Sub(now) - reserve
	if available <= 0 {
		return now
	}
	return now.Add(available / time.Duration(spacesLeft))
}

func maintenanceBudget(total, spaces, index int) int {
	if total <= 0 || spaces <= 0 || index < 0 || index >= spaces {
		return 0
	}
	base, remainder := total/spaces, total%spaces
	if index < remainder {
		base++
	}
	return base
}

func nextMaintenanceSpaceCursor(current, spaces int) int {
	if spaces <= 0 {
		return 0
	}
	return (current + 1) % spaces
}

type collectorMaintenanceRowBudget struct {
	batchItems      int
	batches         int
	retries         int
	writeTargets    int
	instances       int
	runs            int
	periodReadiness int
	periodSnapshots int
	periodManifests int
}

func (b collectorMaintenanceRowBudget) total() int {
	return b.batchItems + b.batches + b.retries + b.writeTargets + b.instances + b.runs + b.periodReadiness + b.periodSnapshots + b.periodManifests
}

func collectorMaintenanceBudgets(total int) collectorMaintenanceRowBudget {
	if total < 9 {
		return collectorMaintenanceRowBudget{}
	}
	const weightTotal = 50000 - 9
	remainingRows := total - 9
	budget := collectorMaintenanceRowBudget{
		batchItems:      1 + remainingRows*(11500-1)/weightTotal,
		batches:         1 + remainingRows*(500-1)/weightTotal,
		retries:         1 + remainingRows*(10000-1)/weightTotal,
		writeTargets:    1 + remainingRows*(12000-1)/weightTotal,
		instances:       1 + remainingRows*(10000-1)/weightTotal,
		runs:            1 + remainingRows*(3000-1)/weightTotal,
		periodReadiness: 1 + remainingRows*(1000-1)/weightTotal,
		periodSnapshots: 1 + remainingRows*(1000-1)/weightTotal,
		periodManifests: 1 + remainingRows*(1000-1)/weightTotal,
	}
	remaining := total - budget.total()
	for index := 0; remaining > 0; index = (index + 1) % 9 {
		switch index {
		case 0:
			budget.batchItems++
		case 1:
			budget.batches++
		case 2:
			budget.retries++
		case 3:
			budget.writeTargets++
		case 4:
			budget.instances++
		case 5:
			budget.runs++
		case 6:
			budget.periodReadiness++
		case 7:
			budget.periodSnapshots++
		case 8:
			budget.periodManifests++
		}
		remaining--
	}
	return budget
}

func marketFetchInvokeConcurrency(spaceID string) int {
	if strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
		return marketfetch.StockCNTimerInvokeConcurrency
	}
	return marketfetch.DefaultInvokeConcurrency
}

func marketFetchMaintenanceLimits(spaceID string, invokeConcurrency, expectedTimerFunctionCount int) (dispatch, recovery int) {
	dispatch, recovery = invokeConcurrency, invokeConcurrency
	if !strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
		return dispatch, recovery
	}
	if expectedTimerFunctionCount <= 0 {
		return max(1, dispatch), max(1, recovery)
	}
	waveSize := max(1, expectedTimerFunctionCount)
	// Size one full maintenance pass to cover the steady-state failure wave;
	// faster passes are a bonus, not part of the capacity guarantee.
	denominator := marketfetch.RetryMaintenancePassesPerMinute * 4
	dispatch = (waveSize*3*5 + denominator - 1) / denominator
	recovery = (waveSize*4*5 + denominator - 1) / denominator
	return max(1, dispatch), max(1, recovery)
}

func marketFetchSpaceID() string {
	spaceIDs := marketFetchSpaceIDs()
	if len(spaceIDs) == 0 {
		return ""
	}
	return spaceIDs[0]
}

func marketFetchSpaceIDs() []string {
	raw := strings.TrimSpace(os.Getenv("MOOX_SPACE_IDS"))
	if raw == "" {
		raw = os.Getenv("MOOX_SPACE_ID")
	}
	return parseMarketFetchSpaceIDs(raw)
}

func parseMarketFetchSpaceIDs(raw string) []string {
	seen := make(map[string]struct{})
	spaceIDs := make([]string, 0, 2)
	for _, value := range strings.Split(raw, ",") {
		spaceID := strings.TrimSpace(value)
		if spaceID == "" {
			continue
		}
		key := strings.ToLower(spaceID)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		spaceIDs = append(spaceIDs, spaceID)
	}
	return spaceIDs
}

func stockCNTargetDataTimeValidator(calendar *stockmarket.Calendar, settleDelay time.Duration, now func() time.Time) func(string, time.Time) bool {
	return func(frequency string, target time.Time) bool {
		if calendar == nil || !strings.EqualFold(strings.TrimSpace(frequency), "1m") || calendar.Location() == nil {
			return false
		}
		current := time.Now().UTC()
		if now != nil {
			current = now().UTC()
		}
		latest, _, err := calendar.LatestClosedMinute(current, settleDelay)
		if err != nil || target.After(latest) {
			return false
		}
		tradeDate := target.In(calendar.Location()).Format("2006-01-02")
		bars, err := calendar.ExpectedMinuteBars(tradeDate)
		if err != nil {
			return false
		}
		for _, bar := range bars {
			if bar.Equal(target.UTC()) {
				return true
			}
		}
		return false
	}
}
