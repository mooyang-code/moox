// Package bootstrap wires the independent moox-monitor service process.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/healthview"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	monitorplacement "github.com/mooyang-code/moox/modules/monitor/internal/placement"
	"github.com/mooyang-code/moox/modules/monitor/internal/storagegateway"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/report"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	log.InfoContextf(ctx, "开始初始化 moox-monitor...")

	cfg, err := config.Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 monitor 配置失败: %v", err)
		return nil, err
	}
	mgr, err := store.OpenFromConfig(cfg.Database)
	if err != nil {
		log.ErrorContextf(ctx, "初始化 monitor 数据库失败: %v", err)
		return nil, err
	}
	// 部署配置里的推送地址只在首次建库时写入；之后渠道由监控页增删，删光了重启也不会自动加回来。
	channelsExisted, err := mgr.TableExists(domain.NotificationChannel{}.TableName())
	if err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "检查 monitor 数据库失败: %v", err)
		return nil, err
	}
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "初始化 monitor schema 失败: %v", err)
		return nil, err
	}
	if !channelsExisted {
		if err := seedNotificationChannel(ctx, mgr.Repositories()); err != nil {
			_ = mgr.Close()
			log.ErrorContextf(ctx, "写入初始通知渠道失败: %v", err)
			return nil, err
		}
	}
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

	var presenceFailureMu sync.Mutex
	presenceFailures := make(map[string]struct{})
	presenceNotificationFailure := func(_ context.Context, transition hostmetrics.PresenceTransition, sendErr error) {
		key := fmt.Sprintf("%s:%s:%s:%d", transition.AgentID, transition.From, transition.To, transition.ObservedAt.UnixNano())
		presenceFailureMu.Lock()
		if _, exists := presenceFailures[key]; exists {
			presenceFailureMu.Unlock()
			return
		}
		auditCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer func() { cancel(); presenceFailureMu.Unlock() }()
		err := runtime.Repositories.Alerts.CreateEvent(auditCtx, &domain.AlertEvent{
			EventID:   uuid.NewString(),
			SpaceID:   hostmetrics.SpaceID,
			RuleID:    hostmetrics.PresenceRuleID,
			CheckID:   hostmetrics.PresenceCheckID(transition.AgentID),
			EventType: domain.AlertEventSendFailed,
			Status:    domain.AlertStatusFiring,
			Message:   "主机状态通知发送失败：" + sendErr.Error(),
			CreatedAt: transition.ObservedAt,
		})
		if err != nil {
			log.ErrorContextf(auditCtx, "record host presence notification failure agent_id=%s: %v", transition.AgentID, err)
			return
		}
		if len(presenceFailures) >= 4096 {
			presenceFailures = make(map[string]struct{})
		}
		presenceFailures[key] = struct{}{}
	}
	presenceProvider := func(ctx context.Context) (notification.Sender, error) {
		return notifier.Sender(ctx, runtime.Repositories.Notifications)
	}
	// The scanner and sample path use separate failure callbacks so a failed
	// transition is audited exactly once.
	// 两条路径共用同一个按主机串行的投递门，保证上下线通知顺序与主机当前状态一致。
	presenceGate := newPresenceGate(func(ctx context.Context, agentID string) (string, error) {
		return hostRegistry.Status(ctx, agentID)
	})
	presenceGate.gatewayAlive = gatewayHeartbeatAlive(runtime.Repositories.Gateways)
	presenceSink := recordHostPresence(runtime.Repositories.Alerts, hostPresenceTransitionSinkWithGate(presenceProvider, nil, presenceGate))
	presenceScannerSink := recordHostPresence(runtime.Repositories.Alerts, hostPresenceTransitionSinkWithGate(presenceProvider, presenceNotificationFailure, presenceGate))
	hostSilence := hostmetrics.NewSilenceScanner(hostRegistry, hostmetrics.DefaultHostStaleAfter, presenceScannerSink)

	var hostStore *hostmetrics.Store
	var hostReader *hostmetrics.StorageReader
	var hostGate *hostmetrics.StorageGate
	var hostRuleCache *hostmetrics.RuleCache
	if cfg.Metrics.HostStorage.Enabled {
		hostAccess, hostMetadata := runtime.StorageGateway, runtime.StorageGateway
		hostWriter := hostmetrics.NewStorageWriter(hostAccess, cfg.Metrics.HostStorage)
		hostReader = hostmetrics.NewStorageReader(hostAccess, cfg.Metrics.HostStorage)
		hostGate = hostmetrics.NewStorageGate(hostMetadata, cfg.Metrics.HostStorage)
		hostStore = hostmetrics.NewStore(hostWriter, hostReader)
		hostStore.SetStorageReady(hostGate.Ready)
		hostRuleCache, err = hostmetrics.NewRuleCache(hostmetrics.RuleCacheOptions{Repository: runtime.Repositories.Alerts, RefreshInterval: cfg.Metrics.HostStorage.RuleRefreshInterval})
		if err != nil {
			_ = runtime.Close()
			return nil, err
		}
		runtime.HostRuleCache = hostRuleCache
		if err := hostRuleCache.Start(runtimeCtx); err != nil {
			log.WarnContextf(ctx, "host alert rule cache unavailable: %v", err)
		}
		hostStore.SetAlertEvaluator(&hostmetrics.AlertEvaluator{
			Cache: hostRuleCache, Repository: runtime.Repositories.Alerts,
			Notification: func(ctx context.Context) (notification.Sender, error) {
				return notifier.Sender(ctx, runtime.Repositories.Notifications)
			},
		})
	} else {
		hostStore = hostmetrics.NewStore(nil, nil)
	}
	hostStore.SetRegistry(hostRegistry)
	hostStore.SetPresenceTransitionSink(presenceSink)
	hostStore.SetPresenceTransitionFailureSink(presenceNotificationFailure)

	var metricsStorage *monmetrics.StorageAdapter
	var metricsQuery *monmetrics.QueryService
	if cfg.Metrics.Enabled {
		metricsStorage = monmetrics.NewStorageAdapter(runtime.StorageGateway, runtime.StorageGateway, cfg.Metrics.Storage)
		metricStores, err := store.WithDatabase(mgr, monmetrics.NewStores)
		if err != nil {
			_ = runtime.Close()
			return nil, err
		}
		runtime.MetricStores = metricStores
		metricsQuery = monmetrics.NewQueryService(metricStores.Messages, metricsStorage)
		if interval, err := time.ParseDuration(cfg.Metrics.Storage.Frequency); err == nil {
			metricsQuery.Catalog().SetNoDataAfter(time.Duration(maxInt(cfg.Metrics.NoDataIntervals, 1)) * interval)
		}
	}
	if err := registerHealth(s, cfg, runtime, metricsStorage, hostStore); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	resultHook := monitorResultHook(runtime)
	probeRunner := buildProbeRunner(cfg)
	marketCanary, marketCanaryProbe, err := buildMonitorMarketCanary(runtimeCtx, cfg, runtime, metricsStorage, resultHook)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if marketCanaryProbe != nil {
		probeCtx, cancelProbe := context.WithTimeout(runtimeCtx, 10*time.Second)
		probeErr := marketCanaryProbe(probeCtx)
		cancelProbe()
		if probeErr != nil {
			if watchdog.IsStorageAuthError(probeErr) {
				_ = runtime.Close()
				return nil, fmt.Errorf("monitor storage primary auth preflight failed; restart Monitor after synchronizing storage-internal-auth.env: %w", probeErr)
			}
			// Primary may still be starting or the dataset may be empty. The
			// periodic canary remains responsible for reporting those conditions.
			log.WarnContextf(ctx, "monitor storage primary auth preflight deferred: %v", probeErr)
		} else {
			log.InfoContextf(ctx, "monitor storage primary auth preflight passed")
		}
	}
	if err := retireObsoleteBusinessChecks(runtimeCtx, runtime.Repositories, cfg); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("retire obsolete monitor checks: %w", err)
	}
	if err := ensureDefaultCheckAlertRules(runtimeCtx, runtime.Repositories); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	syncSystem := monitorSyncFunc(runtimeCtx, s, cfg, runtime)
	hostReady := func() bool { return false }
	if hostGate != nil {
		hostReady = hostGate.Ready
	}
	datasetHealthPolicy, policyErr := loadMonitorDatasetHealthPolicy(cfg)
	if policyErr != nil {
		_ = runtime.Close()
		return nil, policyErr
	}
	doctorContext := &monitordoctor.Builder{
		Deployments: monitorplacement.NewClientSource(runtime.Gateway), Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results,
		Alerts: runtime.Repositories.Alerts, Metrics: metricsQuery, Hosts: hostStore,
		HealthChecks: report.BuiltInModuleHealthChecks(), DatasetHealthPolicy: datasetHealthPolicy,
	}
	marketFetchThresholds := monitorobservability.MarketFetchThresholds{
		CoordinationStaleAfter:      cfg.MarketHealth.TimerCoordinationStaleAfter,
		PendingGrace:                cfg.MarketHealth.TimerCoordinationPendingGrace,
		FeedFailureRateWindow:       cfg.MarketHealth.FeedFailureRateWindow,
		FeedFailureRateThreshold:    cfg.MarketHealth.FeedFailureRateThreshold,
		InstrumentSnapshotMaxAge:    cfg.MarketHealth.InstrumentSnapshotMaxAge,
		InstrumentMinimumCount:      cfg.MarketHealth.InstrumentMinimumCount,
		InstrumentRequiredExchanges: append([]string(nil), cfg.MarketHealth.InstrumentRequiredExchanges...),
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
	businessFreshness := buildBusinessFreshnessReporterWithInterval(&monitorobservability.Builder{
		Metrics: metricsQuery, Hosts: hostStore,
		Checks: runtime.Repositories.Checks, Topology: runtime.Repositories.Topology, Results: runtime.Repositories.Results, Gateways: runtime.Repositories.Gateways,
		Policy:                     doctorContext.DatasetHealthPolicy.RealtimeTimeSeries,
		BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold,
		MarketFetchThresholds:      marketFetchThresholds,
		Inventory:                  inventorySource(klineInventory),
	}, runtime.Repositories, resultHook, cfg.KlineFreshness.EvaluationInterval, klineFreshness)
	watchdogRun := func(watchdogCtx context.Context) error {
		var marketErr, freshnessErr error
		if marketCanary != nil {
			marketErr = marketCanary(watchdogCtx)
		}
		if businessFreshness != nil {
			freshnessErr = businessFreshness(watchdogCtx)
		}
		// Timer-triggered market SCFs intentionally do not publish a per-batch
		// completion event. Their health is derived from Storage/Dataset freshness
		// and the CloudNode/Collector coordination checks, so an EventBus outage
		// must not create a false "missing completion" alert.
		return errors.Join(marketErr, freshnessErr)
	}
	health := &healthview.Builder{ComponentHealth: runtime.Repositories.ComponentHealth, Facts: &monitorobservability.Builder{Metrics: metricsQuery, Hosts: hostStore, Checks: runtime.Repositories.Checks, Topology: runtime.Repositories.Topology, Results: runtime.Repositories.Results, Gateways: runtime.Repositories.Gateways, Policy: doctorContext.DatasetHealthPolicy.RealtimeTimeSeries, BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold, MarketFetchThresholds: marketFetchThresholds, Inventory: inventorySource(klineInventory)}, Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results, Alerts: runtime.Repositories.Alerts, Notifications: runtime.Repositories.Notifications}
	registerMonitorService(s, cfg, runtime, hostStore, hostReader, hostReady, probeRunner, resultHook, syncSystem, metricsQuery, doctorContext, health)
	runtime.ModuleMetrics = registerMetricsReporter(s, runtime)
	if err := registerMonitorDataCleanupTimer(s, cfg, runtime); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := registerMonitorScheduleTimers(s, cfg, runtime, probeRunner, resultHook, watchdogRun); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := registerMonitorHostSilenceTimer(s, hostSilence, func(timerCtx context.Context) error {
		return ensureDefaultHostAlertRules(timerCtx, runtime.Repositories, hostRegistry)
	}); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	startMetricsStorageGate(runtimeCtx, cfg, runtime, metricsStorage)
	startHostStorageGate(runtimeCtx, cfg, runtime, hostGate)
	startObservabilityConsumer(runtimeCtx, cfg, runtime, metricsStorage, hostStore, klineInventory)
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
