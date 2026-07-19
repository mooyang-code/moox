package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server) error {
	if s == nil {
		return fmt.Errorf("strategy metrics reporter requires a tRPC server")
	}
	h, err := report.NewHandler(report.DefaultConfig("moox_strategy"))
	if err != nil {
		return err
	}
	service := s.Service("trpc.moox.strategy.metrics.timer")
	if service == nil {
		return fmt.Errorf("strategy metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return nil
}
