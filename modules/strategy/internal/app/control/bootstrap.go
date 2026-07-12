package control

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/healthz"
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
	defer func() {
		if keepResources {
			return
		}
		_ = db.Close()
		if eng != nil {
			_ = eng.Close()
		}
	}()
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		return nil, nil, fmt.Errorf("apply strategy schema: %w", err)
	}
	repo := store.New(db.DB())
	if cfg.WorkerPath != "" {
		eng, err = engine.New(ctx, cfg.PythonBin, cfg.WorkerPath)
		if err != nil {
			return nil, nil, err
		}
	} else if cfg.LiveEnabled {
		return nil, nil, fmt.Errorf("worker_path is required when live is enabled")
	}
	// pyruntime workers are started lazily on the first LOAD. Do not report
	// them as ready before a real handshake has completed.
	service := &rpc.Service{Repo: repo, Registry: &registry.Service{Repo: repo}, Engine: eng, Workers: cfg.Workers, ReadyWorkers: 0}
	strategypb.RegisterStrategyMgrService(s, service)
	healthState := health.New("strategy", "strategy", "", "")
	healthState.SnapshotFunc = strategyHealthSnapshot(db, eng, cfg.Workers, healthState)
	healthState.SetReady(true)
	if err := health.Register(s.Service("trpc.moox.strategy.Health"), healthState); err != nil {
		return nil, nil, fmt.Errorf("register strategy health service: %w", err)
	}
	closeFn := func() error {
		var engineErr error
		if eng != nil {
			engineErr = eng.Close()
		}
		dbErr := db.Close()
		if engineErr != nil {
			return engineErr
		}
		return dbErr
	}
	keepResources = true
	return s, closeFn, nil
}

func strategyHealthSnapshot(db *store.Manager, eng *engine.Engine, workers int, state *health.State) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		databaseReady := db != nil && db.DB() != nil && db.DB().WithContext(ctx).Exec("SELECT 1").Error == nil
		engineReady := eng != nil
		ready := databaseReady && engineReady && state.Ready()
		rsp := healthz.Base("strategy", "strategy", "", "", state.StartedAt, ready)
		rsp.Details = map[string]any{"database_ready": databaseReady, "python_engine_ready": engineReady, "workers": workers}
		return rsp
	}
}
