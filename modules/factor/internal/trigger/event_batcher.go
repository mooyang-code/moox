package trigger

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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
	MinDataTime     time.Time
	MaxDataTime     time.Time
	LateDataPolicy  string
	LateData        bool
	TriggerType     string
	FactorVersion   string
	TargetRunID     string
	FactorIDs       []string
	// PendingEventIDs are the durable inbox records covered by this task. They
	// are committed only after the scheduler accepts the task.
	PendingEventIDs []string
}

const LateDataPolicyRecompute = "recompute"

const (
	closedBucketRetention = 24 * time.Hour
	maxClosedBuckets      = 4096
)

// EventBatcher groups Storage row-update messages into fixed-window per-symbol task requests.
type EventBatcher struct {
	mu       sync.Mutex
	window   time.Duration
	bindings []domain.FactorBinding
	buckets  map[bucketKey]*bucket
	closed   map[bucketKey]closedBucket
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

type closedBucket struct {
	dataTime time.Time
	closedAt time.Time
}

// NewEventBatcher creates an event batcher with an initial binding snapshot.
func NewEventBatcher(window time.Duration, bindings []domain.FactorBinding) *EventBatcher {
	return &EventBatcher{window: window, bindings: append([]domain.FactorBinding(nil), bindings...), buckets: map[bucketKey]*bucket{}, closed: map[bucketKey]closedBucket{}}
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

// Ingest adds one Storage rows_upserted event into fixed-window buckets.
func (d *EventBatcher) Ingest(event *storagepb.RowsUpserted, now time.Time) {
	_ = d.ingestMemory("", event, now)
}

// IngestMessage persists the event before exposing it to the in-memory window.
func (d *EventBatcher) IngestMessage(ctx context.Context, messageID string, event *storagepb.RowsUpserted, now time.Time) error {
	if d == nil || d.inbox == nil {
		return errors.New("factor event inbox is not configured")
	}
	if messageID == "" {
		return errors.New("factor event message_id is required")
	}
	claimed, err := d.inbox.ClaimPendingEvent(ctx, messageID, event, now)
	if err != nil {
		return err
	}
	if !claimed {
		return nil
	}
	if !d.ingestMemory(messageID, event, now) {
		return d.inbox.CommitPendingEvents(ctx, []string{messageID})
	}
	return nil
}

func (d *EventBatcher) ingestMemory(messageID string, event *storagepb.RowsUpserted, now time.Time) bool {
	return d.ingestMemoryWithDeadline(messageID, event, now, now)
}

// ingestMemoryWithDeadline keeps event reception metadata separate from the
// processing boundary used to close a bucket. Replay uses this to flush a
// bounded range even when the source event was received much later.
func (d *EventBatcher) ingestMemoryWithDeadline(messageID string, event *storagepb.RowsUpserted, receivedAt, processingAt time.Time) bool {
	if d == nil || event == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pruneClosedLocked(processingAt)
	matched := false
	for _, row := range event.GetRows() {
		if row == nil || row.GetKey() == nil {
			continue
		}
		rowKey := row.GetKey().GetTimeSeries()
		if rowKey == nil {
			continue
		}
		key := &storagepb.TimeSeriesKey{SpaceId: row.GetKey().GetSpaceId(), DatasetId: row.GetKey().GetDatasetId(), SubjectId: rowKey.GetSubjectId(), Freq: rowKey.GetFreq(), DataTime: rowKey.GetDataTime()}
		dataTime, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
		if err != nil {
			continue
		}
		matches := d.matchBindings(key)
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
				subjectID:     key.GetSubjectId(),
				freq:          key.GetFreq(),
			}
			b := d.buckets[bkey]
			if b == nil {
				late := false
				if closedAt, ok := d.closed[bkey]; ok && !dataTime.After(closedAt.dataTime) {
					late = true
				}
				b = &bucket{
					task: Task{
						SpaceID:         key.GetSpaceId(),
						SourceDataset:   key.GetDatasetId(),
						TargetDataset:   targetDataset,
						SubjectID:       key.GetSubjectId(),
						Freq:            key.GetFreq(),
						BarTime:         dataTime.UTC(),
						FirstReceivedAt: receivedAt.UTC(),
						LastReceivedAt:  receivedAt.UTC(),
						MinDataTime:     dataTime.UTC(),
						MaxDataTime:     dataTime.UTC(),
						LateDataPolicy:  LateDataPolicyRecompute,
						LateData:        late,
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
			if dataTime.Before(b.task.MinDataTime) {
				b.task.MinDataTime = dataTime.UTC()
			}
			if dataTime.After(b.task.MaxDataTime) {
				b.task.MaxDataTime = dataTime.UTC()
			}
			b.factors[binding.FactorID] = struct{}{}
			if messageID != "" {
				b.messageIDs[messageID] = struct{}{}
			}
		}
	}
	return matched
}

// Flush is an in-memory compatibility helper. Durable callers must use
// FlushPending followed by CommitPending so inbox errors are observable.
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

// FlushPending removes eligible buckets from memory but never deletes durable
// inbox rows. The returned task carries the rows it covers; callers must call
// CommitPending only after downstream task acceptance. If downstream work
// fails, RestorePending rehydrates the still-pending rows.
func (d *EventBatcher) FlushPending(ctx context.Context, now time.Time) ([]Task, error) {
	if d == nil {
		return nil, errors.New("factor event batcher is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]bucketKey, 0, len(d.buckets))
	for key, b := range d.buckets {
		if !now.Before(b.deadline) {
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
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b := d.buckets[key]
		b.task.FactorIDs = orderedFactorIDs(b.factors, d.bindings)
		b.task.PendingEventIDs = orderedMessageIDs(b.messageIDs)
		tasks = append(tasks, b.task)
	}
	for _, key := range keys {
		if maxDataTime := d.buckets[key].task.MaxDataTime; !maxDataTime.IsZero() {
			d.closed[key] = closedBucket{dataTime: maxDataTime, closedAt: now.UTC()}
		}
		delete(d.buckets, key)
	}
	return tasks, nil
}

func (d *EventBatcher) pruneClosedLocked(now time.Time) {
	if len(d.closed) == 0 {
		return
	}
	cutoff := now.UTC().Add(-closedBucketRetention)
	for key, closed := range d.closed {
		if closed.closedAt.Before(cutoff) {
			delete(d.closed, key)
		}
	}
	for len(d.closed) > maxClosedBuckets {
		var oldestKey bucketKey
		var oldest time.Time
		for key, closed := range d.closed {
			if oldest.IsZero() || closed.closedAt.Before(oldest) {
				oldestKey, oldest = key, closed.closedAt
			}
		}
		delete(d.closed, oldestKey)
	}
}

// CommitPending records the message IDs as processed and removes their inbox
// rows atomically. It is safe to retry after a database error.
func (d *EventBatcher) CommitPending(ctx context.Context, tasks ...Task) error {
	if d == nil {
		return errors.New("factor event batcher is nil")
	}
	if d.inbox == nil {
		return nil
	}
	messageSet := map[string]struct{}{}
	for _, task := range tasks {
		for _, messageID := range task.PendingEventIDs {
			if messageID != "" {
				messageSet[messageID] = struct{}{}
			}
		}
	}
	return d.inbox.CommitPendingEvents(ctx, orderedMessageIDs(messageSet))
}

// RestorePending rehydrates inbox rows after a flushed task could not be
// accepted. New live events may arrive concurrently; message IDs make replay
// idempotent when they share a fixed-window bucket with those events.
func (d *EventBatcher) RestorePending(ctx context.Context) error {
	return d.Replay(ctx)
}

// Replay reloads pending events after restart without writing them again.
func (d *EventBatcher) Replay(ctx context.Context) error {
	if d == nil || d.inbox == nil {
		return nil
	}
	return d.inbox.LoadPendingEvents(ctx, func(messageID string, event *storagepb.RowsUpserted, receivedAt time.Time) error {
		if !d.ingestMemory(messageID, event, receivedAt) {
			return d.inbox.CommitPendingEvents(ctx, []string{messageID})
		}
		return nil
	})
}

func orderedMessageIDs(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for messageID := range set {
		if messageID != "" {
			out = append(out, messageID)
		}
	}
	sort.Strings(out)
	return out
}

func (d *EventBatcher) matchBindings(key *storagepb.TimeSeriesKey) []domain.FactorBinding {
	out := []domain.FactorBinding{}
	for _, binding := range d.bindings {
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		if binding.SpaceID != key.GetSpaceId() || binding.SourceDataset != key.GetDatasetId() || binding.Freq != key.GetFreq() {
			continue
		}
		if !subjectAllowed(binding, key.GetSubjectId()) {
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
