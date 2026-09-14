package bootstrap

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
)

func StartControlRecalc(ctx context.Context, cfg CatalogBusConfig, svc *trigger.RecalcService) (func() error, error) {
	if ctx == nil || svc == nil {
		return nil, errors.New("control recalc context and service are required")
	}
	conn, err := connectCatalog(ctx, cfg, "moox-factor-control-recalc")
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	subs, err := trigger.ServeRecalcQueue(runCtx, conn, svc)
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			cancel()
			for _, sub := range subs {
				if sub != nil {
					_ = sub.Unsubscribe()
				}
			}
			closeErr = conn.Close()
		})
		return closeErr
	}, nil
}

func StartEngineRecalc(ctx context.Context, cfg *EngineApplicationConfig, replica catalogsync.SnapshotReader, executor trigger.RecalcExecutor) (func() error, error) {
	if ctx == nil || cfg == nil || executor == nil {
		return nil, errors.New("engine recalc context, config and executor are required")
	}
	conn, err := connectCatalog(ctx, CatalogBusConfig{URLs: cfg.EventBus.URLs, CredentialFile: cfg.EventBus.CredentialFile}, "moox-factor-engine-recalc")
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	interval := 2 * time.Second
	if cfg.CatalogPollInterval > 0 && cfg.CatalogPollInterval < interval {
		interval = cfg.CatalogPollInterval
	}
	var active sync.WaitGroup
	active.Add(1)
	go func() {
		defer active.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		runEngineRecalcTick(runCtx, conn, cfg, replica, executor)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				runEngineRecalcTick(runCtx, conn, cfg, replica, executor)
			}
		}
	}()
	var once sync.Once
	var closeErr error
	return func() error {
		once.Do(func() {
			cancel()
			active.Wait()
			closeErr = conn.Close()
		})
		return closeErr
	}, nil
}

func runEngineRecalcTick(ctx context.Context, bus trigger.RecalcBus, cfg *EngineApplicationConfig, replica catalogsync.SnapshotReader, executor trigger.RecalcExecutor) {
	if ctx.Err() != nil {
		return
	}
	tickCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	desired, applied := int64(0), int64(0)
	if replica != nil {
		if snapshot, err := replica.CatalogSnapshot(tickCtx); err == nil && snapshot != nil {
			desired, applied = snapshot.Revision, snapshot.Revision
		}
	}
	_ = trigger.ReportRecalcHeartbeat(tickCtx, bus, cfg.EngineID, desired, applied)
	job, found, err := trigger.ClaimRecalcJob(tickCtx, bus)
	if err != nil || !found {
		return
	}
	runErr := executor.Run(ctx, job)
	if errors.Is(runErr, trigger.ErrRecalcEngineOffline) {
		_ = trigger.ReportRecalcJob(ctx, bus, job.JobID, trigger.RecalcAccepted, "", "")
		return
	}
	status, failure, message := trigger.RecalcSucceeded, "", ""
	if runErr != nil {
		status = trigger.RecalcFailed
		message = runErr.Error()
		switch {
		case errors.Is(runErr, trigger.ErrRecalcMissingInput):
			failure = trigger.RecalcFailureMissingInput
		case errors.Is(runErr, trigger.ErrRecalcViewWaiting):
			failure = trigger.RecalcFailureViewWaiting
		default:
			failure = trigger.RecalcFailureAlgorithm
		}
	}
	_ = trigger.ReportRecalcJob(ctx, bus, job.JobID, status, failure, message)
}
