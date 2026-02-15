package bootstrap

import (
	"context"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/server/internal/service/cloudnode"
	"github.com/mooyang-code/moox/server/internal/service/collectmgr"
	"github.com/mooyang-code/moox/server/internal/service/dnsproxy"
	"github.com/mooyang-code/moox/server/internal/service/monitor"

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

	// 3.5 注入依赖（避免循环依赖）
	dnsproxy.GetActiveNodeIDsFunc = cloudnode.GetActiveNodeIDs

	// 4. 注册定时器
	// DNS探测定时器（本地DNS解析）
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsproxy.timer"), dnsproxy.HandleSchedule)
	// DNS探测定时器（合并终端+本地DNS并探测）
	timer.RegisterScheduler("dnsProbeSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsprobe.timer"), dnsproxy.HandleDNSProbeSchedule)
	// 云节点保活定时器
	timer.RegisterScheduler("keepaliveSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.keepalive.timer"), cloudnode.HandleKeepaliveSchedule)
	// 任务实例重算定时器
	timer.RegisterScheduler("taskPlannerSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.collectmgr.timer"), collectmgr.HandleTaskPlannerSchedule)
	// 监控数据采集定时器
	timer.RegisterScheduler("monitorSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.monitor.timer"), monitor.HandleMonitorSchedule)
	// 监控历史数据清理定时器（每天0点清理7天前数据）
	timer.RegisterScheduler("monitorCleanupSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.monitor.cleanup.timer"), monitor.HandleMonitorCleanupSchedule)

	log.InfoContextf(ctx, "应用初始化完成")
	return s, nil
}
