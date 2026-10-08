package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const routeRefreshTimerService = "trpc.moox.gateway.route_refresh.timer"

func newRouteRefreshJob(refresh func(context.Context) error) (*timerjob.Job, error) {
	return newRouteRefreshJobWithTimeout(10*time.Second, refresh)
}

func newRouteRefreshJobWithTimeout(timeout time.Duration, refresh func(context.Context) error) (*timerjob.Job, error) {
	return timerjob.New("gateway_route_refresh", timeout, refresh)
}

func registerRouteRefreshTimer(s *server.Server, runtime *Runtime) error {
	if s == nil {
		return fmt.Errorf("gateway route refresh timer requires a tRPC server")
	}
	service := s.Service(routeRefreshTimerService)
	if service == nil {
		return fmt.Errorf("gateway route refresh timer service %q is not configured", routeRefreshTimerService)
	}
	job, err := newRouteRefreshJob(runtime.Refresh)
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
