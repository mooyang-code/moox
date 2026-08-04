// Package bootstrap wires the independent moox-collector service process.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/dnscache"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	collectorobservability "github.com/mooyang-code/moox/modules/collector/internal/observability"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
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
	dnsCache := dnscache.New(dnscache.Config{Domains: cfg.DNS.Domains, RefreshInterval: cfg.DNS.RefreshInterval, ResolveTimeout: cfg.DNS.ResolveTimeout, Nameservers: cfg.DNS.Nameservers})
	if err := dnsCache.Refresh(ctx); err != nil {
		log.WarnContextf(ctx, "collector initial DNS snapshot refresh failed: %v", err)
	}
	registerDNSRefreshSchedule(s, dnsCache)
	registerMarketFetchSchedule(s, cfg, deps, dbm, dnsCache, marketFetchMetrics)
	if err := registerHealth(s, cfg, dbm); err != nil {
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

func registerHealth(s *server.Server, cfg *Config, dbm *store.Store) error {
	if cfg == nil {
		return nil
	}
	state := health.New("collector", "collector", "", "")
	state.SnapshotFunc = collectorHealthSnapshot(cfg, dbm, state)
	if s == nil {
		return fmt.Errorf("collector health service is unavailable")
	}
	if err := health.Register(s.Service("trpc.moox.collector.Health"), state); err != nil {
		return fmt.Errorf("collector health server failed to start: %w", err)
	}
	return nil
}

func collectorHealthSnapshot(cfg *Config, dbm *store.Store, state *health.State) healthz.SnapshotFunc {
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
		return rsp
	}
}

func registerDNSRefreshSchedule(s *server.Server, cache *dnscache.Cache) {
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

func registerMarketFetchSchedule(s *server.Server, cfg *Config, deps Dependencies, dbm *store.Store, dnsCache *dnscache.Cache, metrics *marketfetch.Metrics) {
	if s == nil || cfg == nil || dbm == nil {
		return
	}
	service := s.Service("trpc.moox.collector.schedule.timer")
	if service == nil {
		log.Warn("collector market fetch timer service is not configured, skip register")
		return
	}
	auth := runtimeAuth(deps.ServiceAuth)
	invoker := scfinvoker.New(scfinvoker.Config{ServiceGatewayTarget: deps.AdminGatewayURL, Auth: auth, Timeout: 5 * time.Second})
	metadataSource := storagesource.NewDatasetSource(cfg.Storage.GatewayTarget)
	reconciler := &marketfetch.Reconciler{Rules: dbm.TaskRules(), Symbols: metadataSource, Nodes: invoker, Instances: dbm.TaskInstances(), DNS: dnsCache, Metrics: metrics, MaxSubjects: 30}
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
	completionSpaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	if completionSpaceID == "" {
		completionSpaceID = "crypto_market"
	}
	if err := marketfetch.StartCompletionConsumer(trpc.BackgroundContext(), completionSpaceID, dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), metrics); err != nil {
		log.WarnContextf(trpc.BackgroundContext(), "collector market fetch completion consumer disabled: %v", err)
	}
	if err := marketfetch.StartStorageWriteConsumer(trpc.BackgroundContext(), completionSpaceID, dbm.TaskInstances()); err != nil {
		log.WarnContextf(trpc.BackgroundContext(), "collector storage write consumer disabled: %v", err)
	}
	timer.RegisterScheduler("collectorMarketFetch", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		spaceID := completionSpaceID
		go func() {
			tickCtx := trpc.BackgroundContext()
			if err := invokeScheduler.Tick(tickCtx, spaceID); err != nil {
				log.WarnContextf(tickCtx, "collector invoke scheduler failed space=%s: %v", spaceID, err)
			}
			if err := reconciler.Reconcile(tickCtx, spaceID); err != nil {
				log.WarnContextf(tickCtx, "collector SCF timer reconciliation failed space=%s: %v", spaceID, err)
			}
		}()
		return nil
	})
}

func runtimeAuth(cfg ServiceAuthConfig) runtimeapp.AuthConfig {
	return runtimeapp.AuthConfig{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, TargetNode: cfg.TargetNode, CAFile: cfg.CAFile, CAPEMBase64: cfg.CAPEMBase64, ExpireSec: cfg.ExpireSeconds}
}
