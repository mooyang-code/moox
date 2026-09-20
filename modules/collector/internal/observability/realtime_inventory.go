// Package observability reconciles Collector's expected realtime datasets.
package observability

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	"github.com/mooyang-code/moox/packages/report"
)

const (
	inventoryRefreshInterval = 5 * time.Minute
	inventoryTaskLimit       = store.MaxEnabledTasks
)

type taskSource interface {
	ListEnabledAll(context.Context, int) ([]domain.CollectionTask, error)
}

type datasetRegistry interface {
	ReplaceExpected([]report.DatasetExpectation) error
	ObserveInventoryRefreshError()
}

// RealtimeInventory owns the atomic expected Dataset+Frequency snapshot.
type RealtimeInventory struct {
	source   taskSource
	registry datasetRegistry

	mu              sync.Mutex
	dirty           bool
	lastRefresh     time.Time
	resampleEnabled bool
}

func NewRealtimeInventory(source taskSource, registry datasetRegistry) *RealtimeInventory {
	return &RealtimeInventory{source: source, registry: registry, dirty: true, resampleEnabled: true}
}

// SetResampleEnabled keeps the published expectation aligned with the runtime
// feature gate. Existing callers default to enabled for backwards compatibility.
func (i *RealtimeInventory) SetResampleEnabled(enabled bool) {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.resampleEnabled = enabled
	i.dirty = true
	i.mu.Unlock()
}

// MarkDirty requests reconciliation on the next metrics tick.
func (i *RealtimeInventory) MarkDirty() {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.dirty = true
	i.mu.Unlock()
}

// Due reports whether the inventory needs its five-minute fallback refresh.
func (i *RealtimeInventory) Due(now time.Time) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.dirty || i.lastRefresh.IsZero() || !now.Before(i.lastRefresh.Add(inventoryRefreshInterval))
}

// Refresh builds and validates a complete temporary snapshot before replacing
// the currently published inventory.
func (i *RealtimeInventory) Refresh(ctx context.Context) error {
	if i == nil || i.source == nil || i.registry == nil {
		return fmt.Errorf("collector realtime inventory dependencies are required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	tasks, err := i.source.ListEnabledAll(ctx, inventoryTaskLimit)
	if err != nil {
		i.registry.ObserveInventoryRefreshError()
		return fmt.Errorf("list enabled collector tasks: %w", err)
	}
	expected := make(map[report.DatasetKey]time.Duration)
	for _, task := range tasks {
		if !task.Enabled {
			continue
		}
		params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
		if err != nil {
			i.registry.ObserveInventoryRefreshError()
			return fmt.Errorf("parse collector task %q: %w", task.TaskID, err)
		}
		if err := params.Validate(); err != nil {
			i.registry.ObserveInventoryRefreshError()
			return fmt.Errorf("validate collector task %q: %w", task.TaskID, err)
		}
		if params.Collector.DataType == "kline_resample" {
			if !i.resampleEnabled {
				continue
			}
			if task.PrepareState != domain.PrepareStateReady {
				continue
			}
			frequency, err := report.NormalizeDatasetFrequency(params.TargetFrequency)
			if err != nil {
				i.registry.ObserveInventoryRefreshError()
				return fmt.Errorf("normalize resample task %q frequency %q: %w", task.TaskID, params.TargetFrequency, err)
			}
			interval, err := report.ParseDatasetFrequency(frequency)
			if err != nil {
				i.registry.ObserveInventoryRefreshError()
				return fmt.Errorf("parse resample task %q frequency %q: %w", task.TaskID, frequency, err)
			}
			key := report.DatasetKey{SpaceID: task.SpaceID, DatasetID: params.TargetDatasetID, Freq: frequency}
			if previous, ok := expected[key]; !ok || interval < previous {
				expected[key] = interval
			}
			continue
		}
		if params.Collector.DataType != "kline" {
			continue
		}
		interval, err := domain.ParseScheduleInterval(params.Schedule.Interval)
		if err != nil {
			i.registry.ObserveInventoryRefreshError()
			return fmt.Errorf("parse collector task %q schedule: %w", task.TaskID, err)
		}
		for _, freq := range params.Collector.Intervals {
			if _, err := report.ParseDatasetFrequency(freq); err != nil {
				i.registry.ObserveInventoryRefreshError()
				return fmt.Errorf("parse collector task %q frequency %q: %w", task.TaskID, freq, err)
			}
			canonicalFreq, err := report.NormalizeDatasetFrequency(freq)
			if err != nil {
				i.registry.ObserveInventoryRefreshError()
				return fmt.Errorf("normalize collector task %q frequency %q: %w", task.TaskID, freq, err)
			}
			key := report.DatasetKey{SpaceID: task.SpaceID, DatasetID: params.Target.DatasetID, Freq: canonicalFreq}
			if previous, ok := expected[key]; !ok || interval < previous {
				expected[key] = interval
			}
		}
	}

	items := make([]report.DatasetExpectation, 0, len(expected))
	for key, interval := range expected {
		items = append(items, report.DatasetExpectation{Key: key, Interval: interval})
	}
	sort.Slice(items, func(a, b int) bool {
		left, right := items[a].Key, items[b].Key
		if left.SpaceID != right.SpaceID {
			return left.SpaceID < right.SpaceID
		}
		if left.DatasetID != right.DatasetID {
			return left.DatasetID < right.DatasetID
		}
		return left.Freq < right.Freq
	})
	if err := i.registry.ReplaceExpected(items); err != nil {
		return fmt.Errorf("replace collector expected datasets: %w", err)
	}
	i.lastRefresh = time.Now().UTC()
	i.dirty = false
	return nil
}
