// Package bootstrap wires the independent moox-monitor service process.
package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monitordoctor "github.com/mooyang-code/moox/modules/monitor/internal/doctor"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorsysdeploy "github.com/mooyang-code/moox/modules/monitor/internal/sysdeploy"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
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
	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	runtime := &Runtime{StartedAt: time.Now(), cancel: cancelRuntime, Store: mgr, Repositories: mgr.Repositories()}

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
			Notifier: alerting.WebhookNotifier{},
			Webhook: func(ctx context.Context, spaceID, webhookID string) (*domain.WebhookChannel, error) {
				return runtime.Repositories.Alerts.GetWebhook(ctx, spaceID, webhookID)
			},
		})
	} else {
		hostStore = hostmetrics.NewStore(nil, nil)
	}

	var metricsStorage *monmetrics.StorageAdapter
	var metricsQuery *monmetrics.QueryService
	var metricRules *monmetrics.MetricRuleStore
	var metricEvaluator *monmetrics.MetricEvaluator
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
		metricRules = metricStores.Rules
		metricEvaluator = monmetrics.NewMetricEvaluator(monmetrics.EvaluatorOptions{
			RuleStore: metricRules, Catalog: metricsQuery.Catalog(), Storage: metricsStorage,
			Webhook: func(ctx context.Context, spaceID, id string) (*domain.WebhookChannel, error) {
				return runtime.Repositories.Alerts.GetWebhook(ctx, spaceID, id)
			},
			Notifier: monmetrics.WebhookMetricNotifier{},
		})
	}
	if err := registerHealth(s, cfg, runtime, metricsStorage); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	resultHook := monitorResultHook(runtime)
	probeRunner := buildProbeRunner(cfg)
	syncSystem := monitorSyncFunc(runtimeCtx, s, cfg, runtime)
	hostReady := func() bool { return false }
	if hostGate != nil {
		hostReady = hostGate.Ready
	}
	pipelines, pipelineErr := report.ValidatePipelineEnvironment()
	if pipelineErr != nil {
		_ = runtime.Close()
		return nil, pipelineErr
	}
	doctorContext := &monitordoctor.Builder{
		Deployments: monitorsysdeploy.NewClientSource(cfg.SysDeploy.Target), Checks: runtime.Repositories.Checks, Results: runtime.Repositories.Results,
		Alerts: runtime.Repositories.Alerts, Metrics: metricsQuery, Hosts: hostStore, Pipelines: pipelines,
	}
	registerMonitorService(s, cfg, runtime, hostStore, hostReader, hostReady, probeRunner, resultHook, syncSystem, metricsQuery, metricRules, metricEvaluator, doctorContext)
	registerMetricsReporter(s, runtime)
	if err := registerMonitorDataCleanupTimer(s, cfg, runtime); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	if err := registerMonitorScheduleTimers(s, cfg, runtime, probeRunner, resultHook, metricEvaluator, metricRules); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	startHostMetricsConsumer(runtimeCtx, cfg, runtime, hostStore)
	startHostStorageGate(runtimeCtx, cfg, runtime, hostGate)
	startMetricsConsumer(runtimeCtx, cfg, runtime, metricsStorage)
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = runtime.Close()
		}()
	}

	log.InfoContextf(ctx, "moox-monitor 初始化完成")
	return s, nil
}
