package bootstrap

import (
	"context"

	"github.com/mooyang-code/go-commlib/trpc-database/timer"
	"github.com/mooyang-code/moox/modules/admin/internal/report"
	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"

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
	// DNS探测定时器（本地DNS解析）
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsproxy.timer"), dnsproxy.HandleSchedule)
	// DNS探测定时器（合并终端+本地DNS并探测）
	timer.RegisterScheduler("dnsProbeSchedule", &timer.DefaultScheduler{})
	timer.RegisterHandlerService(s.Service("trpc.dnsprobe.timer"), dnsproxy.HandleDNSProbeSchedule)
	registerMetricsReporter(s)

	log.InfoContextf(ctx, "应用初始化完成")
	return s, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("admin_gateway"))
	if err != nil {
		log.Warnf("admin metrics reporter disabled: %v", err)
		return
	}
	service := s.Service("trpc.moox.admin.metrics.timer")
	if service == nil {
		log.Warn("admin metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}
