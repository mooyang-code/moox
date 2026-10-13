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
	"github.com/mooyang-code/moox/modules/collector/internal/marketstorage"
	collectorobservability "github.com/mooyang-code/moox/modules/collector/internal/observability"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresult "github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
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
	subjectSyncer, err := registerSubjectSyncRuntime(s, cfg, process)
	if err != nil {
		return nil, err
	}
	svc.SetSubjectSyncer(subjectSyncer)
	registerCollectorBackground(ctx, s, cfg, dbm, marketFetchMetrics, process)
	if err := registerFetchPlanManager(cfg, dbm, process, resultManager, marketFetchMetrics, svc); err != nil {
		return nil, err
	}
	if err := registerHealth(s, cfg, dbm, dnsSnapshot); err != nil {
		return nil, err
	}
	registerMetricsReporter(s, realtimeInventory)

	keepRuntime = true
	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return process, nil
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
			if err := refreshTaskDefinition(ctx, repo, &task); err != nil {
				// 哈希冲突等不影响采集：保留旧值，只告警。
				log.WarnContextf(ctx, "%v", err)
			}
		}
		if int64(page*store.MaxEnabledTasks) >= total || len(tasks) == 0 {
			break
		}
	}
	return nil
}

// refreshTaskDefinition 去掉存量任务 collect_params 里已删除的参数（如 history_policy），并按新的规范化形式重算定义哈希，
// 避免旧哈希让同义任务逃过去重、或让旧任务之后的更新被误判重复。
func refreshTaskDefinition(ctx context.Context, repo *store.TaskRepository, task *domain.CollectionTask) error {
	params := domain.DropRetiredParams(task.CollectParams)
	hash := collectsvc.CollectionTaskDefinitionHash(domain.CollectionTask{
		SpaceID: task.SpaceID, TagIDs: task.TagIDs, DataType: task.DataType, CollectParams: params,
	})
	if params == task.CollectParams && hash == task.DefinitionHash {
		return nil
	}
	task.CollectParams, task.DefinitionHash = params, hash
	if _, err := repo.UpdateValidatedTaskByTaskID(ctx, task.SpaceID, task.TaskID, *task); err != nil {
		return fmt.Errorf("refresh task %s/%s definition: %w", task.SpaceID, task.TaskID, err)
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
	h, err := report.NewHandler(report.DefaultConfig("collector", "collector"))
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

// registerCollectorBackground 启动 Collector 的后台任务：K 线重采样与保留期清理。
// 实时行情采集已全部由 SCF 定时器按采集计划触发（见 registerFetchPlanManager），这里不再有任何行情调度。
func registerCollectorBackground(ctx context.Context, s *server.Server, cfg *Config, dbm *store.Store, metrics *marketfetch.Metrics, process *Runtime) {
	if s == nil || cfg == nil || dbm == nil {
		return
	}
	metadataSource := storagesource.NewDatasetSource(storageio.NewGatewayClient(process.gateway))
	newStorage := func(marketType, writeSource string) (marketfetch.Storage, error) {
		if strings.EqualFold(strings.TrimSpace(marketType), "equity") || strings.EqualFold(strings.TrimSpace(marketType), marketfetch.StockCNSpaceID) {
			marketType = marketstorage.InstTypeSPOT
		}
		return marketstorage.NewGatewayBatchStorage(process.gateway, marketType, writeSource)
	}
	spaceIDs := marketFetchSpaceIDs()
	if len(spaceIDs) == 0 {
		log.Warn("collector background tasks have no configured spaces")
		return
	}
	var resampleRunners []*collectorresample.Runner
	var resamplePreparer *collectorresample.Preparer
	if cfg.KlineResample.Enabled {
		resampleMetrics := collectorresample.NewMetrics(prometheus.DefaultRegisterer)
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
				resampleRunners = append(resampleRunners, &collectorresample.Runner{Tasks: dbm.Tasks(), Instances: dbm.TaskInstances(), Readiness: dbm.PeriodReadiness(), Source: metadataSource, Primary: localStorage, Config: collectorresample.RunnerConfig{
					SpaceID: spaceID, ScanTimeout: cfg.KlineResample.ScanTimeout, WorkerConcurrency: cfg.KlineResample.WorkerConcurrency, MaxClaimsPerTick: cfg.KlineResample.MaxClaimsPerTick, WorkerJobTimeout: cfg.KlineResample.WorkerJobTimeout,
					WorkerPollInterval: cfg.KlineResample.WorkerPollInterval, WorkerMaxSourceKeys: cfg.KlineResample.WorkerMaxSourceKeysPerClaim,
					StaleRunningAfter: cfg.KlineResample.StaleRunningAfter, DefaultSettleDelay: cfg.KlineResample.DefaultSettleDelay, RepairLookbackBuckets: cfg.KlineResample.RepairLookbackBuckets,
				}, Metrics: resampleMetrics})
			}
			prepareCtx, prepareCancel := context.WithTimeout(process.ctx, cfg.KlineResample.ScanTimeout)
			if err := resamplePreparer.RunOnce(prepareCtx); err != nil {
				log.WarnContextf(process.ctx, "collector kline resample initial preparation failed: %v", err)
			}
			prepareCancel()
		}
	}
	maintenanceSpaceCursor := 0
	retentionConfig := cfg.CollectorRetention
	maintenanceRunner := marketfetch.NewMaintenanceRunner(retentionConfig.interval(), retentionConfig.offset(), retentionConfig.timeout(), func(passCtx context.Context) error {
		now := time.Now().UTC()
		var passErrors []error
		for _, spaceID := range spaceIDs {
			metrics.ObserveMaintenanceDeletes(spaceID, nil)
		}
		start := maintenanceSpaceCursor % len(spaceIDs)
		maintenanceSpaceCursor = nextMaintenanceSpaceCursor(start, len(spaceIDs))
		for offset := 0; offset < len(spaceIDs); offset++ {
			if passCtx.Err() != nil {
				break
			}
			spaceID := spaceIDs[(start+offset)%len(spaceIDs)]
			deletedRows := make(map[string]int64)
			readinessDeleted, err := dbm.PeriodReadiness().CleanupReportedRetentionInSpace(
				passCtx, spaceID, now.Add(-cfg.PeriodReadiness.ParentRetention), cfg.PeriodReadiness.ItemRetention,
				maintenanceBudget(retentionConfig.MaxRowsPerPass, len(spaceIDs), offset),
			)
			if err != nil {
				passErrors = append(passErrors, fmt.Errorf("cleanup reported PeriodReadiness for space %s: %w", spaceID, err))
			} else {
				deletedRows["period_readiness"] = readinessDeleted
			}
			if err := observeCollectorMaintenanceSpace(passCtx, dbm, metrics, spaceID, now, deletedRows); err != nil {
				log.WarnContextf(passCtx, "Collector store metrics refresh failed space=%s: %v", spaceID, err)
			}
		}
		return errors.Join(passErrors...)
	})
	maintenanceRunner.Metrics = metrics
	// 重采样使用独立的分钟定时器：回调只调度有界的后台扫描，源/目标 Storage 的慢 I/O 不会拖住别的定时任务。
	if resampleService := s.Service("trpc.moox.collector.kline_resample.timer"); resampleService == nil {
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
	// 重采样的周期就绪上报：仅用于本地重采样作业。
	for _, spaceID := range spaceIDs {
		periodMarketType := "spot"
		if strings.EqualFold(spaceID, marketfetch.StockCNSpaceID) {
			periodMarketType = "equity"
		}
		periodStorage, storageErr := newStorage(periodMarketType, "collector")
		if storageErr != nil {
			log.WarnContextf(process.ctx, "collector Storage period reporter disabled space=%s: %v", spaceID, storageErr)
			continue
		}
		reporter, ok := periodStorage.(marketfetch.DatasetPeriodReporter)
		if !ok {
			log.WarnContextf(process.ctx, "collector Storage adapter does not support period readiness reports space=%s", spaceID)
			continue
		}
		periodReporter := marketfetch.NewPeriodReporter(dbm.PeriodReadiness(), reporter, spaceID)
		periodReporter.SetMetrics(metrics)
		if err := process.join(marketfetch.StartPeriodReporterWithDone(ctx, periodReporter, cfg.PeriodReadiness.ReportInterval)); err != nil {
			log.WarnContextf(process.ctx, "collector resample period reporter disabled space=%s: %v", spaceID, err)
		}
	}
	if err := process.join(maintenanceRunner.StartWithDone(ctx)); err != nil {
		return
	}
}

func observeCollectorMaintenanceSpace(ctx context.Context, db *store.Store, metrics *marketfetch.Metrics, spaceID string, now time.Time, deleted map[string]int64) error {
	metrics.ObserveMaintenanceDeletes(spaceID, deleted)
	statsCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return metrics.RefreshOperationalStats(statsCtx, db, spaceID, now)
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
