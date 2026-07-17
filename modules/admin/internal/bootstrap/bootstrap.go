package bootstrap

import (
	"context"
	"fmt"
	"time"

	authdao "github.com/mooyang-code/moox/modules/admin/internal/service/auth/dao"
	"github.com/mooyang-code/moox/modules/admin/internal/service/dnsproxy"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"

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
	cache, err := authdao.NewCacheDBFromBadger(services.DBManager.GetCache())
	if err != nil {
		return nil, fmt.Errorf("create auth cache cleanup handle: %w", err)
	}
	if err := registerAuthCacheCleanupTimer(s, cache); err != nil {
		return nil, err
	}

	// 4. 注册定时器
	// DNS探测定时器（本地DNS解析）
	timer.RegisterScheduler("dnsproxySchedule", &timer.DefaultScheduler{})
	if service := s.Service("trpc.dnsproxy.timer"); service != nil {
		timer.RegisterHandlerService(service, func(ctx context.Context) error {
			return dnsproxy.HandleSchedule(ctx, "")
		})
	}
	// DNS探测定时器（合并终端+本地DNS并探测）
	timer.RegisterScheduler("dnsProbeSchedule", &timer.DefaultScheduler{})
	if service := s.Service("trpc.dnsprobe.timer"); service != nil {
		timer.RegisterHandlerService(service, func(ctx context.Context) error {
			return dnsproxy.HandleDNSProbeSchedule(ctx, "")
		})
	}
	registerMetricsReporter(s)

	log.InfoContextf(ctx, "应用初始化完成")
	return s, nil
}

const authCacheCleanupTimerService = "trpc.moox.admin.auth_cache_cleanup.timer"

type authCacheCleaner interface {
	RunValueLogGC(context.Context) error
}

func registerAuthCacheCleanupTimer(s *server.Server, cleaner authCacheCleaner) error {
	if s == nil || cleaner == nil {
		return fmt.Errorf("auth cache cleanup timer requires server and cache")
	}
	service := s.Service(authCacheCleanupTimerService)
	if service == nil {
		return fmt.Errorf("auth cache cleanup timer service %q is not configured", authCacheCleanupTimerService)
	}
	job, err := timerjob.New("admin_auth_cache_cleanup", time.Minute, cleaner.RunValueLogGC)
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
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
