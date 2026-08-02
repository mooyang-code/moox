// Package bootstrap wires the independent moox-collector service process.
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/health"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	collectorobservability "github.com/mooyang-code/moox/modules/collector/internal/observability"
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
	_, err = report.NewDatasetModuleObserver(
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
	registerMarketFetchSchedule(s, cfg, deps, dbm, marketFetchMetrics)
	spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
	if spaceID == "" {
		spaceID = "crypto_market"
	}
	if err := marketfetch.StartCompletionConsumer(ctx, spaceID, dbm.FetchBatches(), dbm.FetchRetries(), dbm.TaskInstances(), marketFetchMetrics); err != nil {
		log.WarnContextf(ctx, "collector market fetch completion consumer disabled: %v", err)
	}
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
		databaseReady := dbm != nil && dbm.Ping(ctx) == nil
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

func registerMarketFetchSchedule(s *server.Server, cfg *Config, deps Dependencies, dbm *store.Store, metrics *marketfetch.Metrics) {
	if s == nil || cfg == nil || dbm == nil {
		return
	}
	service := s.Service("trpc.moox.collector.schedule.timer")
	if service == nil {
		log.Warn("collector market fetch timer service is not configured, skip register")
		return
	}
	if isLoopbackTarget(deps.StorageRPCGatewayTarget) {
		log.ErrorContextf(context.Background(), "collector market fetch timer disabled: Storage target %q is loopback; refusing to send it to remote SCF", deps.StorageRPCGatewayTarget)
		return
	}
	auth := runtimeAuth(deps.ServiceAuth)
	invoker := scfinvoker.New(scfinvoker.Config{ServiceGatewayTarget: deps.AdminGatewayURL, Auth: auth, Timeout: 5 * time.Second})
	scheduler := &marketfetch.Scheduler{
		Rules: dbm.TaskRules(), Instances: dbm.TaskInstances(), Batches: dbm.FetchBatches(), Retries: dbm.FetchRetries(), Invoker: invoker,
		Storage: func(target, market string) (binance.BatchStorage, error) {
			return binance.NewBatchStorage(target, market)
		},
		StorageTarget: deps.StorageRPCGatewayTarget, BatchSize: marketfetch.MaxRealtimeItems, InvokeConcurrency: 20,
		Metrics: metrics,
	}
	timer.RegisterScheduler("collectorMarketFetch", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		spaceID := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID"))
		if spaceID == "" {
			spaceID = "crypto_market"
		}
		if err := scheduler.Tick(ctx, spaceID); err != nil {
			log.WarnContextf(ctx, "collector market fetch tick failed space=%s: %v", spaceID, err)
			return err
		}
		return nil
	})
}

func isLoopbackTarget(target string) bool {
	target = strings.ToLower(strings.TrimSpace(target))
	return strings.Contains(target, "127.0.0.1") || strings.Contains(target, "localhost") || strings.Contains(target, "[::1]")
}

func runtimeAuth(cfg ServiceAuthConfig) runtimeapp.AuthConfig {
	return runtimeapp.AuthConfig{AccessKey: cfg.AccessKey, SecretKey: cfg.SecretKey, TargetNode: cfg.TargetNode, CAFile: cfg.CAFile, CAPEMBase64: cfg.CAPEMBase64, ExpireSec: cfg.ExpireSeconds}
}
