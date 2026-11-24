// Package bootstrap wires the independent moox-monitor service process.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monitordeployment "github.com/mooyang-code/moox/modules/monitor/internal/deployment"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/storagegateway"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/report"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize 读 configPath 指向的业务配置（-config，部署时是 config/monitor/app.yaml 的绝对路径）并装配服务。
func Initialize(ctx context.Context, s *server.Server, configPath string) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	log.InfoContextf(ctx, "开始初始化 moox-monitor...")

	cfg, err := config.Load(configPath)
	if err != nil {
		log.ErrorContextf(ctx, "加载 monitor 配置失败: %v", err)
		return nil, err
	}
	mgr, err := store.OpenFromConfig(cfg.Database)
	if err != nil {
		log.ErrorContextf(ctx, "初始化 monitor 数据库失败: %v", err)
		return nil, err
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "初始化 monitor schema 失败: %v", err)
		return nil, err
	}
	// Monitor 自己的重要日志直接写库，不绕 EventBus；要赶在创建指标上报之前接上（先接入者生效）。
	attachOwnLogs(mgr.Repositories().Logs)
	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	runtime := &Runtime{StartedAt: time.Now(), cancel: cancelRuntime, Store: mgr, Repositories: mgr.Repositories()}
	runtime.Gateway, err = cfg.OpenGateway(func(err error) { log.WarnContextf(ctx, "monitor gateway directory refresh: %v", err) })
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	runtime.StorageGateway = storagegateway.New(runtime.Gateway)

	hostRegistry, err := store.WithDatabase(mgr, hostmetrics.NewRegistry)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	// 观测输入（EventBus 上的主机快照与服务指标）中断或刚恢复时，所有 last_seen 都是旧的：依赖它们的检查（含主机失联）全部冻结，只由"观测输入中断"这一条告警说明。
	observation := func(now time.Time) observationState { return runtime.observationState(cfg.Observability.Enabled, now) }

	hostSamples, err := store.WithDatabase(mgr, hostmetrics.NewSampleStore)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	runtime.HostSamples = hostSamples
	hostStore := hostmetrics.NewStore(hostSamples)
	hostStore.SetRegistry(hostRegistry)

	var storageMetadata *monmetrics.StorageAdapter
	var metricsQuery *monmetrics.QueryService
	if cfg.Metrics.Enabled {
		storageMetadata = monmetrics.NewStorageAdapter(runtime.StorageGateway)
		metricStores, err := store.WithDatabase(mgr, monmetrics.NewStores)
		if err != nil {
			_ = runtime.Close()
			return nil, err
		}
		runtime.MetricStores = metricStores
		metricStores.Messages.SetSeriesLimits(monmetrics.SeriesLimits{
			MaxNewPerHour: cfg.Metrics.MaxNewSeriesPerHour, MaxNewPerDay: cfg.Metrics.MaxNewSeriesPerDay,
		})
		metricsQuery = monmetrics.NewQueryService(metricStores.Messages, storageMetadata)
		metricsQuery.Catalog().SetNoDataAfter(time.Duration(maxInt(cfg.Metrics.NoDataIntervals, 1)) * monmetrics.ServiceReportInterval)
	}
	if err := registerHealth(runtimeCtx, s, cfg, runtime); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	// 三个评估域（主机、探针、业务）共用一个推进器：在检查定时器的同一个 goroutine 里顺序执行，互不删除对方的检查。
	applier := &checks.Applier{Checks: runtime.Repositories.Checks, Sender: runtime.alertSender, KlineInterval: cfg.KlineFreshness.EvaluationInterval}
	collectorStatus, err := monmetrics.NewCollectorInventoryGatewayClient(runtime.Gateway)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	// 行情探针先于业务评估执行，拿不到本轮概览：直接读各服务最近一次上报判断 Collector 是否暂停，读不到时照常跑探针。
	canaryHalted := func(ctx context.Context) bool {
		services, err := (monitorobservability.Builder{Metrics: metricsQuery, Topology: runtime.Repositories.Topology}).Services(ctx, time.Now().UTC())
		return err == nil && collectorHalted(services)
	}
	marketCanary, err := buildMonitorMarketCanary(cfg, runtime, applier, storageMetadata, collectorStatus, canaryHalted)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := retireMarketCanaries(runtimeCtx, cfg, applier); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("retire market canary checks: %w", err)
	}
	monitorSyncFunc(runtimeCtx, s, cfg, runtime)
	datasetHealthPolicy, policyErr := loadMonitorDatasetHealthPolicy(cfg)
	if policyErr != nil {
		_ = runtime.Close()
		return nil, policyErr
	}
	doctorContext := &monitordoctor.Builder{
		Deployments: monitordeployment.NewClientSource(runtime.Gateway),
		Services:    monitorobservability.Builder{Metrics: metricsQuery, Hosts: hostStore, Topology: runtime.Repositories.Topology},
		Alerts:      runtime.Repositories.Alerts, Metrics: metricsQuery, Hosts: hostStore,
		HealthChecks: report.BuiltInModuleHealthChecks(), DatasetHealthPolicy: datasetHealthPolicy,
	}
	marketFetchThresholds := monitorobservability.MarketFetchThresholds{
		CoordinationStaleAfter: cfg.MarketHealth.TimerCoordinationStaleAfter,
		PendingGrace:           cfg.MarketHealth.TimerCoordinationPendingGrace,
	}
	klineInventory, err := buildTaskResultInventory(cfg, runtime.Gateway)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if klineInventory != nil {
		// 最后一份有效的任务清单持久化：采集服务不可用或重启后仍可检查新鲜度。
		klineInventory.SetStore(store.NewInventoryRepository(runtime.Store))
	}
	klineFreshness := buildKlineFreshnessEvaluator(metricsQuery, cfg, klineInventory, doctorContext.DatasetHealthPolicy.RealtimeTimeSeries)
	if klineFreshness != nil {
		// 新上线对象的首次出现时间持久化：Monitor 重启后宽限期不重新计时，部署前的存量对象不给宽限。
		klineFreshness.SetSubjectFirstSeenStore(store.NewSubjectFirstSeenRepository(runtime.Store))
	}
	runtime.klineFreshness = klineFreshness
	factsBuilder := &monitorobservability.Builder{
		Metrics: metricsQuery, Hosts: hostStore,
		Topology:                   runtime.Repositories.Topology,
		Policy:                     doctorContext.DatasetHealthPolicy.RealtimeTimeSeries,
		BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold,
		MarketFetchThresholds:      marketFetchThresholds,
		Inventory:                  inventorySource(klineInventory),
		Datasets:                   archivableDatasets(storageMetadata),
	}
	businessFreshness := buildBusinessFreshnessReporterWithInterval(factsBuilder, runtime.Repositories, applier, observation, cfg.KlineFreshness.EvaluationInterval, klineFreshness)
	// 主机类检查在检查定时器每一轮的最前面执行（见 checkRound），先于行情探针和业务评估。
	hostChecksRun := buildHostCheckReporter(factsBuilder, applier, observation)
	runtime.businessEvaluation = businessFreshness != nil
	watchdogRun := func(watchdogCtx context.Context) error {
		var marketErr, freshnessErr error
		if marketCanary != nil {
			marketErr = marketCanary(watchdogCtx)
		}
		if businessFreshness != nil {
			if freshnessErr = businessFreshness(watchdogCtx); freshnessErr == nil {
				runtime.businessEvaluatedAt.Store(time.Now().UnixNano())
			}
		}
		// Timer-triggered market SCFs intentionally do not publish a per-batch
		// completion event. Their health is derived from Storage/Dataset freshness
		// and the Collector coordination checks, so an EventBus outage
		// must not create a false "missing completion" alert.
		return errors.Join(marketErr, freshnessErr)
	}
	health := &healthview.Builder{ComponentHealth: runtime.Repositories.ComponentHealth, Facts: &monitorobservability.Builder{Metrics: metricsQuery, Hosts: hostStore, Topology: runtime.Repositories.Topology, Policy: doctorContext.DatasetHealthPolicy.RealtimeTimeSeries, BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold, MarketFetchThresholds: marketFetchThresholds, Inventory: inventorySource(klineInventory), Datasets: archivableDatasets(storageMetadata)}, Checks: runtime.Repositories.Checks, Alerts: runtime.Repositories.Alerts, Notifications: runtime.Repositories.Notifications}
	registerMonitorService(s, cfg, runtime, hostStore, doctorContext, health)
	runtime.ModuleMetrics = registerMetricsReporter(s, runtime)
	if err := registerMonitorDataCleanupTimer(s, cfg, runtime); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := registerMonitorScheduleTimers(s, cfg, runtime, hostChecksRun, watchdogRun); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	startObservabilityConsumer(runtimeCtx, cfg, runtime, hostStore, klineInventory)
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = runtime.Close()
		}()
	}

	log.InfoContextf(ctx, "moox-monitor 初始化完成")
	return s, nil
}

