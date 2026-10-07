package bootstrap

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const factorMetricsTimerService = "trpc.moox.factor.metrics.timer"

// registerMetricsReporter publishes moox-factor-mgr's metrics snapshot every
// 30s. Computation runs in moox-factor-engine on an operator machine, so each
// tick first turns the engine's heartbeat into factor dataset metrics; Monitor
// then checks factor results for freshness like collected data.
func registerMetricsReporter(s *server.Server, engine engineView) error {
	service := s.Service(factorMetricsTimerService)
	if service == nil {
		return fmt.Errorf("factor metrics timer service %s is not configured", factorMetricsTimerService)
	}
	handler, err := report.NewHandler(report.DefaultConfig("factor", "moox_factor_mgr"))
	if err != nil {
		return fmt.Errorf("create factor metrics reporter: %w", err)
	}
	datasets, err := report.NewDatasetMetrics(prometheus.DefaultRegisterer, "factor")
	if err != nil {
		return fmt.Errorf("create factor dataset metrics: %w", err)
	}
	observer := newFactorDatasetObserver(engine, datasets)
	timer.RegisterHandlerService(service, func(ctx context.Context) error {
		if err := observer.observe(ctx); err != nil {
			log.WarnContextf(ctx, "factor dataset metrics not refreshed: %v", err)
		}
		return handler.Handle(ctx)
	})
	return nil
}
