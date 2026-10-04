package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

type MetricsReporterSpec struct {
	ServiceName  string
	TimerService string
}

func MetricsReporterSpecForRole(role string) (MetricsReporterSpec, error) {
	switch role {
	case "primary":
		return MetricsReporterSpec{
			ServiceName: "storage-primary", TimerService: "trpc.moox.storage.primary.metrics.timer",
		}, nil
	case "view":
		return MetricsReporterSpec{
			ServiceName: "storage-view", TimerService: "trpc.moox.storage.view.metrics.timer",
		}, nil
	case "node":
		return MetricsReporterSpec{
			ServiceName: "storage-node", TimerService: "trpc.moox.storage.node.metrics.timer",
		}, nil
	default:
		return MetricsReporterSpec{}, fmt.Errorf("storage metrics reporter does not support role %q", role)
	}
}

func RegisterMetricsReporter(s *server.Server, role string) error {
	if s == nil {
		return fmt.Errorf("storage metrics reporter server is required")
	}
	spec, err := MetricsReporterSpecForRole(role)
	if err != nil {
		return err
	}
	handler, err := report.NewHandler(report.DefaultConfig("storage", spec.ServiceName))
	if err != nil {
		return fmt.Errorf("initialize %s metrics reporter: %w", spec.ServiceName, err)
	}
	service := s.Service(spec.TimerService)
	if service == nil {
		return fmt.Errorf("%s metrics timer service is not configured", spec.ServiceName)
	}
	timer.RegisterHandlerService(service, handler.Handle)
	return nil
}
