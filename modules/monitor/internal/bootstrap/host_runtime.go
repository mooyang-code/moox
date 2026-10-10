package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/hostmetrics"
	"trpc.group/trpc-go/trpc-go/log"
)

func startHostStorageGate(ctx context.Context, cfg *config.Config, runtime *Runtime, gate *hostmetrics.StorageGate) {
	if cfg == nil || gate == nil || !cfg.Metrics.HostStorage.Enabled {
		return
	}
	interval := cfg.Metrics.HostStorage.MetadataRefreshInterval
	if interval <= 0 {
		interval = time.Minute
	}
	check := func() {
		checkCtx, cancel := context.WithTimeout(ctx, interval)
		defer cancel()
		if err := gate.Validate(checkCtx); err != nil {
			log.WarnContextf(ctx, "host storage schema check failed: %v", err)
		}
	}
	runtime.Go(func() {
		check()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				check()
			}
		}
	})
}
