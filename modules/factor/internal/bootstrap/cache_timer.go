package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const cacheMaintenanceTimerService = "trpc.moox.factor.engine.cache.timer"

// RegisterCacheMaintenanceTimer is used only by the engine bootstrap.
func RegisterCacheMaintenanceTimer(s *server.Server, cfg inputcache.Config, maintain func(context.Context) error) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	if s == nil || s.Service(cacheMaintenanceTimerService) == nil {
		return fmt.Errorf("factor engine timer service %q is not configured", cacheMaintenanceTimerService)
	}
	job, err := inputcache.NewMaintenanceJob(cfg, time.Now, maintain)
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(s.Service(cacheMaintenanceTimerService), job.Handle)
	return nil
}
