package bootstrap

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/mooyang-code/moox/packages/timerjob"
	"trpc.group/trpc-go/trpc-database/timer"
	"trpc.group/trpc-go/trpc-go/server"
)

const catalogTimerService = "trpc.moox.factor.engine.catalog.timer"

func connectCatalog(ctx context.Context, cfg CatalogBusConfig, name string) (*jetstream.Client, error) {
	bus := jetstream.ConfigFromEnv(cfg.URLs, name)
	if cfg.CredentialFile != "" {
		if err := bus.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.CredentialFile)); err != nil {
			return nil, fmt.Errorf("load catalog credentials: %w", err)
		}
	}
	return jetstream.Connect(ctx, bus)
}

// StartControlCatalog owns a dedicated authenticated connection. Closing it
// removes the responder before the caller closes the authoritative store.
func StartControlCatalog(ctx context.Context, cfg CatalogBusConfig, reader catalogsync.SnapshotReader) (func() error, error) {
	conn, err := connectCatalog(ctx, cfg, "moox-factor-control-catalog")
	if err != nil {
		return nil, err
	}
	if _, err := catalogsync.ServeSnapshots(ctx, conn, reader); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn.Close, nil
}

// StartEngineCatalog requires an authoritative startup snapshot before any
// computation consumer starts. The returned connection must outlive the timer.
func StartEngineCatalog(ctx context.Context, s *server.Server, cfg *EngineApplicationConfig, replica catalogsync.Replica, activate catalogsync.Activation) (func() error, error) {
	if cfg == nil || s == nil || s.Service(catalogTimerService) == nil {
		return nil, fmt.Errorf("factor engine catalog timer is not configured")
	}
	conn, err := connectCatalog(ctx, CatalogBusConfig{URLs: cfg.EventBus.URLs, CredentialFile: cfg.EventBus.CredentialFile}, "moox-factor-engine-catalog")
	if err != nil {
		return nil, err
	}
	job, err := prepareEngineCatalog(ctx, cfg, replica, activate, func(ctx context.Context) (*domain.CatalogSnapshot, error) {
		return catalogsync.FetchSnapshot(ctx, conn)
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if err := s.Service(catalogTimerService).Register(&timer.ServiceDesc, job); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("register catalog timer: %w", err)
	}
	return conn.Close, nil
}

func prepareEngineCatalog(ctx context.Context, cfg *EngineApplicationConfig, replica catalogsync.Replica, activate catalogsync.Activation, fetch func(context.Context) (*domain.CatalogSnapshot, error)) (*timerjob.Job, error) {
	syncer, err := catalogsync.NewSynchronizer(cfg.Engine.FactorsDir, replica, fetch, activate)
	if err != nil {
		return nil, err
	}
	timeout := cfg.CatalogSyncTimeout
	job, err := catalogsync.NewReconcileJob(cfg.CatalogPollInterval, timeout, time.Now, syncer.Sync)
	if err != nil {
		return nil, err
	}
	startupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if _, err := syncer.Sync(startupCtx); err != nil {
		return nil, fmt.Errorf("initialize engine catalog: %w", err)
	}
	return job, nil
}
