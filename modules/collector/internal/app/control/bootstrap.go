// Package bootstrap wires the independent moox-collector service process.
package control

import (
	"context"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	collectsvc "github.com/mooyang-code/moox/modules/collector/internal/rpc"
	"github.com/mooyang-code/moox/modules/collector/internal/taskpublisher"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize loads config, initializes persistence, and registers RPC services.
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化 moox-collector...")

	cfg, err := Load("./config/app.yaml")
	if err != nil {
		log.ErrorContextf(ctx, "加载 collector 配置失败: %v", err)
		return nil, err
	}
	SetGlobalConfig(cfg)

	dbm := NewManager()
	if err := dbm.Initialize(&cfg.Database); err != nil {
		log.ErrorContextf(ctx, "初始化 collector 数据库失败: %v", err)
		return nil, err
	}
	deps, err := Resolve(ctx, cfg)
	if err != nil {
		log.WarnContextf(ctx, "[Collector] resolve dependencies from sysdeploy failed, use local defaults: %v", err)
	}

	svc := collectsvc.New(dbm.DB(), collectsvc.Dependencies{
		AdminGatewayURL:       deps.AdminGatewayURL,
		ServiceAuth:           taskpublisherAuth(deps.ServiceAuth),
		StorageMetadataTarget: deps.StorageMetadataTarget,
		StorageAccessTarget:   deps.StorageAccessTarget,
	})
	collectorpb.RegisterCollectMgrService(s.Service("trpc.moox.collector.CollectMgr"), svc)
	collectsvc.SetDefaultService(svc)
	registerCollectorSchedule(s)

	log.InfoContextf(ctx, "moox-collector 初始化完成")
	return s, nil
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
