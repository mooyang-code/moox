package unitruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Maintenance scopes installation work and lifecycle calls to one host/root.
// It is valid only during WithMaintenance's callback. A lifecycle call reuses
// the same open lock description, including kernel validation of the descriptor.
type Maintenance struct {
	mu           sync.Mutex
	lock         *os.File
	hostID, root string
	installation *installation
	bootstrap    *bootstrap
}

func WithMaintenance(ctx context.Context, deploymentRoot, hostID string, options Options, work func(*Maintenance) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if work == nil || !servicecatalog.ValidHostID(hostID) {
		return errors.New("maintenance requires a canonical host and callback")
	}
	leave, err := options.enterScope(hostID, deploymentRoot)
	if err != nil {
		return err
	}
	defer leave()
	if err := physicalDirectory(deploymentRoot); err != nil {
		return err
	}
	state, err := openRuntime(Plan{HostID: hostID, DeploymentRoot: deploymentRoot})
	if err != nil {
		return err
	}
	defer state.close()
	lock, _, err := acquireLock(ctx, state.run, options.MaintenanceLockHeld, options.MaintenanceLockFD, false)
	if err != nil {
		return err
	}
	guard := &Maintenance{lock: lock, hostID: hostID, root: deploymentRoot}
	defer func() { guard.mu.Lock(); defer guard.mu.Unlock(); guard.lock.Close(); guard.lock = nil }()
	if err := state.ensureHostIdentity(); err != nil {
		return err
	}
	if pending, err := state.pendingBootstrap(); err != nil {
		return err
	} else if pending != nil && !options.MaintenanceLockHeld && options.BootstrapID != pending.RequestSHA256 {
		return errors.New("an interrupted bootstrap requires its matching coordinator")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return work(guard)
}

// UseLock lets a host coordinator call other maintenance-aware libraries
// synchronously with the same kernel-verified lock. The callback must use the
// supplied options rather than calling methods on this same guard. These
// options expire when this callback returns. Calls that have already
// entered the scope finish before the descriptor can be released.
func (m *Maintenance) UseLock(ctx context.Context, work func(Options) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil || work == nil {
		return errors.New("maintenance requires an active scoped lock callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	scope := &scopedLock{active: true, hostID: m.hostID, root: m.root}
	defer func() {
		scope.mu.Lock()
		defer scope.mu.Unlock()
		scope.active = false
	}()
	return work(Options{MaintenanceLockHeld: true, MaintenanceLockFD: int(m.lock.Fd()), scope: scope})
}

type scopedLock struct {
	mu           sync.RWMutex
	active       bool
	hostID, root string
}

func (options Options) enterScope(hostID, root string) (func(), error) {
	if options.scope == nil {
		return func() {}, nil
	}
	scope := options.scope
	scope.mu.RLock()
	if !scope.active || scope.hostID != hostID || scope.root != root {
		scope.mu.RUnlock()
		return nil, errors.New("scoped maintenance options expired or crossed their host/root")
	}
	return scope.mu.RUnlock, nil
}

func (m *Maintenance) Execute(ctx context.Context, planPath, operation string, ids []string) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return Result{}, errors.New("maintenance callback has ended")
	}
	plan, err := LoadPlan(planPath)
	if err != nil {
		return Result{}, err
	}
	if plan.HostID != m.hostID || plan.DeploymentRoot != m.root {
		return Result{}, errors.New("maintenance lifecycle cannot cross its host or deployment root")
	}
	return Execute(ctx, planPath, operation, ids, Options{MaintenanceLockHeld: true, MaintenanceLockFD: int(m.lock.Fd())})
}

// CheckUnit processes must belong to the current/candidate releases of this
// unit. A valid PID record from another unit is not permission to stop it.
func (m *Maintenance) CheckUnit(ctx context.Context, planPath string, allowedReleaseRoots []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return errors.New("maintenance callback has ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := LoadPlan(planPath)
	if err != nil {
		return err
	}
	if plan.HostID != m.hostID || plan.DeploymentRoot != m.root {
		return errors.New("maintenance ownership check cannot cross its host or deployment root")
	}
	state, err := openRuntime(plan)
	if err != nil {
		return err
	}
	defer state.close()
	for _, component := range plan.Components {
		record, alive, err := state.record(component)
		if err != nil {
			return err
		}
		if alive && !slices.Contains(allowedReleaseRoots, record.ReleaseRoot) {
			return errors.New("running component belongs to a different deployment unit")
		}
	}
	return nil
}

func (s *runtimeState) ensureHostIdentity() error {
	var identity struct {
		HostID string `json:"host_id"`
	}
	if err := privateJSON(filepath.Join(s.run.Name(), "host.json"), &identity); os.IsNotExist(err) {
		identity.HostID = s.plan.HostID
		return writeState(s.run, "host.json", identity)
	} else if err != nil {
		return err
	}
	if identity.HostID != s.plan.HostID {
		return errors.New("runtime deployment root belongs to a different host identity")
	}
	return nil
}
