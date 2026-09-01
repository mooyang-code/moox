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
	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorsysdeploy "github.com/mooyang-code/moox/modules/monitor/internal/sysdeploy"
	"github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
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
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "初始化 monitor schema 失败: %v", err)
		return nil, err
	}
	if reset, err := mgr.ResetLegacyMonitorTables(); err != nil {
		_ = mgr.Close()
		return nil, fmt.Errorf("检查旧 monitor schema 失败: %w", err)
	} else if reset {
		if err := mgr.ApplySchema(schema.SQL()); err != nil {
			_ = mgr.Close()
			return nil, fmt.Errorf("重建 monitor schema 失败: %w", err)
		}
	}
	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	runtime := &Runtime{StartedAt: time.Now(), cancel: cancelRuntime, Store: mgr, Repositories: mgr.Repositories()}
	// This project has not shipped a compatibility migration. Remove retired
	// custom-check/metric storage and rows before seeding the code-owned model.
	if err := mgr.DropRetiredTables(); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("drop retired monitor tables: %w", err)
	}
	if _, err := runtime.Repositories.Alerts.PurgeRetiredRules(runtimeCtx); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("purge retired alert rules: %w", err)
	}
	if _, err := runtime.Repositories.Checks.PurgeRetiredChecks(runtimeCtx); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("purge retired monitor checks: %w", err)
	}
	hostRegistry, err := store.WithDatabase(mgr, hostmetrics.NewRegistry)
	if err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := hostRegistry.MigrateLegacyIDs(runtimeCtx); err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("migrate host agent identities: %w", err)
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
			RuleID:    "default:host-presence",
			CheckID:   "host:" + transition.AgentID + ":presence",
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
		channel, channelErr := runtime.Repositories.Notifications.GetGlobal(ctx)
		if channelErr != nil || channel == nil || strings.TrimSpace(channel.WebhookURL) == "" {
			return nil, channelErr
		}
		return notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channel.ChannelType), WebhookURL: channel.WebhookURL})
	}
	// The scanner and sample path use separate failure callbacks so a failed
	// transition is audited exactly once.
	presenceSink := hostPresenceTransitionSinkProviderWithFailure(presenceProvider, nil)
	presenceScannerSink := hostPresenceTransitionSinkProviderWithFailure(presenceProvider, presenceNotificationFailure)
	hostSilence := hostmetrics.NewSilenceScanner(hostRegistry, hostmetrics.DefaultHostStaleAfter, presenceScannerSink)

	var hostStore *hostmetrics.Store
	var hostReader *hostmetrics.StorageReader
	var hostGate *hostmetrics.StorageGate
	var hostRuleCache *hostmetrics.RuleCache
	if cfg.Metrics.HostStorage.Enabled {
		credentials, credErr := gatewayauth.ResolveCredentials(cfg.Metrics.HostStorage.KeyID, cfg.Metrics.HostStorage.HMACKeyFile)
		if credErr != nil {
			_ = runtime.Close()
			return nil, credErr
		}
		options := gatewayauth.NewTRPCClientOptions(normalizeHostStorageTarget(cfg.Metrics.HostStorage.GatewayTarget), cfg.Metrics.HostStorage.GatewayNodeID, credentials)
		hostAccess := storagepb.NewPrimaryStoreClientProxy(options...)
		hostMetadata := storagepb.NewMetadataClientProxy(options...)
		hostWriter := hostmetrics.NewStorageWriter(hostAccess, cfg.Metrics.HostStorage)
		hostReader = hostmetrics.NewStorageReader(hostAccess, cfg.Metrics.HostStorage)
		hostReader.SetAgentAliases(hostRegistry.Aliases)
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
			Notification: func(ctx context.Context) (*domain.NotificationChannel, error) {
				return runtime.Repositories.Notifications.GetGlobal(ctx)
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
		metricsStorage = monmetrics.NewStorageAdapterFromConfig(cfg.Metrics.Storage)
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
	marketCanary, marketCanaryProbe, err := buildMonitorMarketCanary(runtimeCtx, cfg, runtime, resultHook)
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
		Deployments: monitorsysdeploy.NewClientSource(cfg.SysDeploy.Target), Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results,
		Alerts: runtime.Repositories.Alerts, Metrics: metricsQuery, Hosts: hostStore,
		HealthChecks: report.BuiltInModuleHealthChecks(), DatasetHealthPolicy: datasetHealthPolicy,
	}
	marketFetchThresholds := monitorobservability.MarketFetchThresholds{
		CoordinationStaleAfter:      cfg.MarketHealth.TimerCoordinationStaleAfter,
		PendingGrace:                cfg.MarketHealth.TimerCoordinationPendingGrace,
		LowCapacityHeadroom:         cfg.MarketHealth.LowCapacityHeadroom,
		FeedFailureRateWindow:       cfg.MarketHealth.FeedFailureRateWindow,
		FeedFailureRateThreshold:    cfg.MarketHealth.FeedFailureRateThreshold,
		InstrumentSnapshotMaxAge:    cfg.MarketHealth.InstrumentSnapshotMaxAge,
		InstrumentMinimumCount:      cfg.MarketHealth.InstrumentMinimumCount,
		InstrumentRequiredExchanges: append([]string(nil), cfg.MarketHealth.InstrumentRequiredExchanges...),
	}
	businessFreshness := buildBusinessFreshnessReporter(&monitorobservability.Builder{
		Metrics: metricsQuery, Hosts: hostStore,
		Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results,
		Policy:                     doctorContext.DatasetHealthPolicy.RealtimeTimeSeries,
		BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold,
		MarketFetchThresholds:      marketFetchThresholds,
	}, runtime.Repositories, resultHook)
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
	health := &healthview.Builder{Facts: &monitorobservability.Builder{Metrics: metricsQuery, Hosts: hostStore, Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results, Policy: doctorContext.DatasetHealthPolicy.RealtimeTimeSeries, BalanceDifferenceThreshold: cfg.Observability.BalanceDifferenceThreshold, MarketFetchThresholds: marketFetchThresholds}, Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results, Alerts: runtime.Repositories.Alerts, Notifications: runtime.Repositories.Notifications}
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
	startObservabilityConsumer(runtimeCtx, cfg, runtime, metricsStorage, hostStore)
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = runtime.Close()
		}()
	}

	log.InfoContextf(ctx, "moox-monitor 初始化完成")
	return s, nil
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
