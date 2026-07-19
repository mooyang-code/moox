package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

func registerMetricsReporter(s *server.Server) error {
	if s == nil {
		return fmt.Errorf("archive metrics reporter requires a tRPC server")
	}
	h, err := report.NewHandler(report.DefaultConfig("moox_archive"))
	if err != nil {
		return err
	}
	service := s.Service("trpc.moox.archive.metrics.timer")
	if service == nil {
		return fmt.Errorf("archive metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return nil
}
