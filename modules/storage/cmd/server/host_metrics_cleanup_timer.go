//go:build legacy_storage

package main

import (
	"context"
	"fmt"
	"time"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	storagesvc "github.com/mooyang-code/moox/modules/storage/internal/service/primarystore"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

const hostMetricsCleanupTimerService = "trpc.moox.storage.host_metrics_cleanup.timer"

type hostMetricsCleanupAccess interface {
	CleanupExpiredHostMetrics(context.Context, storagesvc.HostMetricsCleanupOptions) (storagesvc.HostMetricsCleanupResult, error)
}

func registerHostMetricsCleanupTimer(s *server.Server, access hostMetricsCleanupAccess, storage storageconfig.StorageConfig) error {
	if !storage.HasRole("primary") {
		return nil
	}
	if s == nil {
		return fmt.Errorf("host metrics cleanup timer requires a tRPC server")
	}
	service := s.Service(hostMetricsCleanupTimerService)
	if service == nil {
		return fmt.Errorf("host metrics cleanup timer service %q is not configured", hostMetricsCleanupTimerService)
	}
	cfg := storage.Maintenance.HostMetricsCleanup
	if !cfg.IsEnabled() {
		timer.RegisterHandlerService(service, func(context.Context) error { return nil })
		return nil
	}
	if access == nil {
		return fmt.Errorf("enabled host metrics cleanup requires the access service")
	}
	job, err := newHostMetricsCleanupJob(access, cfg, time.Minute, func() time.Time { return time.Now().UTC() })
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}

func newHostMetricsCleanupJob(access hostMetricsCleanupAccess, cfg storageconfig.HostMetricsCleanupConfig, timeout time.Duration, now func() time.Time) (*timerjob.Job, error) {
	if access == nil {
		return nil, fmt.Errorf("host metrics cleanup access service is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	maxAge, err := time.ParseDuration(cfg.MaxAge)
	if err != nil {
		return nil, fmt.Errorf("parse host metrics cleanup max age: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	return timerjob.New("storage_host_metrics_cleanup", timeout, func(ctx context.Context) error {
		result, err := access.CleanupExpiredHostMetrics(ctx, storagesvc.HostMetricsCleanupOptions{
			SpaceID:          "moox_system",
			DatasetIDs:       append([]string(nil), cfg.DatasetIDs...),
			MaxAge:           maxAge,
			BatchSize:        cfg.BatchSize,
			MaxBatchesPerRun: cfg.MaxBatchesPerRun,
			Now:              now(),
		})
		if result.Deleted > 0 || result.Batches > 0 {
			log.InfoContextf(ctx, "host metrics cleanup deleted=%d batches=%d", result.Deleted, result.Batches)
		}
		if err != nil {
			log.ErrorContextf(ctx, "host metrics cleanup failed after deleted=%d batches=%d: %v", result.Deleted, result.Batches, err)
		}
		return err
	})
}
