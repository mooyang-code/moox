// Package bootstrap wires the independent moox-collector service process.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/dnscache"
	collectordns "github.com/mooyang-code/moox/modules/collector/internal/dnsresolver"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	collectorobservability "github.com/mooyang-code/moox/modules/collector/internal/observability"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectorresample "github.com/mooyang-code/moox/modules/collector/internal/resample"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/gatewayauth"
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
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	log.InfoContextf(ctx, "开始初始化 moox-collector...")

	cfg, err := Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 collector 配置失败: %v", err)
		return nil, err
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
	keepDB := false
	defer func() {
		if !keepDB {
			_ = dbm.Close()
		}
	}()
	if err := dbm.ApplySchema(collectorschema.AllSQL()); err != nil {
		log.ErrorContextf(ctx, "初始化 collector schema 失败: %v", err)
		return nil, err
	}
	deps, err := Resolve(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("resolve collector dependencies from sysdeploy: %w", err)
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
	realtimeInventory := collectorobservability.NewRealtimeInventory(dbm.TaskRules(), datasetMetrics)
	if err := realtimeInventory.Refresh(ctx); err != nil {
		return nil, fmt.Errorf("initialize collector realtime dataset inventory: %w", err)
	}
	svc := collectsvc.New(dbm, collectsvc.Dependencies{
		StorageRPCGatewayTarget:        deps.StorageRPCGatewayTarget,
		PlannerStorageRPCGatewayTarget: cfg.Storage.GatewayTarget,
		RealtimeInventory:              realtimeInventory,
	})
	collectorpb.RegisterCollectMgrService(s.Service("trpc.moox.collector.CollectMgr"), svc)
	marketFetchMetrics := marketfetch.NewMetrics(prometheus.DefaultRegisterer)
	marketFetchMetrics.SetDatasetRunObserver(datasetRunObserver)
	dnsDomains := append([]string(nil), cfg.DNS.Domains...)
	if cfg.DNSResolver.Enabled {
		dnsDomains = append(dnsDomains, cfg.DNSResolver.Domains...)
	}
	localDNS := dnscache.New(dnscache.Config{Domains: dnsDomains, RefreshInterval: cfg.DNS.RefreshInterval, ResolveTimeout: cfg.DNS.ResolveTimeout, Nameservers: cfg.DNS.Nameservers})
	var remoteDNS collectordns.DomainResolver
	if cfg.DNSResolver.Enabled {
		// The resolver call is a native Gateway call made as the collector
		// caller. SysDeploy's service credential is reserved for the control
		// plane dependency API and must not be reused for Trade RPCs.
		remoteDNS = collectordns.NewTradeClient(
			cfg.DNSResolver.Target,
			cfg.DNSResolver.NodeID,
			gatewayauth.CredentialsFromEnv(),
			cfg.DNSResolver.RequestTimeout,
		)
	}
	refreshInterval, cacheTTL := cfg.DNS.RefreshInterval, cfg.DNS.RefreshInterval
	if cfg.DNSResolver.Enabled {
		refreshInterval, cacheTTL = cfg.DNSResolver.RefreshInterval, cfg.DNSResolver.CacheTTL
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
	if cfg.DNSResolver.Enabled {
		dnsPersistencePath = filepath.Join(filepath.Dir(cfg.Database.Path), "dns_resolver_snapshot.json")
	}
	dnsSnapshot := collectordns.NewCoordinatorWithMetricsAndPersistence(localDNS, remoteDNS, dnsDomains, refreshInterval, cacheTTL, dnsMetrics, dnsPersistencePath)
	if err := dnsSnapshot.RestoreLastGoodSnapshot(); err != nil {
		log.WarnContextf(ctx, "restore collector DNS last-good snapshot failed: %v", err)
	}
	if err := dnsSnapshot.Refresh(ctx); err != nil {
		log.WarnContextf(ctx, "collector initial DNS snapshot refresh failed: %v", err)
	}
	registerDNSRefreshSchedule(s, dnsSnapshot)
	registerMarketFetchSchedule(s, cfg, deps, dbm, dnsSnapshot, marketFetchMetrics)
	if err := registerHealth(s, cfg, dbm, dnsSnapshot); err != nil {
		return nil, err
	}
	registerMetricsReporter(s, realtimeInventory)

	keepDB = true
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = dbm.Close()
		}()
	}
	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return s, nil
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
			"database":                   databaseReady,
			"cloudnode_address":          cfg.CloudNode.Address,
			"storage_rpc_gateway_target": cfg.Storage.GatewayTarget,
		}
		if cfg.DNSResolver.Enabled {
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

func registerDNSRefreshSchedule(s *server.Server, cache dnsSnapshotter) {
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
		go func() {
			if err := cache.Refresh(trpc.BackgroundContext()); err != nil {
				log.WarnContextf(ctx, "collector DNS snapshot refresh failed: %v", err)
			}
		}()
		return nil
	})
}

