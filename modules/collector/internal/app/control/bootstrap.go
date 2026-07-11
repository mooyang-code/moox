// Package bootstrap wires the independent moox-collector service process.
package control

import (
	"context"
	"time"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/collector/internal/builtin"
	"github.com/mooyang-code/moox/modules/collector/internal/metricspublish"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

var collectorStartedAt = time.Now()

// Initialize loads config, initializes persistence, and registers RPC services.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-collector...")

	cfg, err := Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 collector 配置失败: %v", err)
		return nil, err
	}
	SetGlobalConfig(cfg)
	manifests, err := marketmanifest.LoadDir("./config/markets")
	if err != nil {
		return nil, err
	}
	if err := builtin.Default("./config/markets/stock_cn/calendar.yaml").ValidateManifests(manifests); err != nil {
		return nil, err
	}

	dbm := NewManager()
	if err := dbm.Initialize(&cfg.Database); err != nil {
		log.ErrorContextf(ctx, "初始化 collector 数据库失败: %v", err)
		return nil, err
	}
	if err := repository.MigrateMarketControl(dbm.DB()); err != nil {
		return nil, err
	}
	leader := NewLeader(dbm.DB(), "")
	if err := leader.Start(ctx); err != nil {
		return nil, err
	}
	startHealthServer(ctx, cfg, leader)
	deps, err := Resolve(ctx, cfg)
	if err != nil {
		log.WarnContextf(ctx, "[Collector] resolve dependencies from sysdeploy failed, use local defaults: %v", err)
	}

	svc := collectsvc.New(dbm.DB(), collectsvc.Dependencies{
		AdminGatewayURL:       deps.AdminGatewayURL,
		ServiceGatewayTarget:  deps.ServiceGatewayTarget,
		ServiceAuth:           taskpublisherAuth(deps.ServiceAuth),
		StorageMetadataTarget: deps.StorageMetadataTarget,
		StorageAccessTarget:   deps.StorageAccessTarget,
		MutationGuard:         leader,
		MarketManifests:       manifests,
	})
	taskpublisher.SetDefaultOutboxPublisher(&taskpublisher.OutboxPublisher{Client: taskpublisher.New(taskpublisher.Config{ServiceGatewayTarget: deps.ServiceGatewayTarget, StorageMetadataTarget: deps.StorageMetadataTarget, StorageAccessTarget: deps.StorageAccessTarget, Auth: taskpublisherAuth(deps.ServiceAuth)}), Repository: repository.NewMarketAttemptRepository(dbm.DB()), Guard: leader})
	collectorpb.RegisterCollectMgrService(s.Service("trpc.moox.collector.CollectMgr"), svc)
	collectsvc.SetDefaultService(svc)
	registerCollectorSchedule(s)
	registerCoverageSchedule(s)
	registerOutboxPublisher(s)
	registerMetricsReporter(s)

	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return s, nil
}

func registerCoverageSchedule(s *server.Server) {
	service := s.Service("trpc.moox.collector.coverage.timer")
	if service == nil {
		log.Warn("collector coverage timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, collectsvc.HandleCoverageSchedule)
}

func registerOutboxPublisher(s *server.Server) {
	service := s.Service("trpc.moox.collector.outbox.timer")
	if service == nil {
		log.Warn("collector outbox timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, taskpublisher.HandleOutbox)
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := metricspublish.NewHandler(metricspublish.DefaultConfig("moox_collector"))
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

func startHealthServer(ctx context.Context, cfg *Config, leaders ...*Leader) {
	if cfg == nil {
		return
	}
	if _, err := healthz.Start(ctx, cfg.Health.Addr, collectorHealthSnapshot(cfg, leaders...)); err != nil {
		log.ErrorContextf(ctx, "collector health server failed to start: %v", err)
	}
}

func collectorHealthSnapshot(cfg *Config, leaders ...*Leader) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		ready, leadership := true, "active"
		if len(leaders) > 0 && leaders[0] != nil && !leaders[0].Active() {
			ready, leadership = false, "standby"
		}
		rsp := healthz.Base("collector", "collector", "", "", collectorStartedAt, ready)
		rsp.Details = map[string]any{
			"database":                "ok",
			"cloudnode_address":       cfg.CloudNode.Address,
			"storage_metadata_target": cfg.Storage.MetadataTarget,
			"storage_access_target":   cfg.Storage.AccessTarget,
			"control_leadership":      leadership,
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
	timer.RegisterHandlerService(service, collectsvc.HandleSchedule)
}

func taskpublisherAuth(cfg ServiceAuthConfig) taskpublisher.AuthConfig {
	return taskpublisher.AuthConfig{
		Version:   cfg.Version,
		AccessKey: cfg.AccessKey,
		SecretKey: cfg.SecretKey,
		ExpireSec: cfg.ExpireSeconds,
	}
}
