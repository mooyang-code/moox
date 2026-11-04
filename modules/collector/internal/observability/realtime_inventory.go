// Package observability reconciles Collector's expected realtime datasets.
package observability

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/resample"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"github.com/mooyang-code/moox/packages/report"
	"trpc.group/trpc-go/trpc-go/log"
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
	ObserveRun(report.DatasetObservation) error
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
	// registered 是本进程已登记过的期望数据集：新进入清单的数据集登记时记一次“空”运行（见 Refresh）。
	registered map[report.DatasetKey]struct{}
	now        func() time.Time
}

func NewRealtimeInventory(source taskSource, registry datasetRegistry) *RealtimeInventory {
	return &RealtimeInventory{source: source, registry: registry, dirty: true, resampleEnabled: true, registered: map[report.DatasetKey]struct{}{}, now: time.Now}
}

// SetResampleEnabled keeps the published expectation aligned with the runtime
// feature gate, which defaults to enabled.
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
		params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
		if err != nil {
			i.registry.ObserveInventoryRefreshError()
			return fmt.Errorf("parse collector task %q: %w", task.TaskID, err)
		}
		var validateErr error
		if params.Collector.DataType == "kline_resample" {
			validateErr = resample.ValidateTaskParams(task.SpaceID, params)
		} else {
			// Direct collection tasks derive Provider/market routing from their
			// selected Tags. The task definition itself must therefore be valid
			// without task-level exchange/market fields.
			validateErr = params.ValidateTaskDefinition()
		}
		if validateErr != nil {
			i.registry.ObserveInventoryRefreshError()
			return fmt.Errorf("validate collector task %q: %w", task.TaskID, validateErr)
		}
		if params.Collector.DataType == "kline_resample" {
			if !i.resampleEnabled {
				continue
			}
			if task.PrepareState != domain.PrepareStateReady {
				continue
			}
			parsed, err := frequencypkg.Parse(params.TargetFrequency)
			if err != nil {
				i.registry.ObserveInventoryRefreshError()
				return fmt.Errorf("parse resample task %q frequency %q: %w", task.TaskID, params.TargetFrequency, err)
			}
			interval := parsed.NominalDuration()
			key := report.DatasetKey{SpaceID: task.SpaceID, DatasetID: params.TargetDatasetID, Freq: string(parsed)}
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
			canonicalFreq, err := frequencypkg.Normalize(freq)
			if err != nil {
				i.registry.ObserveInventoryRefreshError()
				return fmt.Errorf("parse collector task %q frequency %q: %w", task.TaskID, freq, err)
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
	now := i.now().UTC()
	i.graceNewDatasets(items, now)
	i.lastRefresh = now
	i.dirty = false
	return nil
}

// graceNewDatasets 给本进程新登记的数据集记一次“空”运行（只刷新最近运行与最近成功时间，不推进水位）。
//
// Collector 的定时采集路径没有逐次运行上报，Monitor 在 Storage 第一次提交之前只看到这条清单：不记的话会一直判“尚未上报”，
// 收盘后或周末启用 A 股任务时整夜告警，而交易所休市豁免要有运行时间才能接管。登记时刻作为起点，开盘后一直没有写入时
// 照常从等待中的 K 线起算判停滞。进程重启后清单重新登记，这条记录随之重写；移出清单的数据集重新进入时再记一次。
func (i *RealtimeInventory) graceNewDatasets(items []report.DatasetExpectation, now time.Time) {
	next := make(map[report.DatasetKey]struct{}, len(items))
	for _, item := range items {
		next[item.Key] = struct{}{}
		if _, known := i.registered[item.Key]; known {
			continue
		}
		if err := i.registry.ObserveRun(report.DatasetObservation{Key: item.Key, Result: "empty", FinishedAt: now}); err != nil {
			log.Warnf("记录数据集 %s/%s/%s 的登记运行失败：%v", item.Key.SpaceID, item.Key.DatasetID, item.Key.Freq, err)
		}
	}
	i.registered = next
}
