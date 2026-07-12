package control

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/health"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
	"github.com/mooyang-code/moox/modules/strategy/internal/repository"
	"github.com/mooyang-code/moox/modules/strategy/internal/rpc"
	strategypb "github.com/mooyang-code/moox/modules/strategy/proto/strategygen"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/healthz"
	"gorm.io/gorm"
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
	if err := os.MkdirAll(filepath.Dir(cfg.Database), 0o755); err != nil {
		return nil, nil, err
	}
	db, err := gorm.Open(sqlite.Open(cfg.Database), &gorm.Config{})
	if err != nil {
		return nil, nil, err
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		return nil, nil, fmt.Errorf("apply strategy schema: %w", err)
	}
	repo := repository.New(db)
	var eng *engine.Engine
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
		if sqlDB, err := db.DB(); err == nil {
			defer sqlDB.Close()
		}
		if eng == nil {
			return nil
		}
		return eng.Close()
	}
	return s, closeFn, nil
}

func strategyHealthSnapshot(db *gorm.DB, eng *engine.Engine, workers int, state *health.State) func(context.Context) healthz.Response {
	return func(ctx context.Context) healthz.Response {
		databaseReady := db != nil && db.WithContext(ctx).Exec("SELECT 1").Error == nil
		engineReady := eng != nil
		ready := databaseReady && engineReady && state.Ready()
		rsp := healthz.Base("strategy", "strategy", "", "", state.StartedAt, ready)
		rsp.Details = map[string]any{"database_ready": databaseReady, "python_engine_ready": engineReady, "workers": workers}
		return rsp
	}
}
