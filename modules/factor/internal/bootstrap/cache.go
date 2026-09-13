package bootstrap

import (
	"fmt"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const cacheTimerService = "trpc.moox.factor.engine.cache.timer"

func registerEngineCache(s *server.Server, runtime *inputcache.Runtime) error {
	if runtime == nil {
		return nil
	}
	if s == nil || s.Service(cacheTimerService) == nil {
		return fmt.Errorf("factor engine cache timer is not configured")
	}
	if runtime.Job == nil {
		return fmt.Errorf("factor engine cache maintenance job is missing")
	}
	if err := s.Service(cacheTimerService).Register(&timer.ServiceDesc, runtime.Job); err != nil {
		return fmt.Errorf("register cache timer: %w", err)
	}
	return nil
}
