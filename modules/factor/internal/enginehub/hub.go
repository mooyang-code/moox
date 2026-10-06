// Package enginehub is moox-factor-mgr's side of the compute engine contract:
// it holds the single-engine lease, keeps the latest heartbeat, assembles the
// hashed catalog snapshot and leases recalc jobs to the engine.
package enginehub

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
)

// ErrLeaseConflict means another engine holds the live engine lease.
var ErrLeaseConflict = errors.New("factor engine lease is held by another engine")

// Store is the catalog and job storage the hub reads and leases from.
type Store interface {
	ListSets(ctx context.Context) ([]domain.FactorSet, error)
	GetSet(ctx context.Context, setID string) (domain.FactorSet, error)
	ListMembers(ctx context.Context, setID, status string) ([]domain.SetMember, error)
	PullRecalcJob(ctx context.Context, engineID string, now time.Time, ttl time.Duration, skipSets map[string]bool) (store.RecalcJob, bool, error)
	ReportRecalcProgress(ctx context.Context, jobID, leaseToken string, progress int64, status, errText string, now time.Time, ttl time.Duration) (store.RecalcJob, error)
}

const (
	DefaultEngineLeaseTTL = 45 * time.Second
	DefaultJobLeaseTTL    = 15 * time.Minute
)

type Option func(*Hub)

// WithEngineLeaseTTL sets how long a heartbeat keeps an engine online and
// holding the lease.
func WithEngineLeaseTTL(ttl time.Duration) Option { return func(h *Hub) { h.engineTTL = ttl } }

// WithJobLeaseTTL sets how long a pulled recalc job stays leased without a
// progress report; it must exceed the duration of one recalc chunk.
func WithJobLeaseTTL(ttl time.Duration) Option { return func(h *Hub) { h.jobTTL = ttl } }

func WithClock(now func() time.Time) Option { return func(h *Hub) { h.now = now } }

type Hub struct {
	store     Store
	engineTTL time.Duration
	jobTTL    time.Duration
	now       func() time.Time

	mu     sync.Mutex
	ready  map[string]bool
	engine *engineState
}

type engineState struct {
	identity domain.EngineIdentity
	status   domain.EngineStatus
	lastSeen time.Time
}

func New(db Store, options ...Option) *Hub {
	h := &Hub{store: db, engineTTL: DefaultEngineLeaseTTL, jobTTL: DefaultJobLeaseTTL, now: time.Now, ready: make(map[string]bool)}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return h
}

// Heartbeat records the engine's status and renews its lease. While another
// engine's lease is live the heartbeat is rejected with ErrLeaseConflict.
func (h *Hub) Heartbeat(_ context.Context, id domain.EngineIdentity, status domain.EngineStatus) (time.Duration, error) {
	if id.EngineID == "" {
		return 0, errors.New("engine_id is required")
	}
	now := h.now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	// A live lease is held by one process: another engine id, or the same id
	// from another boot (a copied config), is rejected until the lease lapses.
	if h.engine != nil && now.Before(h.engine.lastSeen.Add(h.engineTTL)) &&
		(h.engine.identity.EngineID != id.EngineID || h.engine.identity.BootID != id.BootID) {
		return 0, fmt.Errorf("%w: %s (boot %s)", ErrLeaseConflict, h.engine.identity.EngineID, h.engine.identity.BootID)
	}
	h.engine = &engineState{identity: id, status: status, lastSeen: now}
	return h.engineTTL, nil
}

// SetResultReady records whether a set's result dataset accepted its columns
// in the latest reconciliation; the engine only computes ready sets.
func (h *Hub) SetResultReady(setID string, ready bool) {
	h.mu.Lock()
	h.ready[setID] = ready
	h.mu.Unlock()
}

// Engine returns the manager's view of the lease holder and its last status,
// with run lags recomputed against the manager clock. ok is false before the
// first heartbeat.
func (h *Hub) Engine(ctx context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error) {
	now := h.now().UTC()
	h.mu.Lock()
	state := h.engine
	h.mu.Unlock()
	if state == nil {
		return domain.EngineInfo{}, domain.EngineStatus{}, false, nil
	}
	hash, _, _, err := h.Snapshot(ctx, "")
	if err != nil {
		return domain.EngineInfo{}, domain.EngineStatus{}, false, err
	}
	status := state.status
	status.RecentRuns = withLag(status.RecentRuns, now)
	info := domain.EngineInfo{
		EngineIdentity: state.identity, Online: now.Before(state.lastSeen.Add(h.engineTTL)),
		LastHeartbeatAt: state.lastSeen, CatalogHash: status.CatalogHash, CatalogSyncedAt: status.CatalogSyncedAt,
		CatalogInSync: status.CatalogHash != "" && status.CatalogHash == hash,
	}
	return info, status, true, nil
}

// LatestRun returns the engine's most recent live period of one set.
func (h *Hub) LatestRun(setID string) domain.SetRunSummary {
	now := h.now().UTC()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.engine != nil {
		for _, run := range withLag(h.engine.status.RecentRuns, now) {
			if run.SetID == setID {
				return run
			}
		}
	}
	return domain.SetRunSummary{SetID: setID}
}

func withLag(runs []domain.SetRunSummary, now time.Time) []domain.SetRunSummary {
	out := make([]domain.SetRunSummary, len(runs))
	for i, run := range runs {
		run.LagSeconds = max(0, now.Unix()-run.LastPeriodTime)
		out[i] = run
	}
	return out
}

func (h *Hub) holdsLease(engineID string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.engine != nil && h.engine.identity.EngineID == engineID && now.Before(h.engine.lastSeen.Add(h.engineTTL))
}

func (h *Hub) isReady(setID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ready[setID]
}