// buildTaskResultInventory 构造采集任务清单缓存。清单声明了每个空间的市场交易日历，数据集新鲜度按它判断应收的最新一根，
// 所以不随 K 线新鲜度检查的开关关闭；开关只决定是否运行 K 线新鲜度检查。
func buildTaskResultInventory(cfg *config.Config, gateway gatewayclient.Invoker) (*monmetrics.TaskResultInventoryCache, error) {
	if cfg == nil {
		return nil, nil
	}
	client, err := monmetrics.NewCollectorInventoryGatewayClient(gateway)
	if err != nil {
		return nil, err
	}
	source, err := monmetrics.NewCollectorTaskResultInventorySource(client, cfg.KlineFreshness.SpaceIDs, cfg.KlineFreshness.InventoryPageSize, cfg.KlineFreshness.InventoryMaxEntries)
	if err != nil {
		return nil, fmt.Errorf("monitor collector inventory source: %w", err)
	}
	cache, err := monmetrics.NewTaskResultInventoryCache(source, cfg.KlineFreshness.InventoryRefreshInterval, cfg.KlineFreshness.InventoryMaxEntries)
	if err != nil {
		return nil, fmt.Errorf("monitor collector inventory cache: %w", err)
	}
	return cache, nil
}

// inventorySource 把可能为空的清单缓存转成接口：没有启用 K 线新鲜度时返回 nil，概览不按交易日历判断。
func inventorySource(cache *monmetrics.TaskResultInventoryCache) monitorobservability.InventorySource {
	if cache == nil {
		return nil
	}
	return cache
}

