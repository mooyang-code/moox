package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/artifacts"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"github.com/mooyang-code/moox/packages/events"
)

// CatalogCache is the engine's copy of the manager's enabled sets. It is
// replaced atomically on every changed sync, persisted so a restart works
// while the manager is unreachable, and keeps serving the last snapshot when
// syncs fail (computing with a stale snapshot is safe, see the split design).
type CatalogCache struct {
	artifacts artifacts.Artifacts
	stateFile string
	registry  *events.Registry
	now       func() time.Time

	mu             sync.RWMutex
	loaded         bool
	hash           string
	sets           []domain.EngineSet
	syncedAt       time.Time
	syncErrorSince time.Time
	syncError      string
}

type catalogState struct {
	Hash     string             `json:"hash"`
	SyncedAt time.Time          `json:"synced_at"`
	Sets     []domain.EngineSet `json:"sets"`
}

func NewCatalogCache(factorsDir, stateFile string) (*CatalogCache, error) {
	registry, err := events.DefaultRegistry()
	if err != nil {
		return nil, err
	}
	return &CatalogCache{artifacts: artifacts.Artifacts{FactorsDir: factorsDir}, stateFile: stateFile, registry: registry, now: time.Now}, nil
}

// Load restores the snapshot saved by the last successful sync. It reports
// false when no snapshot was saved yet.
func (c *CatalogCache) Load() (bool, error) {
	raw, err := os.ReadFile(c.stateFile)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read engine catalog: %w", err)
	}
	var state catalogState
	if err := json.Unmarshal(raw, &state); err != nil {
		return false, fmt.Errorf("decode engine catalog %s: %w", c.stateFile, err)
	}
	if err := c.materialize(state.Sets); err != nil {
		return false, err
	}
	c.mu.Lock()
	c.loaded, c.hash, c.sets, c.syncedAt = true, state.Hash, sortSets(state.Sets), state.SyncedAt
	c.mu.Unlock()
	return true, nil
}

// Apply installs a sync result. Sources are materialized and the snapshot is
// persisted before it becomes visible, so a failure leaves the previous
// snapshot in place. changed reports whether the set of factor sets changed.
func (c *CatalogCache) Apply(snapshot CatalogSnapshot) (bool, error) {
	now := c.now().UTC()
	if snapshot.NotModified {
		c.mu.Lock()
		c.syncedAt, c.syncErrorSince, c.syncError = now, time.Time{}, ""
		c.mu.Unlock()
		return false, nil
	}
	sets := sortSets(snapshot.Sets)
	if err := c.materialize(sets); err != nil {
		return false, err
	}
	if err := c.persist(catalogState{Hash: snapshot.Hash, SyncedAt: now, Sets: sets}); err != nil {
		return false, err
	}
	c.mu.Lock()
	changed := !sameSetIDs(c.sets, sets)
	c.loaded, c.hash, c.sets = true, snapshot.Hash, sets
	c.syncedAt, c.syncErrorSince, c.syncError = now, time.Time{}, ""
	c.mu.Unlock()
	return changed, nil
}

// MarkSyncFailed records a failed sync; the current snapshot stays in use.
func (c *CatalogCache) MarkSyncFailed(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.syncErrorSince.IsZero() {
		c.syncErrorSince = c.now().UTC()
	}
	c.syncError = err.Error()
}

func (c *CatalogCache) Loaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

// CatalogStatus is what the engine reports about its snapshot.
type CatalogStatus struct {
	Loaded         bool
	Hash           string
	SyncedAt       time.Time
	SyncErrorSince time.Time
	SyncError      string
	Sets           int
}

func (c *CatalogCache) Status() CatalogStatus {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CatalogStatus{
		Loaded: c.loaded, Hash: c.hash, SyncedAt: c.syncedAt,
		SyncErrorSince: c.syncErrorSince, SyncError: c.syncError, Sets: len(c.sets),
	}
}

// SetIDs lists the enabled sets of the snapshot.
func (c *CatalogCache) SetIDs() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ids := make([]string, 0, len(c.sets))
	for _, set := range c.sets {
		ids = append(ids, set.Set.SetID)
	}
	return ids
}

// EnabledSetByDataset implements trigger.SetLocator from the snapshot. A set
// whose result dataset is not ready, or a missing snapshot, is a transient
// infrastructure condition so the event is retried rather than dropped.
func (c *CatalogCache) EnabledSetByDataset(_ context.Context, spaceID, datasetID, freq string) (domain.FactorSet, []domain.FactorDef, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.loaded {
		return domain.FactorSet{}, nil, false, fmt.Errorf("%w: factor catalog has not been loaded", storageio.ErrInfra)
	}
	for _, set := range c.sets {
		if set.Set.SpaceID != spaceID || set.Set.SourceDatasetID != datasetID || set.Set.Freq != freq {
			continue
		}
		if !set.ResultReady {
			return domain.FactorSet{}, nil, false, fmt.Errorf("%w: result dataset of factor set %s is not ready", storageio.ErrInfra, set.Set.SetID)
		}
		return set.Set, append([]domain.FactorDef(nil), set.Factors...), true, nil
	}
	return domain.FactorSet{}, nil, false, nil
}

// FilterSubjects renders the collector period subjects of the snapshot's sets.
func (c *CatalogCache) FilterSubjects(context.Context) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	filters := make([]string, 0, len(c.sets))
	for _, set := range c.sets {
		subject, err := c.registry.RenderSubject(events.CollectorPeriodCompleted, set.Set.SpaceID, set.Set.SourceDatasetID)
		if err != nil {
			return nil, fmt.Errorf("render collector period subject for set %s: %w", set.Set.SetID, err)
		}
		filters = append(filters, subject)
	}
	return trigger.NormalizeFilters(filters), nil
}

func (c *CatalogCache) materialize(sets []domain.EngineSet) error {
	for _, set := range sets {
		for _, factor := range set.Factors {
			if _, err := c.artifacts.Materialize(factor); err != nil {
				return fmt.Errorf("materialize factor %s of set %s: %w", factor.FactorID, set.Set.SetID, err)
			}
		}
	}
	return nil
}

func (c *CatalogCache) persist(state catalogState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode engine catalog: %w", err)
	}
	dir := filepath.Dir(c.stateFile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create engine catalog directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".catalog-*.json")
	if err != nil {
		return fmt.Errorf("create engine catalog temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write engine catalog: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync engine catalog: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close engine catalog: %w", err)
	}
	if err := os.Rename(tmp.Name(), c.stateFile); err != nil {
		return fmt.Errorf("install engine catalog: %w", err)
	}
	return nil
}

func sortSets(sets []domain.EngineSet) []domain.EngineSet {
	out := append([]domain.EngineSet(nil), sets...)
	sort.Slice(out, func(i, j int) bool { return out[i].Set.SetID < out[j].Set.SetID })
	return out
}

func sameSetIDs(left, right []domain.EngineSet) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Set.SetID != right[i].Set.SetID || left[i].Set.SourceDatasetID != right[i].Set.SourceDatasetID {
			return false
		}
	}
	return true
}

var _ trigger.SetLocator = (*CatalogCache)(nil)
