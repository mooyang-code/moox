package trigger

import (
	"context"
	"encoding/json"
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
	BarTime         time.Time
	FirstReceivedAt time.Time
	LastReceivedAt  time.Time
	TriggerType     string
	FactorVersion   string
	TargetRunID     string
	FactorIDs       []string
	// PendingEventIDs are the durable inbox records covered by this task. They
	// are committed only after the scheduler accepts the task.
	PendingEventIDs []string
}

// EventBatcher groups Storage row-update messages into fixed-window per-symbol task requests.
type EventBatcher struct {
	mu       sync.Mutex
	window   time.Duration
	bindings []domain.FactorBinding
	buckets  map[bucketKey]*bucket
	inbox    PendingEventStore
}

type bucketKey struct {
	spaceID       string
	sourceDataset string
	targetDataset string
	subjectID     string
	freq          string
}

type bucket struct {
	task       Task
	deadline   time.Time
	factors    map[string]struct{}
	messageIDs map[string]struct{}
}

// NewEventBatcher creates an event batcher with an initial binding snapshot.
func NewEventBatcher(window time.Duration, bindings []domain.FactorBinding) *EventBatcher {
	return &EventBatcher{window: window, bindings: append([]domain.FactorBinding(nil), bindings...), buckets: map[bucketKey]*bucket{}}
}

func NewDurableEventBatcher(window time.Duration, bindings []domain.FactorBinding, inbox PendingEventStore) *EventBatcher {
	d := NewEventBatcher(window, bindings)
	d.inbox = inbox
	return d
}

// SetBindings replaces the enabled binding snapshot.
func (d *EventBatcher) SetBindings(bindings []domain.FactorBinding) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.bindings = append([]domain.FactorBinding(nil), bindings...)
}

// Ingest adds one DatasetRowsUpserted event into fixed-window buckets.
func (d *EventBatcher) Ingest(event *storagepb.DatasetRowsUpserted, now time.Time) {
	_ = d.ingestMemory("", event, now)
}

func (d *EventBatcher) ingestMemory(messageID string, event *storagepb.DatasetRowsUpserted, now time.Time) bool {
	return d.ingestMemoryWithDeadline(messageID, event, now, now)
}

// ingestMemoryWithDeadline keeps event reception metadata separate from the
// processing boundary used to close a bucket. Replay uses this to flush a
// bounded range even when the source event was received much later.
func (d *EventBatcher) ingestMemoryWithDeadline(messageID string, event *storagepb.DatasetRowsUpserted, receivedAt, processingAt time.Time) bool {
	if d == nil || event == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	matched := false
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
		matches := d.matchBindings(key.GetSpaceId(), key.GetDatasetId(), rowKey.GetSubjectId(), rowKey.GetFreq())
		if len(matches) == 0 {
			continue
		}
		matched = true
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
						BarTime:         dataTime.UTC(),
						FirstReceivedAt: receivedAt.UTC(),
						LastReceivedAt:  receivedAt.UTC(),
					},
					deadline: processingAt.Add(d.window),
					factors:  map[string]struct{}{}, messageIDs: map[string]struct{}{},
				}
				d.buckets[bkey] = b
			}
			if dataTime.After(b.task.BarTime) {
				b.task.BarTime = dataTime.UTC()
			}
			if receivedAt.Before(b.task.FirstReceivedAt) {
				b.task.FirstReceivedAt = receivedAt.UTC()
			}
			if receivedAt.After(b.task.LastReceivedAt) {
				b.task.LastReceivedAt = receivedAt.UTC()
			}
			b.factors[binding.FactorID] = struct{}{}
			if messageID != "" {
				b.messageIDs[messageID] = struct{}{}
			}
		}
	}
	return matched
}

// Flush 是纯内存兼容入口。需要持久化的调用方必须使用 FlushPending，
// 再调用 CommitPending，确保 inbox 错误可以被观察到。
func (d *EventBatcher) Flush(now time.Time) []Task {
	if d == nil || d.inbox != nil {
		return nil
	}
	tasks, err := d.FlushPending(context.Background(), now)
	if err != nil {
		return nil
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
		if !subjectAllowed(binding, subjectID) {
			continue
		}
		out = append(out, binding)
	}
	return out
}

func subjectAllowed(binding domain.FactorBinding, subjectID string) bool {
	if binding.SubjectMode == "" || binding.SubjectMode == domain.SubjectModeAll {
		return true
	}
	if binding.SubjectMode != domain.SubjectModeInclude {
		return false
	}
	var subjects []string
	if err := json.Unmarshal([]byte(binding.SubjectsJSON), &subjects); err != nil {
		return false
	}
	for _, subject := range subjects {
		if subject == subjectID {
			return true
		}
	}
	return false
}

func orderedFactorIDs(set map[string]struct{}, bindings []domain.FactorBinding) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for _, binding := range bindings {
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
