package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const viewIndexCleanupTimerService = "trpc.moox.storage.view.cleanup.timer"
const viewIndexCleanupTimerTimeout = 20 * time.Second

func RegisterViewIndexCleanupTimer(s *server.Server, cleanup func(context.Context) error) error {
	if s == nil {
		return fmt.Errorf("storage view index cleanup timer requires a tRPC server")
	}
	service := s.Service(viewIndexCleanupTimerService)
	if service == nil {
		return fmt.Errorf("storage view index cleanup timer service %q is not configured", viewIndexCleanupTimerService)
	}
	job, err := timerjob.New("storage_view_index_cleanup", viewIndexCleanupTimerTimeout, cleanup)
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
