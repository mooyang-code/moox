// Package bootstrap wires the independent moox-monitor service process.
package bootstrap

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/monitor/internal/alerting"
	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/health"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	monitorpeer "github.com/mooyang-code/moox/modules/monitor/internal/peer"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	"github.com/mooyang-code/moox/modules/monitor/internal/scheduler"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorsysdeploy "github.com/mooyang-code/moox/modules/monitor/internal/sysdeploy"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Runtime owns monitor's process-scoped resources. Keeping these handles
// together avoids package globals and gives shutdown one explicit lifecycle.
type Runtime struct {
	StartedAt       time.Time
	cancel          context.CancelFunc
	workers         sync.WaitGroup
	closeOnce       sync.Once
	closeErr        error
	Store           *store.Store
	Repositories    *store.Repositories
	MetricStores    *monmetrics.Stores
	HostRuleCache   *hostmetrics.RuleCache
	Scheduler       *scheduler.Scheduler
	MetricScheduler *monmetrics.RuleScheduler
}

func (r *Runtime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		if r.Scheduler != nil {
			r.Scheduler.Stop()
		}
		if r.HostRuleCache != nil {
			_ = r.HostRuleCache.Stop(context.Background())
		}
		if r.MetricScheduler != nil {
			r.MetricScheduler.Stop()
		}
		r.workers.Wait()
		if r.Store != nil {
			r.closeErr = r.Store.Close()
		}
	})
	return r.closeErr
}

func (r *Runtime) Go(fn func()) {
	if r == nil || fn == nil {
		return
	}
	r.workers.Add(1)
	go func() {
		defer r.workers.Done()
		fn()
	}()
}

func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = context.Background()
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
	if err := mgr.EnsureMetricRuleStateColumns(); err != nil {
		_ = mgr.Close()
		log.ErrorContextf(ctx, "升级 monitor metric rule state schema 失败: %v", err)
		return nil, err
	}
	runtimeCtx, cancelRuntime := context.WithCancel(ctx)
	runtime := &Runtime{StartedAt: time.Now(), cancel: cancelRuntime, Store: mgr, Repositories: mgr.Repositories()}
	var hostStore *hostmetrics.Store
	var hostReader *hostmetrics.StorageReader
	var hostGate *hostmetrics.StorageGate
	var hostRuleCache *hostmetrics.RuleCache
	var hostAccess storagepb.AccessClientProxy
	if cfg.Metrics.HostStorage.Enabled {
		hostAccess = storagepb.NewAccessClientProxy(client.WithTarget(normalizeHostStorageTarget(cfg.Metrics.HostStorage.AccessTarget)))
		hostMetadata := storagepb.NewMetadataClientProxy(client.WithTarget(normalizeHostStorageTarget(cfg.Metrics.HostStorage.MetadataTarget)))
		hostWriter := hostmetrics.NewStorageWriter(hostAccess, cfg.Metrics.HostStorage)
		hostReader = hostmetrics.NewStorageReader(hostAccess, cfg.Metrics.HostStorage)
		hostGate = hostmetrics.NewStorageGate(hostMetadata, cfg.Metrics.HostStorage)
		hostStore = hostmetrics.NewStoreWithWriterReader(hostWriter, hostReader)
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
		hostStore.SetAlertEvaluator(&hostmetrics.AlertEvaluator{Cache: hostRuleCache, Repository: runtime.Repositories.Alerts, InstanceID: cfg.Instance.InstanceID, Notifier: alerting.WebhookNotifier{}, Webhook: func(ctx context.Context, spaceID, webhookID string) (*domain.WebhookChannel, error) {
			return runtime.Repositories.Alerts.GetWebhook(ctx, spaceID, webhookID)
		}})
	} else {
		hostStore = hostmetrics.NewStoreWithWriter(nil)
	}
	if err := hostStore.EnsureSchema(); err != nil {
		_ = runtime.Close()
		return nil, err
	}
	var metricsStorage *monmetrics.StorageAdapter
	var metricsQuery *monmetrics.QueryService
	var metricRules *monmetrics.MetricRuleStore
	var metricStores *monmetrics.Stores
	var metricEvaluator *monmetrics.MetricEvaluator
	if cfg.Metrics.Enabled {
		metricsStorage = monmetrics.NewStorageAdapterFromConfig(cfg.Metrics.Storage)
		metricStores, err = store.WithDatabase(mgr, monmetrics.NewStores)
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
			RuleStore:  metricRules,
			Catalog:    metricsQuery.Catalog(),
			Storage:    metricsStorage,
			InstanceID: cfg.Instance.InstanceID,
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
	resultHook := monitorResultHook(cfg, runtime)
	syncSystem := monitorSyncFunc(runtimeCtx, cfg, runtime)
	var hostReady func() bool
	if hostGate != nil {
		hostReady = hostGate.Ready
	} else {
		hostReady = func() bool { return false }
	}
	registerMonitorService(s, cfg, runtime, hostStore, hostReader, hostReady, resultHook, syncSystem, metricsQuery, metricRules, metricEvaluator)
	registerMetricsReporter(s)
	runtime.Scheduler = startScheduler(runtimeCtx, cfg, runtime, resultHook)
	startRetentionCleaner(runtimeCtx, cfg, runtime)
	startPeerPuller(runtimeCtx, cfg, runtime)
	startHostMetricsConsumer(runtimeCtx, cfg, runtime, hostStore)
	startHostStorageGate(runtimeCtx, cfg, runtime, hostGate)
	startMetricsConsumer(runtimeCtx, cfg, runtime, metricsStorage)
	if metricEvaluator != nil {
		runtime.MetricScheduler = monmetrics.NewRuleScheduler(monmetrics.SchedulerOptions{Evaluator: metricEvaluator, Rules: metricRules, InstanceID: cfg.Instance.InstanceID, ReloadInterval: time.Duration(cfg.Scheduler.ReloadIntervalSeconds) * time.Second, ActiveInstances: func(ctx context.Context) ([]string, error) {
			return activeMonitorInstanceIDs(ctx, cfg.Instance.InstanceID, runtime.Repositories.Peers, 3*time.Duration(cfg.Peer.TimeoutSeconds)*time.Second), nil
		}})
		runtime.MetricScheduler.Start(runtimeCtx)
	}
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = runtime.Close()
		}()
	}

	log.InfoContextf(ctx, "moox-monitor 初始化完成")
	return s, nil
}

