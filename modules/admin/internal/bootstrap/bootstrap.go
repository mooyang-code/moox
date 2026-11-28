package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	"github.com/mooyang-code/moox/modules/admin/internal/webserver"
	"github.com/mooyang-code/moox/modules/admin/internal/webui"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/timerjob"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"

	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize 初始化应用：这是应用启动的统一入口，configPath 是业务配置 app.yaml（-config）。
func Initialize(ctx context.Context, s *server.Server, configPath string) (*server.Server, error) {
	log.InfoContextf(ctx, "开始初始化应用...")

	// 1. 加载配置
	cfg, err := LoadConfigs(ctx, configPath)
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
	// 4. 浏览器入口（9527）：启动时检查或签发网页证书，之后随证书巡检每天检查一次。
	web, err := webserver.New(cfg.Web, services.consoleHandler, webui.FS())
	if err != nil {
		return nil, err
	}
	attention, err := report.NewAttention(prometheus.DefaultRegisterer, adminAttentionKinds)
	if err != nil {
		return nil, fmt.Errorf("初始化 Admin 关注项指标失败: %w", err)
	}
	if err := registerCertificateWatchTimer(ctx, s, newCertificateWatchFromEnvironment(services.DBManager.GetDB(), attention), web); err != nil {
		return nil, err
	}
	registerMetricsReporter(s)
	if err := web.Start(); err != nil {
		return nil, err
	}

	log.InfoContextf(ctx, "应用初始化完成")
	// 钩子按注册顺序执行：先停浏览器入口、等在途请求（最多 30 秒），再关转发用的网关客户端。
	s.RegisterOnShutdown(func() {
		if err := web.Shutdown(); err != nil {
			log.Warnf("浏览器入口停机时仍有请求未完成，已断开：%v", err)
		}
	})
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

// certificateRenewer 是浏览器入口：每天随证书巡检检查一次网页证书，剩余不足 30 天就重签。
type certificateRenewer interface {
	RefreshCertificate(context.Context) error
}

func registerCertificateWatchTimer(ctx context.Context, s *server.Server, watch certificateWatch, web certificateRenewer) error {
	if s == nil {
		return fmt.Errorf("certificate watch timer requires server")
	}
	service := s.Service(certificateWatchTimerService)
	if service == nil {
		return fmt.Errorf("certificate watch timer service %q is not configured", certificateWatchTimerService)
	}
	job, err := timerjob.New("admin_certificate_watch", certificateWatchTimeout, func(ctx context.Context) error {
		var renewErr error
		if web != nil {
			renewErr = web.RefreshCertificate(ctx)
		}
		return errors.Join(renewErr, watch.Validate(ctx))
	})
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