func registerMarketFetchSchedule(s *server.Server, cfg *Config, deps Dependencies, dbm *store.Store, dnsCache dnsSnapshotter, metrics *marketfetch.Metrics) {
	if s == nil || cfg == nil || dbm == nil {
		return
	}
	service := s.Service("trpc.moox.collector.schedule.timer")
	if service == nil {
		log.Warn("collector market fetch timer service is not configured, skip register")
		return
	}
	auth := runtimeAuth(deps.ServiceAuth)
	// CloudNode control calls are service-gateway requests.  The admin gateway
	// is only used to discover active deployments; using it here makes every
	// scheduled invocation hit the browser/admin auth surface instead of the
	// authenticated service route.
	invoker := scfinvoker.New(scfinvoker.Config{ServiceGatewayTarget: deps.ServiceGatewayTarget, Auth: auth, Timeout: 5 * time.Second})
	metadataSource := storagesource.NewDatasetSource(cfg.Storage.GatewayTarget)
	completionSpaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	if completionSpaceID == "" {
		completionSpaceID = "crypto_market"
	}
	var resampleRunner *collectorresample.Runner
	var resamplePreparer *collectorresample.Preparer
	if cfg.KlineResample.Enabled {
		metadataClient, metadataErr := binance.NewResampleMetadataClient(cfg.Storage.GatewayTarget, "spot")
		localStorage, storageErr := binance.NewResampleStorage(deps.StorageRPCGatewayTarget, "spot", "collector:kline_resample")
		if metadataErr != nil || storageErr != nil {
			log.WarnContextf(trpc.BackgroundContext(), "collector kline resample disabled: metadata=%v storage=%v", metadataErr, storageErr)
		} else {
			catalog := &collectorresample.Catalog{Metadata: metadataClient.Client, Auth: metadataClient.Auth}
			resamplePreparer = &collectorresample.Preparer{Rules: dbm.TaskRules(), Source: metadataSource, Catalog: catalog, KeepDuration: cfg.KlineResample.TargetKeepDuration.String(), Limit: cfg.KlineResample.WorkerSubjectBatchSize}
			if waiter, ok := localStorage.(binance.ResampleViewSyncWaiter); ok {
				catalog.ViewSync = waiter
			} else {
				log.Warn("collector kline resample disabled: Storage adapter has no View sync waiter")
			}
			resampleRunner = &collectorresample.Runner{Rules: dbm.TaskRules(), Instances: dbm.TaskInstances(), Readiness: dbm.PeriodReadiness(), Source: metadataSource, Primary: localStorage, Config: collectorresample.RunnerConfig{
				SpaceID: completionSpaceID, WorkerConcurrency: cfg.KlineResample.WorkerConcurrency, WorkerJobTimeout: cfg.KlineResample.WorkerJobTimeout,
				WorkerPollInterval: cfg.KlineResample.WorkerPollInterval, WorkerMaxSourceKeys: cfg.KlineResample.WorkerMaxSourceKeysPerClaim,
				StaleRunningAfter: cfg.KlineResample.StaleRunningAfter, DefaultSettleDelay: cfg.KlineResample.DefaultSettleDelay, RepairLookbackBuckets: cfg.KlineResample.RepairLookbackBuckets,
			}}
			if err := resamplePreparer.RunOnce(trpc.BackgroundContext()); err != nil {
				log.WarnContextf(trpc.BackgroundContext(), "collector kline resample initial preparation failed: %v", err)
			}
		}
	}
	reconciler := &marketfetch.Reconciler{Rules: dbm.TaskRules(), Symbols: metadataSource, Nodes: invoker, Instances: dbm.TaskInstances(), DNS: dnsCache, Metrics: metrics, MaxSubjects: 30}
	readiness := marketfetch.NewPeriodReadinessService(dbm.TaskInstances(), dbm.PeriodReadiness(), cfg.PeriodReadiness.Grace)
	invokeScheduler := &marketfetch.Scheduler{
		Rules: dbm.TaskRules(), Instances: dbm.TaskInstances(), Batches: dbm.FetchBatches(), Retries: dbm.FetchRetries(),
		// Use the target resolved by discovery rather than the static local
		// config.  Invoke SCFs may run outside the Collector host, so a
		// 127.0.0.1 gateway target would point back at the function itself.
		Invoker: invoker, Storage: binance.NewBatchStorageWithWriteSource, StorageTarget: deps.StorageRPCGatewayTarget,
		InvokeConcurrency: 20, MaxRetryAttempts: 3, Metrics: metrics, SpaceID: "crypto_market", DNSCache: dnsCache,
		Symbols:               metadataSource,
		InvokeNonRealtimeOnly: true,
	}
	if err := readiness.EnsureCurrentAndNext(trpc.BackgroundContext(), completionSpaceID, time.Now().UTC()); err != nil {
		log.WarnContextf(trpc.BackgroundContext(), "collector period readiness prebuild failed space=%s: %v", completionSpaceID, err)
	}
	if err := marketfetch.StartCompletionConsumer(trpc.BackgroundContext(), completionSpaceID, dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), metrics); err != nil {
		log.WarnContextf(trpc.BackgroundContext(), "collector market fetch completion consumer disabled: %v", err)
	}
	replayReady, err := marketfetch.StartStorageWriteConsumerReady(trpc.BackgroundContext(), completionSpaceID, dbm.TaskInstances(), readiness)
	if err != nil {
		log.WarnContextf(trpc.BackgroundContext(), "collector storage write consumer disabled: %v", err)
	}
	periodStorage, storageErr := binance.NewBatchStorageWithWriteSource(deps.StorageRPCGatewayTarget, "spot", "collector")
	if storageErr != nil {
		log.WarnContextf(trpc.BackgroundContext(), "collector period readiness reporter disabled: %v", storageErr)
	} else if reporter, ok := periodStorage.(marketfetch.DatasetPeriodReporter); ok {
		periodReporter := marketfetch.NewPeriodReporter(dbm.PeriodReadiness(), reporter, completionSpaceID, cfg.PeriodReadiness.ParentRetention)
		periodReporter.SetItemRetention(cfg.PeriodReadiness.ItemRetention)
		periodReporter.SetMetrics(metrics)
		// DeliverAll replay is a one-time migration/readiness projection. Do
		// not finalize deadlines until the replay has drained, otherwise a
		// slow startup can publish degraded before its historical rows arrive.
		go func() {
			// A DeliverAll replay is part of the readiness evidence. Do not
			// fail open after a wall-clock timeout: starting FinalizeDue while
			// the row tail is still pending would permanently publish degraded.
			<-replayReady
			if err := marketfetch.StartPeriodReporter(trpc.BackgroundContext(), periodReporter, cfg.PeriodReadiness.ReportInterval); err != nil {
				log.WarnContextf(trpc.BackgroundContext(), "collector period readiness reporter disabled: %v", err)
			}
		}()
	} else {
		log.Warn("collector Storage adapter does not support period readiness reports")
	}
	timer.RegisterScheduler("collectorMarketFetch", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		spaceID := completionSpaceID
		go func() {
			tickCtx := trpc.BackgroundContext()
			if resamplePreparer != nil {
				if err := resamplePreparer.RunOnce(tickCtx); err != nil {
					log.WarnContextf(tickCtx, "collector kline resample preparation failed space=%s: %v", spaceID, err)
				}
			}
			if resampleRunner != nil {
				if err := resampleRunner.Tick(tickCtx, time.Now().UTC()); err != nil {
					log.WarnContextf(tickCtx, "collector kline resample tick failed space=%s: %v", spaceID, err)
				}
			}
			if err := invokeScheduler.Tick(tickCtx, spaceID); err != nil {
				log.WarnContextf(tickCtx, "collector invoke scheduler failed space=%s: %v", spaceID, err)
			}
			if err := reconciler.Reconcile(tickCtx, spaceID); err != nil {
				log.WarnContextf(tickCtx, "collector SCF timer reconciliation failed space=%s: %v", spaceID, err)
			}
			if err := readiness.EnsureCurrentAndNext(tickCtx, spaceID, time.Now().UTC()); err != nil {
				log.WarnContextf(tickCtx, "collector period readiness prebuild failed space=%s: %v", spaceID, err)
			}
		}()
		return nil
	})
}

func runtimeAuth(cfg ServiceAuthConfig) runtimeapp.AuthConfig {
	return runtimeapp.AuthConfig{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, TargetNode: cfg.TargetNode, CAFile: cfg.CAFile, CAPEMBase64: cfg.CAPEMBase64, ExpireSec: cfg.ExpireSeconds}
}
