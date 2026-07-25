package trigger

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/packages/storagepb"
)

// IngestMessage persists the event before exposing it to the in-memory window.
func (d *EventBatcher) IngestMessage(ctx context.Context, messageID string, event *storagepb.DatasetRowsUpserted, now time.Time) error {
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
		delete(d.buckets, key)
	}
	return tasks, nil
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
	return d.inbox.LoadPendingEvents(ctx, func(messageID string, event *storagepb.DatasetRowsUpserted, receivedAt time.Time) error {
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
