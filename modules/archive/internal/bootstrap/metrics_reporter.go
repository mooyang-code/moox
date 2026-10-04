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
		return nil, fmt.Errorf("archive metrics reporter requires a tRPC server")
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "archive", report.HealthCheckIDsForModule("archive"))
	if err != nil {
		return nil, err
	}
	h, err := report.NewHandler(report.DefaultConfig("archive", "moox_archive"))
	if err != nil {
		return nil, err
	}
	service := s.Service("trpc.moox.archive.metrics.timer")
	if service == nil {
		return nil, fmt.Errorf("archive metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return moduleMetrics, nil
}
