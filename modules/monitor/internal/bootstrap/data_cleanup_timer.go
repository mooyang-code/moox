package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const monitorDataCleanupTimerService = "trpc.moox.monitor.data_cleanup.timer"

const (
	monitorMetricEvaluationRetention = 14 * 24 * time.Hour
	monitorCleanupBatchSize          = 500
	monitorCleanupMaxBatches         = 10
)

type monitorDataCleanupOps struct {
	retention               time.Duration
	evaluationRetention     time.Duration
	now                     func() time.Time
	deleteResults           func(context.Context, time.Time) error
	deleteAlerts            func(context.Context, time.Time) error
	deleteMetricEvaluations func(context.Context, time.Time, int) (int64, error)
	pruneDedupe             func(context.Context, time.Time) error
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
	if ops.deleteMetricEvaluations != nil && ops.evaluationRetention > 0 {
		evaluationCutoff := now.Add(-ops.evaluationRetention)
		for batch := 0; batch < monitorCleanupMaxBatches; batch++ {
			deleted, err := ops.deleteMetricEvaluations(ctx, evaluationCutoff, monitorCleanupBatchSize)
			if err != nil {
				errs = append(errs, fmt.Errorf("delete monitor metric rule evaluations: %w", err))
				break
			}
			if deleted < monitorCleanupBatchSize {
				break
			}
		}
	}
	if ops.pruneDedupe != nil {
		if err := ops.pruneDedupe(ctx, now); err != nil {
			errs = append(errs, fmt.Errorf("prune metric message dedupe: %w", err))
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
		now:                 func() time.Time { return time.Now().UTC() },
		evaluationRetention: monitorMetricEvaluationRetention,
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
	}
	if runtime != nil && runtime.MetricStores != nil && runtime.MetricStores.Rules != nil {
		ops.deleteMetricEvaluations = runtime.MetricStores.Rules.DeleteEvaluationsOlderThan
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
