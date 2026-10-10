package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server) error {
	if s == nil {
		return fmt.Errorf("gateway metrics reporter requires a tRPC server")
	}
	h, err := report.NewHandler(report.DefaultConfig("host_gateway", "host-gateway"))
	if err != nil {
		return err
	}
	service := s.Service("trpc.moox.hostgateway.metrics.timer")
	if service == nil {
		return fmt.Errorf("gateway metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return nil
}
