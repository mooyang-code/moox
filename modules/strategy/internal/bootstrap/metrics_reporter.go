package bootstrap

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const metricsTimerService = "trpc.moox.strategy.metrics.timer"

// registerMetricsReporter 每个定时周期先刷新实例的期望数据集清单，再把指标快照发给 Monitor。
func registerMetricsReporter(s *server.Server, observer *instanceObserver) error {
	if s == nil {
		return errors.New("策略指标上报需要 tRPC 服务")
	}
	service := s.Service(metricsTimerService)
	if service == nil {
		return errors.New("策略指标定时服务 " + metricsTimerService + " 未配置")
	}
	handler, err := report.NewHandler(report.DefaultConfig("strategy", "moox_strategy"))
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		if observer != nil {
			if err := observer.refresh(ctx); err != nil {
				log.WarnContextf(ctx, "策略实例的期望数据集未刷新：%v", err)
			}
		}
		return handler.Handle(ctx)
	})
	return nil
}
