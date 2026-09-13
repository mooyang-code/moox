package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	factorobservability "github.com/mooyang-code/moox/modules/factor/internal/observability"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	factorsvc "github.com/mooyang-code/moox/modules/factor/internal/rpc"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/taskrunner"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"trpc.group/trpc-go/trpc-go/server"
)

const controlHealthService = "trpc.moox.factor.Health"

// ControlRuntime owns FactorMgr, catalog snapshot replies and metadata
// reconciliation. It must not start Python workers or realtime consumers.
type ControlRuntime struct {
	Resources      *ControlResources
	Health         *health.State
	cancel         context.CancelFunc
	stopCatalog    func() error
	waitReconcile  func()
	closeResources func() error
	once           sync.Once
	mu             sync.RWMutex
	closed         bool
	err            error
}

func validateControlRuntime(s *server.Server, cfg *ControlConfig) error {
	if s == nil || cfg == nil {
		return errors.New("control server and config are required")
	}
	if s.Service(controlHealthService) == nil {
		return errors.New("control health service is required")
	}
	if s.Service("trpc.moox.factor.FactorMgr") == nil && s.Service("trpc.moox.factor.FactorMgr.trpc") == nil {
		return errors.New("control FactorMgr service is required")
	}
	for _, name := range []string{engineHealthService, catalogTimerService, cacheTimerService} {
		if s.Service(name) != nil {
			return fmt.Errorf("control must not configure engine service %s", name)
		}
	}
	return nil
}

func InitializeControl(ctx context.Context, s *server.Server, cfg *ControlConfig) (_ *ControlRuntime, err error) {
	if ctx == nil {
		return nil, errors.New("control context is required")
	}
	if err := validateControlRuntime(s, cfg); err != nil {
		return nil, err
	}
	resources, err := OpenControlResources(ctx, cfg)
	if err != nil {
		return nil, err
	}
	r := &ControlRuntime{Resources: resources, cancel: resources.Cancel, closeResources: resources.Close}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	if err = validateStartupFactorContracts(resources.Context(), resources.Registry); err != nil {
		return nil, err
	}
	r.stopCatalog, err = StartControlCatalog(resources.Context(), cfg.EventBus, resources.Store)
	if err != nil {
		return nil, err
	}
	credentials, err := gatewayauth.ResolveCredentials(cfg.Storage.KeyID, cfg.Storage.HMACKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load control storage credentials: %w", err)
	}
	storage := storageio.NewClientWithCredentials(cfg.Storage.GatewayTarget, cfg.Storage.GatewayNodeID, credentials, factorAuthInfo()).
		WithViewAuth(factorViewAuthInfo()).
		WithOutputManifests(resources.Store.OutputManifests())
	inventory := controlRealtimeInventory(s, resources)
	factorService := factorsvc.NewWithRuntime(
		resources.Store,
		nil,
		factorsvc.WithFactorsDir(cfg.ArtifactsDir),
		factorsvc.WithMetadataSync(resources.Metadata),
		factorsvc.WithRealtimeInventory(inventory),
		factorsvc.WithOperationGate(taskrunner.NewOperationGate()),
		factorsvc.WithBindingOutputCleaner(bindingOutputCleaner{storage: storage, manifests: resources.Store.OutputManifests()}),
		factorsvc.WithBindingSchemaCleaner(resources.Metadata),
	)
	registered := false
	for _, name := range []string{"trpc.moox.factor.FactorMgr", "trpc.moox.factor.FactorMgr.trpc"} {
		if service := s.Service(name); service != nil {
			factorpb.RegisterFactorMgrService(service, factorService)
			registered = true
		}
	}
	if !registered {
		return nil, errors.New("control FactorMgr service is required")
	}
	reconcileCtx, cancelReconcile := context.WithCancel(resources.Context())
	r.waitReconcile = startPendingBindingReconciler(reconcileCtx, factorService)
	startManifestRetention(reconcileCtx, resources.Store.OutputManifests(), time.Hour, factorManifestRetention())
	r.Health = health.New("factor", "factor-01", "", "")
	r.Health.SnapshotFunc = r.snapshot
	if err = health.Register(s.Service(controlHealthService), r.Health); err != nil {
		cancelReconcile()
		if r.waitReconcile != nil {
			r.waitReconcile()
		}
		return nil, err
	}
	r.Health.SetReady(true)
	s.RegisterOnShutdown(func() {
		r.Health.SetReady(false)
		cancelReconcile()
		resources.Cancel()
	})
	return r, nil
}

func controlRealtimeInventory(s *server.Server, resources *ControlResources) *factorobservability.RealtimeInventory {
	if s == nil || s.Service("trpc.moox.factor.metrics.timer") == nil || resources == nil {
		return nil
	}
	datasetMetrics, err := factorobservability.NewDatasetMetrics(prometheus.DefaultRegisterer)
	if err != nil {
		return nil
	}
	moduleMetrics, err := report.NewModuleMetrics(prometheus.DefaultRegisterer, "factor", report.HealthCheckIDsForModule("factor"))
	if err != nil {
		return nil
	}
	if _, err := report.NewDatasetModuleObserver(datasetMetrics, moduleMetrics, "calculate", "factor-calculation"); err != nil {
		return nil
	}
	inventory := factorobservability.NewRealtimeInventory(resources.Store.Bindings(), datasetMetrics)
	registerMetricsReporter(s, inventory)
	return inventory
}

// Close cancels catalog replies and management work before releasing SQLite.
func (r *ControlRuntime) Close() error {
	r.once.Do(func() {
		if r.Health != nil {
			r.Health.SetReady(false)
		}
		if r.cancel != nil {
			r.cancel()
		}
		if r.waitReconcile != nil {
			r.waitReconcile()
		}
		if r.stopCatalog != nil {
			r.err = errors.Join(r.err, r.stopCatalog())
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		r.closed = true
		if r.closeResources != nil {
			r.err = errors.Join(r.err, r.closeResources())
		}
	})
	return r.err
}

func (r *ControlRuntime) snapshot(ctx context.Context) healthz.Response {
	r.mu.RLock()
	defer r.mu.RUnlock()
	resources := r.Resources
	alive := !r.closed && resources != nil && resources.Context().Err() == nil
	database := false
	if alive {
		probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		database = resources.Store.Ping(probe) == nil
		cancel()
	}
	rsp := healthz.Base("factor", r.Health.InstanceID, "", "", r.Health.StartedAt, alive && database)
	rsp.Details = map[string]any{
		"control_context": alive,
		"database":        database,
		"catalog_server":  alive && !r.closed,
		"python_workers":  0,
		"execution_scope": "control-catalog",
	}
	return rsp
}
