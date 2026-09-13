package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/engine"
	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/pyruntime/process"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

const EngineAppID = "moox-factor-engine"

// EngineResources owns local computation resources, not catalog authority or
// ingress. Open, activate the startup catalog, then call StartCompute. Callers
// must Cancel, stop and join all consumers/catalog jobs, then Close. Cancellation
// deliberately does not close SQLite underneath in-flight callers.
type EngineResources struct {
	Store         *store.Store
	Storage       *storageio.Client
	OperationGate *taskrunner.OperationGate
	PythonPool    *engine.PythonWorkerPool
	Runner        *taskrunner.Service
	Cache         *inputcache.Runtime

	ctx          context.Context
	cancel       context.CancelFunc
	engineConfig EngineConfig
	mu           sync.Mutex
	closed       bool
	closeErr     error
}

func OpenEngineResources(ctx context.Context, cfg *EngineApplicationConfig) (*EngineResources, error) {
	if ctx == nil || cfg == nil {
		return nil, errors.New("engine context and configuration are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.EngineID) == "" {
		return nil, errors.New("engine_id is required")
	}
	if err := gatewayauth.ValidateTargetNode(cfg.Storage.GatewayNodeID); err != nil {
		return nil, err
	}
	if err := validateRoleStorage(cfg.Database, cfg.Storage, cfg.EventBus.URLs); err != nil {
		return nil, err
	}
	if strings.TrimSpace(os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")) == "" || strings.TrimSpace(os.Getenv("MOOX_STORAGE_VIEW_AUTH_SECRET")) == "" {
		return nil, errors.New("engine storage primary and view secrets are required")
	}
	if err := cfg.Cache.Validate(); err != nil {
		return nil, err
	}
	ec := cfg.Engine
	if ec.PythonWorkers <= 0 || ec.ViewReadWorkers <= 0 || ec.TaskTimeoutMS <= 0 || ec.ViewReadTimeoutMS <= 0 || strings.TrimSpace(ec.PythonBin) == "" || strings.TrimSpace(ec.WorkerPath) == "" || strings.TrimSpace(ec.FactorsDir) == "" {
		return nil, errors.New("engine worker counts, timeouts and Python paths are required")
	}
	credentials, err := gatewayauth.ResolveCredentials(cfg.Storage.KeyID, cfg.Storage.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load engine storage credentials: %w", err)
	}
	db, err := store.Open(&store.Options{Path: cfg.Database.Path, MaxIdleConns: cfg.Database.MaxIdleConns, MaxOpenConns: cfg.Database.MaxOpenConns, ConnMaxLifetime: cfg.Database.ConnMaxLifetime, ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime})
	if err != nil {
		return nil, err
	}
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return nil, errors.Join(fmt.Errorf("initialize engine runtime schema: %w", err), db.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	runCtx, cancel := context.WithCancel(ctx)
	var cache *inputcache.Runtime
	if cfg.Cache.Enabled {
		cache, err = inputcache.NewRuntime(runCtx, cfg.Cache, time.Now)
		if err != nil {
			cancel()
			return nil, errors.Join(fmt.Errorf("initialize engine input cache: %w", err), db.Close())
		}
	}
	return &EngineResources{
		Cache:         cache,
		Store:         db,
		Storage:       storageio.NewClientWithCredentials(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, credentials, EngineAuthInfo(false)).WithViewAuth(EngineAuthInfo(true)).WithOutputManifests(db.OutputManifests()),
		OperationGate: taskrunner.NewOperationGate(), ctx: runCtx, cancel: cancel, engineConfig: ec,
	}, nil
}

// EngineAuthInfo uses the engine's own storage authorization identity. View
// authentication never falls back to the primary secret.
func EngineAuthInfo(view bool) *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{AppId: EngineAppID, Operator: EngineAppID, RequestId: fmt.Sprintf("factor-engine-%d", time.Now().UnixNano())}
	variable := "MOOX_STORAGE_PRIMARY_AUTH_SECRET"
	if view {
		variable = "MOOX_STORAGE_VIEW_AUTH_SECRET"
	}
	if secret := os.Getenv(variable); strings.TrimSpace(secret) != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}

func (r *EngineResources) Context() context.Context { return r.ctx }
func (r *EngineResources) Cancel()                  { r.cancel() }

// StartCompute must follow successful catalog activation. On failure, callers
// still own the resources and must drain catalog jobs before closing them.
func (r *EngineResources) StartCompute() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("engine resources are closed")
	}
	if err := r.ctx.Err(); err != nil {
		return err
	}
	if r.Runner != nil {
		return nil
	}
	snapshot, err := r.Store.CatalogSnapshot(r.ctx)
	if err != nil {
		return fmt.Errorf("read engine startup catalog: %w", err)
	}
	if snapshot.Revision <= 0 {
		return errors.New("engine startup catalog has not been activated")
	}
	cfg := r.engineConfig
	workers, err := engine.NewPythonWorkerPool(r.ctx, cfg.PythonWorkers, process.Config{PythonBin: cfg.PythonBin, WorkerPath: cfg.WorkerPath, Args: []string{"--factors-dir", cfg.FactorsDir}, TaskTimeout: time.Duration(cfg.TaskTimeoutMS) * time.Millisecond, Limits: process.DefaultLimits()})
	if err != nil {
		return fmt.Errorf("start engine Python workers: %w", err)
	}
	if err := r.ctx.Err(); err != nil {
		return errors.Join(err, workers.Close())
	}
	r.PythonPool = workers
	r.Runner = taskrunner.NewService(cfg.PythonWorkers, r.Storage, workers,
		taskrunner.WithBatchExecution(cfg.BatchEnabled),
		taskrunner.WithViewReadConfig(cfg.ViewReadWorkers, time.Duration(cfg.ViewReadTimeoutMS)*time.Millisecond),
		taskrunner.WithTaskValidator(newTaskValidator(r.Store.Factors(), r.Store.Bindings())),
	)
	return nil
}

// Close is idempotent. It must not run until external users of Store, Storage,
// Runner and PythonPool have stopped; it does not itself join those callers.
func (r *EngineResources) Close() error {
	r.cancel()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	if r.PythonPool != nil {
		r.closeErr = r.PythonPool.Close()
	}
	if r.Cache != nil {
		r.closeErr = errors.Join(r.closeErr, r.Cache.Close())
	}
	r.closeErr = errors.Join(r.closeErr, r.Store.Close())
	return r.closeErr
}
