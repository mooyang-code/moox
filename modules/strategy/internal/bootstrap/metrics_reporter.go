package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server) (*report.ModuleMetrics, error) {
	if s == nil {
		return nil, fmt.Errorf("strategy metrics reporter requires a tRPC server")
	}
	pipelines, err := report.ValidatePipelineEnvironment()
	if err != nil {
		return nil, err
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "strategy", pipelines.IDsForModule("strategy"))
	if err != nil {
		return nil, err
	}
	h, err := report.NewHandler(report.DefaultConfig("strategy", "moox_strategy"))
	if err != nil {
		return nil, err
	}
	service := s.Service("trpc.moox.strategy.metrics.timer")
	if service == nil {
		return nil, fmt.Errorf("strategy metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return moduleMetrics, nil
}
