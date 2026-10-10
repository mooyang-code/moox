package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monmetrics "github.com/mooyang-code/moox/modules/monitor/internal/metrics"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const monitorDataCleanupTimerService = "trpc.moox.monitor.data_cleanup.timer"

type monitorDataCleanupOps struct {
	retention     time.Duration
	now           func() time.Time
	deleteResults func(context.Context, time.Time) error
	deleteAlerts  func(context.Context, time.Time) error
	pruneDedupe   func(context.Context, time.Time) error
	pruneSeries   func(context.Context, time.Time) error
}

func runMonitorDataCleanup(ctx context.Context, ops monitorDataCleanupOps) error {
	now := time.Now().UTC()
	if ops.now != nil {
		now = ops.now().UTC()
	}
	cutoff := now.Add(-ops.retention)
	var errs []error
	if ops.deleteResults != nil {
		if err := ops.deleteResults(ctx, cutoff); err != nil {
			errs = append(errs, fmt.Errorf("delete monitor results: %w", err))
		}
	}
	if ops.deleteAlerts != nil {
		if err := ops.deleteAlerts(ctx, cutoff); err != nil {
			errs = append(errs, fmt.Errorf("delete monitor alert events: %w", err))
		}
	}
	if ops.pruneDedupe != nil {
		if err := ops.pruneDedupe(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("prune metric message dedupe: %w", err))
		}
	}
	if ops.pruneSeries != nil {
		if err := ops.pruneSeries(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("prune retired metric series: %w", err))
		}
	}
	return errors.Join(errs...)
}

func registerMonitorDataCleanupTimer(s *server.Server, cfg *config.Config, runtime *Runtime) error {
	if s == nil {
		return fmt.Errorf("monitor data cleanup timer requires a tRPC server")
	}
	service := s.Service(monitorDataCleanupTimerService)
	if service == nil {
		return fmt.Errorf("monitor data cleanup timer service %q is not configured", monitorDataCleanupTimerService)
	}
	ops := monitorDataCleanupOps{
		now: func() time.Time { return time.Now().UTC() },
	}
	if cfg != nil {
		ops.retention = time.Duration(cfg.Scheduler.ResultRetentionDays) * 24 * time.Hour
	}
	if runtime != nil && runtime.Store != nil && runtime.Repositories != nil && ops.retention > 0 {
		ops.deleteResults = func(ctx context.Context, cutoff time.Time) error {
			_, err := runtime.Repositories.Results.DeleteOlderThan(ctx, cutoff)
			return err
		}
		ops.deleteAlerts = func(ctx context.Context, cutoff time.Time) error {
			_, err := runtime.Repositories.Alerts.DeleteEventsOlderThan(ctx, cutoff)
			return err
		}
	}
	if runtime != nil && runtime.MetricStores != nil && runtime.MetricStores.Messages != nil {
		ops.pruneDedupe = func(ctx context.Context, now time.Time) error {
			_, err := runtime.MetricStores.Messages.PruneDedupe(ctx, now)
			return err
		}
		ops.pruneSeries = func(ctx context.Context, now time.Time) error {
			var registered []monmetrics.ReporterPlacement
			if runtime.Repositories != nil && runtime.Repositories.Topology != nil {
				snapshot, err := runtime.Repositories.Topology.Snapshot(ctx)
				if err != nil {
					return err
				}
				if snapshot != nil {
					registered = make([]monmetrics.ReporterPlacement, 0, len(snapshot.Placements))
					for _, placement := range snapshot.Placements {
						registered = append(registered, monmetrics.ReporterPlacement{NodeID: placement.HostID, ServiceName: placement.ComponentID})
					}
				}
			}
			_, err := runtime.MetricStores.Messages.PruneRetiredSeries(ctx, now, registered)
			return err
		}
	}
	job, err := timerjob.New("monitor_data_cleanup", 2*time.Minute, func(ctx context.Context) error {
		return runMonitorDataCleanup(ctx, ops)
	})
	if err != nil {
		return err
	}
	timer.RegisterHandlerService(service, job.Handle)
	return nil
}
