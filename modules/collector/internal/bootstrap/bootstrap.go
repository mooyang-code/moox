// Package bootstrap wires the independent moox-collector service process.
package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/health"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/report"
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
	eventClient, err := jetstream.Connect(ctx, jetstream.ConfigFromEnv(cfg.EventBus.URLs, "collector"))
	if err != nil {
		return nil, fmt.Errorf("连接 EventBus 失败: %w", err)
	}
	keepEventClient := false
	defer func() {
		if !keepEventClient {
			_ = eventClient.Close()
		}
	}()
	eventRegistry, err := events.DefaultRegistry()
	if err != nil {
		return nil, fmt.Errorf("加载事件注册表失败: %w", err)
	}
	eventPublisher, err := events.NewPublisher(eventClient, eventRegistry)
	if err != nil {
		return nil, fmt.Errorf("初始化事件发布器失败: %w", err)
	}
	binance.SetEventPublisher(eventPublisher)
	if err := dbm.ApplySchema(collectorschema.AllSQL()); err != nil {
		log.ErrorContextf(ctx, "初始化 collector schema 失败: %v", err)
		return nil, err
	}
	deps, err := Resolve(ctx, cfg)
	if err != nil {
		log.WarnContextf(ctx, "[Collector] resolve dependencies from sysdeploy failed, use local defaults: %v", err)
	}

	svc := collectsvc.New(dbm, collectsvc.Dependencies{
		AdminGatewayURL:                deps.AdminGatewayURL,
		ServiceGatewayTarget:           deps.ServiceGatewayTarget,
		ServiceAuth:                    taskpublisherAuth(deps.ServiceAuth),
		StorageRPCGatewayTarget:        deps.StorageRPCGatewayTarget,
		PlannerStorageRPCGatewayTarget: cfg.Storage.GatewayTarget,
	})
	collectorpb.RegisterCollectMgrService(s.Service("trpc.moox.collector.CollectMgr"), svc)
	collectsvc.SetDefaultService(svc)
	if err := registerHealth(s, cfg, dbm); err != nil {
		return nil, err
	}
	registerCollectorSchedule(s)
	registerMetricsReporter(s)

	keepDB = true
	keepEventClient = true
	if done := ctx.Done(); done != nil {
		go func() {
			<-done
			_ = dbm.Close()
			_ = eventClient.Close()
		}()
	}
	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return s, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("moox_collector"))
	if err != nil {
		log.Warnf("collector metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.collector.metrics.timer")
	if service == nil {
		log.Warn("collector metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
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

func registerCollectorSchedule(s *server.Server) {
	timer.RegisterScheduler("collectorSchedule", &timer.DefaultScheduler{})
	service := s.Service("trpc.moox.collector.schedule.timer")
	if service == nil {
		log.Warn("collector schedule timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		return collectsvc.HandleSchedule(ctx, "")
	})
}

func taskpublisherAuth(cfg ServiceAuthConfig) taskpublisher.AuthConfig {
	return taskpublisher.AuthConfig{
		AccessKey:   cfg.AccessKey,
		SecretKey:   cfg.SecretKey,
		TargetNode:  cfg.TargetNode,
		CAFile:      cfg.CAFile,
		CAPEMBase64: cfg.CAPEMBase64,
		ExpireSec:   cfg.ExpireSeconds,
	}
}
