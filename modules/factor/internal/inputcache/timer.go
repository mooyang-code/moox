package inputcache

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/timerjob"
)

// NewMaintenanceJob adapts cache maintenance to the shared synchronous tRPC job.
func NewMaintenanceJob(cfg Config, now func() time.Time, maintain func(context.Context) error) (*timerjob.Job, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if now == nil || maintain == nil {
		return nil, fmt.Errorf("cache maintenance requires a clock and callback")
	}
	schedule := NewMaintenanceSchedule(now(), cfg.CheckInterval)
	return timerjob.New("factor_input_cache_maintenance", cfg.RebuildTimeout, func(ctx context.Context) error {
		if !cfg.Enabled || !schedule.TryBegin(now()) {
			return nil
		}
		defer schedule.Finish()
		return maintain(ctx)
	})
}
