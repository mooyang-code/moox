package bootstrap

import (
	"context"
	"fmt"
	"time"

	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	"github.com/mooyang-code/moox/packages/healthz"
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
	initialized := false
	defer func() {
		if !initialized {
			services.closeGateways()
		}
	}()

	// 3. 注册TRPC服务
	if err := RegisterTRPCServices(s, cfg, services); err != nil {
		log.ErrorContextf(ctx, "注册TRPC服务失败: %v", err)
		return nil, err
	}
	if err := registerCertificateWatchTimer(ctx, s, newCertificateWatchFromEnvironment(services.DBManager.GetDB())); err != nil {
		return nil, err
	}
	registerMetricsReporter(s)

	log.InfoContextf(ctx, "应用初始化完成")
	s.RegisterOnShutdown(services.closeGateways)
	initialized = true
	return s, nil
}

func registerMetricsReporter(s *server.Server) {
	if s == nil {
		return
	}
	h, err := report.NewHandler(report.DefaultConfig("admin", "admin"))
	if err != nil {
		log.Warnf("admin metrics reporter disabled: %v", err)
		return
	}
	h.SetReadiness(healthz.Readiness(adminhealth.Snapshot(time.Now())))
	service := s.Service("trpc.moox.admin.metrics.timer")
	if service == nil {
		log.Warn("admin metrics timer service is not configured, skip register")
		return
	}
	timer.RegisterHandlerService(service, h.Handle)
}

func registerCertificateWatchTimer(ctx context.Context, s *server.Server, watch certificateWatch) error {
	if s == nil {
		return fmt.Errorf("certificate watch timer requires server")
	}
	service := s.Service(certificateWatchTimerService)
	if service == nil {
		return fmt.Errorf("certificate watch timer service %q is not configured", certificateWatchTimerService)
	}
	job, err := timerjob.New("admin_certificate_watch", certificateWatchTimeout, watch.Validate)
	if err != nil {
		return err
	}
	// Other registered hosts may not have completed their first deployment yet.
	// Report findings while allowing the control plane to start so deployment
	// and certificate renewal can proceed.
	if err := job.Handle(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		log.WarnContextf(ctx, "initial certificate watch reported a problem: %v", err)
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
