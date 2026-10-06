package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const factorMetricsTimerService = "trpc.moox.factor.metrics.timer"

// registerMetricsReporter publishes moox-factor-mgr's metrics snapshot every
// 30s so Monitor sees it as a live reporter. It carries no factor-calculation
// freshness signal: computation runs in moox-factor-engine on the operator
// machine, and the manager cannot vouch for it.
func registerMetricsReporter(s *server.Server) error {
	service := s.Service(factorMetricsTimerService)
	if service == nil {
		return fmt.Errorf("factor metrics timer service %s is not configured", factorMetricsTimerService)
	}
	handler, err := report.NewHandler(report.DefaultConfig("factor", "moox_factor_mgr"))
	if err != nil {
		return fmt.Errorf("create factor metrics reporter: %w", err)
	}
	timer.RegisterHandlerService(service, handler.Handle)
	return nil
}
