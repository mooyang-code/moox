package bootstrap

import (
	"context"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize 初始化应用
// 这是应用启动的统一入口，完成所有初始化工作
func Initialize(ctx context.Context, s *server.Server) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化应用...")

	// 1. 加载配置
	cfg, err := LoadConfigs(ctx)
	if err != nil {
		log.ErrorContextf(ctx, "加载配置失败: %v", err)
		return nil, err
	}

	// 2. 启动后台服务
	services, err := StartBackgroundServices(ctx, cfg)
	if err != nil {
		log.ErrorContextf(ctx, "启动后台服务失败: %v", err)
		return nil, err
	}

	// 3. 注册TRPC服务
	if err := RegisterTRPCServices(s, cfg, services); err != nil {
		log.ErrorContextf(ctx, "注册TRPC服务失败: %v", err)
		return nil, err
	}

	// 4. 注册定时器
	// 节点心跳探测定时器（仅探测异常超时节点）
	timer.RegisterScheduler("healthProbeSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.healthProbe.timer"), cloudnode.HealthProbeSchedule)
	// 节点心跳探测定时器（保活所有节点）
	timer.RegisterScheduler("keepaliveSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.keepalive.timer"), cloudnode.KeepaliveSchedule)
	// DNS探测定时器
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsproxy.timer"), dnsproxy.HandleSchedule)
	// 任务实例重算定时器
	timer.RegisterScheduler("taskPlannerSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.collectmgr.timer"), collectmgr.HandleTaskPlannerSchedule)

	log.InfoContextf(ctx, "应用初始化完成")
	return s, nil
}
