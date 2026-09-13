package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/catalogsync"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/packages/healthz"
	"trpc.group/trpc-go/trpc-go/server"
)

const engineHealthService = "trpc.moox.factor.engine.Health"

// EngineRuntime runs the subject/time-series path only. Cache and cross/barrier
// execution are not integrated and must not be advertised as available.
type EngineRuntime struct {
	Resources      *EngineResources
	Health         *health.State
	consumer       realtimeStatus
	cancel         context.CancelFunc
	stopSubject    func() error
	stopCatalog    func() error
	closeResources func() error
	once           sync.Once
	mu             sync.RWMutex
	closed         bool
	err            error
}

func validateEngineRuntime(s *server.Server, cfg *EngineApplicationConfig) error {
	if s == nil || cfg == nil {
		return errors.New("engine server and config are required")
	}
	if cfg.Cache.Enabled {
		return errors.New("engine cache integration is not implemented; cache.enabled must be false")
	}
	if s.Service(engineHealthService) == nil || s.Service(catalogTimerService) == nil {
		return errors.New("engine health and catalog timer services are required")
	}
	for _, name := range []string{"trpc.moox.factor.FactorMgr", "trpc.moox.factor.FactorMgr.trpc"} {
		if s.Service(name) != nil {
			return fmt.Errorf("engine must not configure control service %s", name)
		}
	}
	return nil
}

func InitializeEngine(ctx context.Context, s *server.Server, cfg *EngineApplicationConfig) (_ *EngineRuntime, err error) {
	if ctx == nil {
		return nil, errors.New("engine context is required")
	}
	if err := validateEngineRuntime(s, cfg); err != nil {
		return nil, err
	}
	resources, err := OpenEngineResources(ctx, cfg)
	if err != nil {
		return nil, err
	}
	r := &EngineRuntime{Resources: resources, cancel: resources.Cancel, closeResources: resources.Close}
	defer func() {
		if err != nil {
			err = errors.Join(err, r.Close())
		}
	}()
	if err = registerEngineCache(s, resources.Cache); err != nil {
		return nil, err
	}
	activate, err := catalogsync.EngineActivation(resources.OperationGate, resources.Store.OutputManifests(), resources.Storage)
	if err != nil {
		return nil, err
	}
	r.stopCatalog, err = StartEngineCatalog(resources.Context(), s, cfg, resources.Store, subjectOnlyActivation(activate))
	if err != nil {
		return nil, err
	}
	if err = resources.StartCompute(); err != nil {
		return nil, err
	}
	consumer, err := StartEngineSubject(resources.Context(), cfg, resources.Store, resources.Runner, resources.OperationGate)
	if err != nil {
		return nil, err
	}
	r.consumer, r.stopSubject = consumer, consumer.Close
	r.Health = health.New("factor-engine", cfg.EngineID, "", "")
	r.Health.SnapshotFunc = r.snapshot
	if err = health.Register(s.Service(engineHealthService), r.Health); err != nil {
		return nil, err
	}
	r.Health.SetReady(true)
	s.RegisterOnShutdown(func() {
		r.Health.SetReady(false)
		resources.Cancel()
	})
	return r, nil
}

func subjectOnlyActivation(activate catalogsync.Activation) catalogsync.Activation {
	return func(ctx context.Context, previous, next domain.CatalogSnapshot, commit func() error) error {
		for _, factor := range next.Factors {
			if factor.FactorType != domain.FactorTypeTimeSeries {
				return fmt.Errorf("engine subject-only runtime cannot activate factor %q of type %q", factor.FactorID, factor.FactorType)
			}
		}
		return activate(ctx, previous, next, commit)
	}
}

// Close cancels execution and joins ingress before releasing Python and SQLite.
func (r *EngineRuntime) Close() error {
	r.once.Do(func() {
		if r.Health != nil {
			r.Health.SetReady(false)
		}
		if r.cancel != nil {
			r.cancel()
		}
		if r.stopSubject != nil {
			r.err = errors.Join(r.err, r.stopSubject())
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

func (r *EngineRuntime) snapshot(ctx context.Context) healthz.Response {
	r.mu.RLock()
	defer r.mu.RUnlock()
	resources := r.Resources
	alive := !r.closed && resources != nil && resources.Context().Err() == nil
	database, python, consumer := false, false, false
	if alive {
		probe, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
		database = resources.Store.Ping(probe) == nil
		cancel()
		python = resources.PythonPool != nil && resources.PythonPool.Status().Ready
		consumer = r.consumer != nil && r.consumer.Ready() && !r.consumer.Status().Stalled
	}
	rsp := healthz.Base("factor-engine", r.Health.InstanceID, "", "", r.Health.StartedAt, alive && database && python && consumer)
	rsp.Details = map[string]any{"engine_context": alive, "database": database, "python": python, "subject_consumer": consumer, "execution_scope": "subject-time-series", "cache_integrated": false, "cross_barrier_integrated": false}
	return rsp
}
