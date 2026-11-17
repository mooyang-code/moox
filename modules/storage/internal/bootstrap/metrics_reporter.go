package bootstrap

import (
	"context"
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
	default:
		return MetricsReporterSpec{}, fmt.Errorf("storage metrics reporter does not support role %q", role)
	}
}

// RegisterMetricsReporter 注册指标上报；readiness 是这个进程 /readyz 用的就绪判断，随上报带给 Monitor。
func RegisterMetricsReporter(s *server.Server, role string, readiness func(context.Context) (bool, string)) error {
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
	handler.SetReadiness(readiness)
	service := s.Service(spec.TimerService)
	if service == nil {
		return fmt.Errorf("%s metrics timer service is not configured", spec.ServiceName)
	}
	if role == "primary" || role == "view" {
		// 写入水位与 View 水位前进时立刻补报，Monitor 约 1 秒内就能看到新写完的那根，不等 30 秒定时上报。
		handler.FlushOnOutputAdvance()
	}
	timer.RegisterHandlerService(service, handler.Handle)
	return nil
}
