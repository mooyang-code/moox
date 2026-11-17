package bootstrap

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

// registerMetricsReporter 注册指标上报；readiness 是 /readyz 用的同一个就绪判断，随上报带给 Monitor。
func registerMetricsReporter(s *server.Server, readiness func(context.Context) (bool, string)) error {
	if s == nil {
		return fmt.Errorf("gateway metrics reporter requires a tRPC server")
	}
	h, err := report.NewHandler(report.DefaultConfig("host_gateway", "host-gateway"))
	if err != nil {
		return err
	}
	h.SetReadiness(readiness)
	service := s.Service("trpc.moox.hostgateway.metrics.timer")
	if service == nil {
		return fmt.Errorf("gateway metrics timer service is not configured")
	}
	timer.RegisterHandlerService(service, h.Handle)
	return nil
}