// archivableDatasets 在没有启用指标存储（没有 Storage 元数据客户端）时返回 nil，避免把空指针包进接口。
func archivableDatasets(adapter *monmetrics.StorageAdapter) monitorobservability.ArchivableDatasets {
	if adapter == nil {
		return nil
	}
	return adapter
}

func buildKlineFreshnessEvaluator(query *monmetrics.QueryService, cfg *config.Config, inventory *monmetrics.TaskResultInventoryCache, policy report.RealtimeTimeSeriesPolicy) *monmetrics.KlineFreshnessEvaluator {
	if query == nil || cfg == nil || !cfg.KlineFreshness.Enabled {
		return nil
	}
	return monmetrics.NewKlineFreshnessEvaluatorWithInventory(query, inventory, policy, cfg.KlineFreshness.MaxSubjectsPerAlert)
}

func loadMonitorDatasetHealthPolicy(cfg *config.Config) (report.DatasetHealthPolicy, error) {
	if strings.TrimSpace(os.Getenv("MOOX_DATASET_HEALTH_POLICY")) != "" {
		return report.ValidateDatasetHealthEnvironment()
	}
	if cfg == nil || strings.TrimSpace(cfg.Metrics.DatasetHealthPolicyPath) == "" {
		return report.DatasetHealthPolicy{}, fmt.Errorf("monitor Dataset health policy path is required")
	}
	return report.LoadDatasetHealthPolicy(cfg.Metrics.DatasetHealthPolicyPath)
}