func startHostStorageGate(ctx context.Context, cfg *config.Config, runtime *Runtime, gate *hostmetrics.StorageGate) {
	if cfg == nil || gate == nil || !cfg.Metrics.HostStorage.Enabled {
		return
	}
	interval := cfg.Metrics.HostStorage.MetadataRefreshInterval
	if interval <= 0 {
		interval = time.Minute
	}
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()
		if err := gate.Validate(checkCtx); err != nil {
			log.WarnContextf(ctx, "host storage schema check failed: %v", err)
		}
	}
	runtime.Go(func() {
		check()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			check()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func startHostMetricsConsumer(ctx context.Context, cfg *config.Config, runtime *Runtime, store *hostmetrics.Store) {
	if cfg == nil || !cfg.Metrics.Enabled || !cfg.Metrics.HostStorage.Enabled || store == nil {
		return
	}
	runtime.Go(func() {
		for ctx.Err() == nil {
			urls := strings.Split(cfg.Metrics.EventBusURL, ",")
			jc := jetstream.ConfigFromEnv(urls, "moox-monitor-hostmetrics")
			if path := strings.TrimSpace(cfg.Metrics.EventBusCredentialFile); path != "" {
				if raw, err := jetstream.LoadCredentialFile(jetstream.ExpandCredentialPath(path)); err == nil {
					jc.Username, jc.Password, jc.TLSCAFile = raw.Username, raw.Password, raw.CAFile
				} else {
					log.WarnContextf(ctx, "host metrics credential unavailable: %v", err)
					waitHostMetrics(ctx)
					continue
				}
			}
			client, err := jetstream.Connect(ctx, jc)
			if err != nil {
				log.WarnContextf(ctx, "host metrics eventbus unavailable: %v", err)
				waitHostMetrics(ctx)
				continue
			}
			consumer, err := hostmetrics.BindWithDLQ(ctx, client, store, hostmetrics.NewDLQPublisher(client))
			if err != nil {
				_ = client.Close()
				log.WarnContextf(ctx, "host metrics durable unavailable: %v", err)
				waitHostMetrics(ctx)
				continue
			}
			err = consumer.Run(ctx)
			_ = consumer.Close()
			_ = client.Close()
			if err != nil && ctx.Err() == nil {
				log.WarnContextf(ctx, "host metrics consumer stopped: %v", err)
				waitHostMetrics(ctx)
			}
		}
	})
}
func waitHostMetrics(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(15 * time.Second):
	}
}

func normalizeHostStorageTarget(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:20102"
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	return "ip://" + raw
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("moox_monitor"))
	if err != nil {
		log.WarnContextf(context.Background(), "monitor metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.monitor.metrics.timer")
	if service == nil {
		log.Warn("monitor metrics timer service is not configured, skip register")
		return
	}
	// Deliberately no scheduler/startAtOnce: an unavailable EventBus must not
	// block monitor startup, and every replica owns this local timer.
	timer.RegisterHandlerService(service, h.Handle)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func startMetricsConsumer(ctx context.Context, cfg *config.Config, runtime *Runtime, storage *monmetrics.StorageAdapter) {
	if cfg == nil || !cfg.Metrics.Enabled || runtime == nil || runtime.Store == nil || runtime.Store.Ping(ctx) != nil || runtime.MetricStores == nil {
		return
	}
	if storage == nil {
		storage = monmetrics.NewStorageAdapterFromConfig(cfg.Metrics.Storage)
	}
	repo := runtime.MetricStores.Messages
	startMetricsDedupeCleaner(ctx, runtime, repo)
	runtime.Go(func() {
		for {
			if err := ctx.Err(); err != nil {
				return
			}
			urls := strings.Split(cfg.Metrics.EventBusURL, ",")
			js, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(urls, "moox-monitor-metrics"))
			if err != nil {
				log.WarnContextf(ctx, "metrics eventbus unavailable; ingestion degraded: %v", err)
				select {
				case <-ctx.Done():
					return
				case <-time.After(30 * time.Second):
				}
				continue
			}
			err = monmetrics.RunWhenReady(ctx, monmetrics.ConsumerOptions{Client: js, Storage: storage, MessageStore: repo, Authorizer: monmetrics.CheckProducerAuthorizer{Checks: runtime.Repositories.Checks}, DLQ: monmetrics.JetStreamDLQ(js, "moox-monitor", cfg.Instance.InstanceID), Config: cfg.Metrics, ServiceName: "moox-monitor", InstanceID: cfg.Instance.InstanceID})
			_ = js.Close()
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.WarnContextf(ctx, "metrics ingestion stopped; retrying: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
	})
}

func startMetricsDedupeCleaner(ctx context.Context, runtime *Runtime, repo *monmetrics.MetricMessageStore) {
	if repo == nil {
		return
	}
	runtime.Go(func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			if _, err := repo.PruneDedupe(ctx, time.Now().UTC()); err != nil {
				log.WarnContextf(ctx, "metrics dedupe prune failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func registerHealth(s *server.Server, cfg *config.Config, runtime *Runtime, metricsStorage *monmetrics.StorageAdapter) error {
	if cfg == nil {
		return nil
	}
	state := health.New("monitor", cfg.Instance.InstanceID, "", "")
	state.SetReady(true)
	state.SnapshotFunc = monitorHealthSnapshot(cfg, runtime, metricsStorage)
	handler := monitorpeer.NewHTTPHandler(monitorpeer.HTTPOptions{
		Token:    cfg.Peer.Token,
		Health:   healthz.ReadinessHandler(state.Snapshot),
		Liveness: healthz.LivenessHandler(state.Snapshot),
		Snapshot: monitorSnapshot(cfg),
	})
	if s == nil {
		return fmt.Errorf("monitor health service is unavailable")
	}
	if err := healthz.RegisterNoProtocolServiceMux(s.Service("trpc.moox.monitor.Health"), handler); err != nil {
		return fmt.Errorf("monitor health server failed to start: %w", err)
	}
	return nil
}

func registerMonitorService(s *server.Server, cfg *config.Config, runtime *Runtime, hostStore *hostmetrics.Store, hostReader *hostmetrics.StorageReader, hostReady func() bool, hook func(context.Context, domain.Check, domain.CheckResult), syncSystem func(context.Context) (int, error), metricsQuery *monmetrics.QueryService, metricRules *monmetrics.MetricRuleStore, metricEvaluator *monmetrics.MetricEvaluator) {
	service := s.Service("trpc.moox.monitor.MonitorMgr")
	if service == nil {
		log.Warn("MonitorMgr service is not configured, skip register")
		return
	}
	monitorpb.RegisterMonitorMgrService(service, monitorrpc.New(runtime.Repositories, monitorrpc.Options{
		InstanceID:       cfg.Instance.InstanceID,
		OnResult:         hook,
		SyncSystem:       syncSystem,
		MetricsQuery:     metricsQuery,
		MetricRules:      metricRules,
		MetricEvaluator:  metricEvaluator,
		HostStore:        hostStore,
		HostReader:       hostReader,
		HostStorageReady: hostReady,
	}))
}

func startScheduler(ctx context.Context, cfg *config.Config, runtime *Runtime, hook func(context.Context, domain.Check, domain.CheckResult)) *scheduler.Scheduler {
	s := scheduler.New(runtime.Repositories, scheduler.Options{
		InstanceID:     cfg.Instance.InstanceID,
		ReloadInterval: time.Duration(cfg.Scheduler.ReloadIntervalSeconds) * time.Second,
		MaxConcurrency: cfg.Scheduler.MaxConcurrency,
		OnResult:       hook,
	})
	s.Start(ctx)
	return s
}

func monitorResultHook(cfg *config.Config, runtime *Runtime) func(context.Context, domain.Check, domain.CheckResult) {
	evaluator := alerting.NewEvaluator(runtime.Repositories.Alerts, alerting.Options{InstanceID: cfg.Instance.InstanceID})
	peers := runtime.Repositories.Peers
	maxPeerAge := time.Duration(0)
	if cfg.Peer.Enabled && len(cfg.Peer.Peers) > 0 {
		maxPeerAge = 3 * time.Duration(cfg.Peer.TimeoutSeconds) * time.Second
	}
	return func(ctx context.Context, check domain.Check, result domain.CheckResult) {
		activeInstanceIDs := activeMonitorInstanceIDs(ctx, cfg.Instance.InstanceID, peers, maxPeerAge)
		if err := evaluator.Evaluate(ctx, check, result, activeInstanceIDs); err != nil {
			log.ErrorContextf(ctx, "monitor alert evaluation failed: %v", err)
		}
	}
}

func activeMonitorInstanceIDs(ctx context.Context, localID string, peers *store.PeerRepository, maxPeerAge time.Duration) []string {
	seen := map[string]struct{}{}
	var ids []string
	if localID != "" {
		ids = append(ids, localID)
		seen[localID] = struct{}{}
	}
	if peers == nil || maxPeerAge <= 0 {
		return ids
	}
	instances, err := peers.ListInstances(ctx)
	if err != nil {
		return ids
	}
	cutoff := time.Now().UTC().Add(-maxPeerAge)
	for _, instance := range instances {
		if instance.Status != domain.InstanceStatusActive || instance.InstanceID == "" {
			continue
		}
		if instance.LastSeenAt == nil || instance.LastSeenAt.Before(cutoff) {
			continue
		}
		if _, ok := seen[instance.InstanceID]; ok {
			continue
		}
		ids = append(ids, instance.InstanceID)
		seen[instance.InstanceID] = struct{}{}
	}
	return ids
}

func startRetentionCleaner(ctx context.Context, cfg *config.Config, runtime *Runtime) {
	retention := time.Duration(cfg.Scheduler.ResultRetentionDays) * 24 * time.Hour
	if retention <= 0 {
		return
	}
	runtime.Go(func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			if err := pruneMonitorHistory(ctx, runtime, retention); err != nil {
				log.WarnContextf(ctx, "monitor retention prune failed: %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func pruneMonitorHistory(ctx context.Context, runtime *Runtime, retention time.Duration) error {
	if runtime == nil || runtime.Store == nil || runtime.Store.Ping(ctx) != nil || retention <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-retention)
	if _, err := runtime.Repositories.Results.DeleteOlderThan(ctx, cutoff); err != nil {
		return err
	}
	if _, err := runtime.Repositories.Alerts.DeleteEventsOlderThan(ctx, cutoff); err != nil {
		return err
	}
	return nil
}

func monitorSyncFunc(ctx context.Context, cfg *config.Config, runtime *Runtime) func(context.Context) (int, error) {
	if !cfg.SysDeploy.Enabled {
		return nil
	}
	syncer := monitorsysdeploy.NewSyncer(runtime.Repositories.Checks, monitorsysdeploy.NewClientSource(cfg.SysDeploy.Target))
	runtime.Go(func() {
		ticker := time.NewTicker(time.Duration(cfg.SysDeploy.SyncIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			if n, err := syncer.Sync(ctx); err != nil {
				log.WarnContextf(ctx, "monitor sysdeploy sync failed: %v", err)
			} else if n > 0 {
				log.InfoContextf(ctx, "monitor sysdeploy sync updated %d checks", n)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
	return syncer.Sync
}

func startPeerPuller(ctx context.Context, cfg *config.Config, runtime *Runtime) {
	if !cfg.Peer.Enabled {
		return
	}
	remotes := make([]monitorpeer.Remote, 0, len(cfg.Peer.Peers))
	for _, item := range cfg.Peer.Peers {
		remotes = append(remotes, monitorpeer.Remote{
			InstanceID: item.InstanceID,
			BaseURL:    item.BaseURL,
			Token:      item.Token,
		})
	}
	puller := monitorpeer.NewPuller(runtime.Repositories.Peers, monitorpeer.PullerOptions{
		Peers:   remotes,
		Timeout: time.Duration(cfg.Peer.TimeoutSeconds) * time.Second,
	})
	runtime.Go(func() {
		ticker := time.NewTicker(time.Duration(cfg.Peer.PullIntervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			if err := puller.PullOnce(ctx); err != nil {
				log.WarnContextf(ctx, "monitor peer pull failed: %v", err)
			}
			_ = puller.MarkStale(ctx, time.Now(), time.Duration(cfg.Peer.TimeoutSeconds)*time.Second)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func monitorSnapshot(cfg *config.Config) func(context.Context) monitorpeer.Snapshot {
	return func(ctx context.Context) monitorpeer.Snapshot {
		return monitorpeer.Snapshot{
			InstanceID: cfg.Instance.InstanceID,
			BaseURL:    cfg.Instance.BaseURL,
			ObservedAt: time.Now().UTC(),
		}
	}
}

func monitorHealthSnapshot(cfg *config.Config, runtime *Runtime, metricsStorage *monmetrics.StorageAdapter) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		var activeChecks, activePeers int64
		var checksErr, peersErr error
		if runtime != nil && runtime.Store != nil && runtime.Store.Ping(ctx) == nil && runtime.Repositories != nil {
			activeChecks, checksErr = runtime.Repositories.Checks.CountEnabled(ctx)
			activePeers, peersErr = runtime.Repositories.Peers.CountActive(ctx)
		}
		databaseReady := runtime != nil && runtime.Store != nil && runtime.Store.Ping(ctx) == nil && runtime.Repositories != nil && checksErr == nil && peersErr == nil
		schedulerReady := runtime != nil && runtime.Scheduler != nil
		ready := databaseReady && schedulerReady
		startedAt := time.Time{}
		if runtime != nil {
			startedAt = runtime.StartedAt
		}
		rsp := healthz.Base("monitor", cfg.Instance.InstanceID, "", "", startedAt, ready)
		rsp.Details = map[string]any{
			"database":          map[bool]string{true: "ok", false: "error"}[databaseReady],
			"scheduler_ok":      schedulerReady,
			"active_checks":     activeChecks,
			"peer_count":        len(cfg.Peer.Peers),
			"active_peer_count": activePeers,
			"peer_enabled":      cfg.Peer.Enabled,
			"sysdeploy_enabled": cfg.SysDeploy.Enabled,
		}
		metricsReady := !cfg.Metrics.Enabled
		metricsReason := "metrics ingestion disabled"
		if cfg.Metrics.Enabled {
			metricsReason = "metrics schema has not been checked"
			if metricsStorage != nil {
				status := metricsStorage.SchemaStatus()
				metricsReady = status.Valid
				if status.Error != "" {
					metricsReason = status.Error
				}
			}
		}
		rsp.Details["metrics_schema_ready"] = metricsReady
		rsp.Details["metrics_schema_reason"] = metricsReason
		ready = databaseReady && schedulerReady && metricsReady
		rsp.Ready = ready
		if ready {
			rsp.Status = "ok"
		} else {
			rsp.Status = "degraded"
		}
		rsp.Details["database_ready"] = databaseReady
		if checksErr != nil {
			rsp.Details["database_checks_error"] = checksErr.Error()
		}
		if peersErr != nil {
			rsp.Details["database_peers_error"] = peersErr.Error()
		}
		rsp.Details["scheduler_ready"] = schedulerReady
		return rsp
	}
}
