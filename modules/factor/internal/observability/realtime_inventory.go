package observability

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/packages/report"
)

const inventoryRefreshInterval = 5 * time.Minute

type bindingSource interface {
	ListExecutable(context.Context) ([]domain.FactorBinding, error)
}

type datasetRegistry interface {
	ReplaceExpected([]report.DatasetExpectation) error
	ObserveInventoryRefreshError()
}

// RealtimeInventory owns Factor's expected output Dataset+Frequency snapshot.
type RealtimeInventory struct {
	source   bindingSource
	registry datasetRegistry

	mu          sync.Mutex
	dirty       bool
	lastRefresh time.Time
}

func NewRealtimeInventory(source bindingSource, registry datasetRegistry) *RealtimeInventory {
	return &RealtimeInventory{source: source, registry: registry, dirty: true}
}

func (i *RealtimeInventory) MarkDirty() {
	if i == nil {
		return
	}
	i.mu.Lock()
	i.dirty = true
	i.mu.Unlock()
}

func (i *RealtimeInventory) Due(now time.Time) bool {
	if i == nil {
		return false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.dirty || i.lastRefresh.IsZero() || !now.Before(i.lastRefresh.Add(inventoryRefreshInterval))
}

// Refresh publishes only bindings returned by ListExecutable, whose SQL join
// requires both the binding and its factor to be enabled.
func (i *RealtimeInventory) Refresh(ctx context.Context) error {
	if i == nil || i.source == nil || i.registry == nil {
		return fmt.Errorf("factor realtime inventory dependencies are required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()

	bindings, err := i.source.ListExecutable(ctx)
	if err != nil {
		i.registry.ObserveInventoryRefreshError()
		return fmt.Errorf("list executable factor bindings: %w", err)
	}
	expected := make(map[report.DatasetKey]time.Duration)
	for _, binding := range bindings {
		freq := strings.TrimSpace(binding.Freq)
		interval, err := parseFrequency(freq)
		if err != nil || interval <= 0 {
			i.registry.ObserveInventoryRefreshError()
			return fmt.Errorf("factor binding %q has invalid positive freq %q", binding.BindingID, binding.Freq)
		}
		key := report.DatasetKey{
			SpaceID:   strings.TrimSpace(binding.SpaceID),
			DatasetID: strings.TrimSpace(binding.TargetDataset),
			Freq:      freq,
		}
		expected[key] = interval
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
		return fmt.Errorf("replace factor expected datasets: %w", err)
	}
	i.lastRefresh = time.Now().UTC()
	i.dirty = false
	return nil
}

func parseFrequency(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.ParseUint(strings.TrimSuffix(raw, "d"), 10, 64)
		maxDays := uint64((time.Duration(1<<63 - 1)) / (24 * time.Hour))
		if err != nil || days == 0 || days > maxDays {
			return 0, fmt.Errorf("frequency must be a positive duration")
		}
		interval := time.Duration(days) * 24 * time.Hour
		return interval, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("frequency must be a positive duration")
	}
	return interval, nil
}
