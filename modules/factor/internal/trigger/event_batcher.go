package trigger

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// Task is an event-batched scheduler request.
type Task struct {
	SpaceID         string
	SourceDataset   string
	TargetDataset   string
	SubjectID       string
	Freq            string
	StartTime       time.Time
	EndTime         time.Time
	FirstReceivedAt time.Time
	LastReceivedAt  time.Time
	TriggerType     string
	FactorIDs       []string
}

// EventBatcher groups Storage row-update messages into fixed-window per-symbol task requests.
type EventBatcher struct {
	mu       sync.Mutex
	window   time.Duration
	bindings []domain.FactorBinding
	buckets  map[bucketKey]*bucket
}

type bucketKey struct {
	spaceID       string
	sourceDataset string
	targetDataset string
	subjectID     string
	freq          string
}

type bucket struct {
	task     Task
	deadline time.Time
	factors  map[string]struct{}
}

// NewEventBatcher creates an event batcher with an initial binding snapshot.
func NewEventBatcher(window time.Duration, bindings []domain.FactorBinding) *EventBatcher {
	return &EventBatcher{window: window, bindings: append([]domain.FactorBinding(nil), bindings...), buckets: map[bucketKey]*bucket{}}
}

// SetBindings replaces the enabled binding snapshot.
func (d *EventBatcher) SetBindings(bindings []domain.FactorBinding) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bindings = append([]domain.FactorBinding(nil), bindings...)
}

// Add adds one DatasetRowsUpserted event into fixed-window buckets.
func (d *EventBatcher) Add(event *storagepb.DatasetRowsUpserted, now time.Time) {
	if d == nil || event == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, row := range event.GetRows() {
		if row == nil || row.GetKey() == nil {
			continue
		}
		rowKey := row.GetKey().GetTimeSeries()
		if rowKey == nil {
			continue
		}
		key := row.GetKey()
		dataTime, err := time.Parse(time.RFC3339Nano, rowKey.GetDataTime())
		if err != nil {
			continue
		}
		dataTime = dataTime.UTC()
		endTime := dataTime.Add(time.Nanosecond)
		if dataTime.IsZero() || dataTime.Year() < 1 || dataTime.Year() > 9999 || endTime.Year() > 9999 {
			continue
		}
		matches := d.matchBindings(key.GetSpaceId(), key.GetDatasetId(), rowKey.GetSubjectId(), rowKey.GetFreq())
		if len(matches) == 0 {
			continue
		}
		for _, binding := range matches {
			targetDataset := binding.TargetDataset
			if targetDataset == "" {
				targetDataset = registry.ResultDataset(key.GetDatasetId())
			}
			bkey := bucketKey{
				spaceID:       key.GetSpaceId(),
				sourceDataset: key.GetDatasetId(),
				targetDataset: targetDataset,
				subjectID:     rowKey.GetSubjectId(),
				freq:          rowKey.GetFreq(),
			}
			b := d.buckets[bkey]
			if b == nil {
				b = &bucket{
					task: Task{
						SpaceID:         key.GetSpaceId(),
						SourceDataset:   key.GetDatasetId(),
						TargetDataset:   targetDataset,
						SubjectID:       rowKey.GetSubjectId(),
						Freq:            rowKey.GetFreq(),
						StartTime:       dataTime,
						EndTime:         endTime,
						FirstReceivedAt: now.UTC(),
						LastReceivedAt:  now.UTC(),
					},
					deadline: now.Add(d.window),
					factors:  map[string]struct{}{},
				}
				d.buckets[bkey] = b
			}
			if dataTime.Before(b.task.StartTime) {
				b.task.StartTime = dataTime
			}
			if endTime.After(b.task.EndTime) {
				b.task.EndTime = endTime
			}
			if now.Before(b.task.FirstReceivedAt) {
				b.task.FirstReceivedAt = now.UTC()
			}
			if now.After(b.task.LastReceivedAt) {
				b.task.LastReceivedAt = now.UTC()
			}
			b.factors[binding.FactorID] = struct{}{}
		}
	}
}

// Flush removes and returns buckets whose fixed window has elapsed.
func (d *EventBatcher) Flush(now time.Time) []Task {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]bucketKey, 0, len(d.buckets))
	for key, item := range d.buckets {
		if !now.Before(item.deadline) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		return strings.Join([]string{a.spaceID, a.sourceDataset, a.targetDataset, a.subjectID, a.freq}, "\x00") <
			strings.Join([]string{b.spaceID, b.sourceDataset, b.targetDataset, b.subjectID, b.freq}, "\x00")
	})
	tasks := make([]Task, 0, len(keys))
	for _, key := range keys {
		item := d.buckets[key]
		item.task.FactorIDs = orderedFactorIDs(key, item.factors, d.bindings)
		if len(item.task.FactorIDs) > 0 {
			tasks = append(tasks, item.task)
		}
		delete(d.buckets, key)
	}
	return tasks
}

func (d *EventBatcher) matchBindings(spaceID, datasetID, subjectID, freq string) []domain.FactorBinding {
	out := []domain.FactorBinding{}
	for _, binding := range d.bindings {
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		if binding.SpaceID != spaceID || binding.SourceDataset != datasetID || binding.Freq != freq {
			continue
		}
		if !domain.BindingAllowsSubject(binding, subjectID) {
			continue
		}
		out = append(out, binding)
	}
	return out
}

func orderedFactorIDs(key bucketKey, set map[string]struct{}, bindings []domain.FactorBinding) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, binding := range bindings {
		targetDataset := binding.TargetDataset
		if targetDataset == "" {
			targetDataset = registry.ResultDataset(binding.SourceDataset)
		}
		if binding.Status != domain.BindingStatusEnabled ||
			binding.SpaceID != key.spaceID ||
			binding.SourceDataset != key.sourceDataset ||
			targetDataset != key.targetDataset ||
			binding.Freq != key.freq ||
			!domain.BindingAllowsSubject(binding, key.subjectID) {
			continue
		}
		if _, ok := set[binding.FactorID]; !ok {
			continue
		}
		if _, ok := seen[binding.FactorID]; ok {
			continue
		}
		out = append(out, binding.FactorID)
		seen[binding.FactorID] = struct{}{}
	}
	return out
}
