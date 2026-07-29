package bootstrap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	strategyaction "github.com/mooyang-code/moox/modules/strategy/internal/action"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	strategyoutbox "github.com/mooyang-code/moox/modules/strategy/internal/outbox"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/jetstream"
	"trpc.group/trpc-go/trpc-go/server"
)

// Initialize opens the control-plane database, prepares the Python engine and
// registers the StrategyMgr service before the tRPC server starts listening.
func Initialize(ctx context.Context, s *server.Server, cfg Config) (*server.Server, func() error, error) {
	if _, err := os.Stat(cfg.WorkerPath); err != nil {
		return nil, nil, fmt.Errorf("strategy worker path: %w", err)
	}
	if _, err := exec.LookPath(cfg.PythonBin); err != nil {
		return nil, nil, fmt.Errorf("strategy python executable: %w", err)
	}
	db, err := store.Open(cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	keepResources := false
	var eng *engine.Engine
	var eventRuntime *strategyoutbox.Runtime
	defer func() {
		if keepResources {
			return
		}
		if eventRuntime != nil {
			_ = eventRuntime.Close()
		}
		if eng != nil {
			_ = eng.Close()
		}
		_ = db.Close()
	}()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return nil, nil, fmt.Errorf("apply strategy schema: %w", err)
	}
	repo := db
	if cfg.WorkerPath != "" {
		eng, err = engine.NewWithWorkers(ctx, cfg.PythonBin, cfg.WorkerPath, cfg.Workers)
		if err != nil {
			return nil, nil, err
		}
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, workerProbeTimeout())
	err = eng.Probe(probeCtx)
	probeCancel()
	if err != nil {
		return nil, nil, err
	}
	eventRuntime, err = newEventBusRuntime(db, cfg)
	if err != nil {
		return nil, nil, err
	}
	if err := eventRuntime.Start(ctx); err != nil {
		return nil, nil, err
	}
	if _, err := registerMetricsReporter(s); err != nil {
		return nil, nil, err
	}
	service := newRPCService(repo, eng, cfg)
	strategypb.RegisterStrategyMgrService(s, service)
	healthState := health.New("strategy", "strategy", "", "")
	healthState.SnapshotFunc = strategyHealthSnapshot(db, eventRuntime, cfg.Workers, healthState)
	healthState.SetReady(true)
	if err := health.Register(s.Service("trpc.moox.strategy.Health"), healthState); err != nil {
		return nil, nil, fmt.Errorf("register strategy health service: %w", err)
	}
	closeFn := func() error {
		var eventBusErr error
		if eventRuntime != nil {
			eventBusErr = eventRuntime.Close()
		}
		var engineErr error
		if eng != nil {
			engineErr = eng.Close()
		}
		dbErr := db.Close()
		if eventBusErr != nil {
			return eventBusErr
		}
		if engineErr != nil {
			return engineErr
		}
		return dbErr
	}
	keepResources = true
	return s, closeFn, nil
}

func workerProbeTimeout() time.Duration {
	return 30 * time.Second
}

func newEventBusRuntime(repo *store.Store, cfg Config) (*strategyoutbox.Runtime, error) {
	connector := func(ctx context.Context) (strategyoutbox.JetStreamClient, error) {
		jsConfig := jetstream.ConfigFromEnv(cfg.EventBus.URLs, "moox-strategy")
		if jsConfig.Credentials == "" && jsConfig.Username == "" && strings.TrimSpace(cfg.EventBus.CredentialFile) != "" {
			if err := jsConfig.ApplyCredentialFile(jetstream.ExpandCredentialPath(cfg.EventBus.CredentialFile)); err != nil {
				return nil, err
			}
		}
		jsConfig.ConnectTimeout = cfg.EventBus.ConnectTimeout
		client, err := jetstream.Connect(ctx, jsConfig)
		if err != nil {
			return nil, err
		}
		return strategyoutbox.NewManagedClient(client)
	}
	return strategyoutbox.NewRuntime(strategyoutbox.RuntimeConfig{
		Connector: connector, Store: repo, InstanceID: cfg.InstanceID,
		Probe: func(ctx context.Context, client strategyoutbox.JetStreamClient) error {
			return strategyoutbox.ValidateJetStreamPublisher(ctx, client, cfg.InstanceID)
		},
		RelayInterval: cfg.EventBus.RelayInterval, ReconnectInterval: cfg.EventBus.ReconnectInterval,
		BatchSize: cfg.EventBus.RelayBatchSize,
	})
}

func newRPCService(repo *store.Store, eng *engine.Engine, cfg Config) *rpc.Service {
	return &rpc.Service{
		Repo: repo, Registry: &registry.Service{Repo: repo}, Runtime: eng,
		Results: &strategyaction.Service{Repo: repo},
		Workers: cfg.Workers,
		LogicalAccounts: newLogicalAccountOwnerClient(
			cfg.LogicalAccountTarget,
			cfg.LogicalAccountTimeout,
		),
	}
}

func strategyHealthSnapshot(db *store.Store, eventRuntime *strategyoutbox.Runtime, workers int, state *health.State) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		databaseReady := db != nil && db.Ping(ctx) == nil
		workerReady := state.Ready()
		eventBusConnected := eventRuntime != nil && eventRuntime.Connected()
		stats, statsErr := db.PendingOutboxStats(ctx)
		oldestAge := 0.0
		if !stats.OldestPending.IsZero() {
			oldestAge = max(0, time.Since(stats.OldestPending).Seconds())
		}
		ready := databaseReady && workerReady && eventBusConnected && statsErr == nil
		rsp := healthz.Base("strategy", "strategy", "", "", state.StartedAt, ready)
		rsp.Details = map[string]any{
			"database_ready": databaseReady, "python_worker_ready": workerReady, "workers": workers,
			"eventbus_connected": eventBusConnected, "outbox_pending_count": stats.PendingCount,
			"oldest_outbox_age_seconds": oldestAge,
		}
		return rsp
	}
}
