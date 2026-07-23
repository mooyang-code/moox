package trigger

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// ReplayRequest is the explicit offline execution boundary for Factor. A
// replay never inherits the live consumer's processing-time deadline.
type ReplayRequest struct {
	SpaceID       string
	DatasetID     string
	StartTime     time.Time
	EndTime       time.Time
	FactorVersion string
	TargetRunID   string
}

func (r ReplayRequest) Validate() error {
	if r.SpaceID == "" || r.DatasetID == "" {
		return errors.New("replay space_id and dataset_id are required")
	}
	if r.StartTime.IsZero() || r.EndTime.IsZero() || !r.StartTime.Before(r.EndTime) {
		return errors.New("replay start_time must be before end_time")
	}
	if r.FactorVersion == "" {
		return errors.New("replay factor_version is required")
	}
	if r.TargetRunID == "" {
		return errors.New("replay target_run_id is required")
	}
	return nil
}

// ReplayEvent is the source-independent shape returned by an offline reader.
// ReceivedAt is required so replay cannot accidentally use time.Now() to
// determine a processing window.
type ReplayEvent struct {
	MessageID  string
	Event      *storagepb.RowsUpserted
	ReceivedAt time.Time
}

type ReplaySource interface {
	Load(context.Context, ReplayRequest) ([]ReplayEvent, error)
}

// ReplayRange ingests a bounded historical range and flushes it using an
// explicit end-time boundary. The returned tasks retain replay identity and
// timing metadata for the scheduler and audit trail.
func (d *EventBatcher) ReplayRange(ctx context.Context, req ReplayRequest, source ReplaySource) ([]Task, error) {
	if d == nil {
		return nil, errors.New("factor event batcher is nil")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("replay source is required")
	}
	d.mu.Lock()
	window := d.window
	bindings := append([]domain.FactorBinding(nil), d.bindings...)
	d.mu.Unlock()
	// Replay is an offline execution scope. It must not share live buckets or
	// the live durable inbox, otherwise a bounded rebuild can flush live work.
	events, err := source.Load(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("load factor replay events: %w", err)
	}
	groups := make(map[string][]ReplayEvent)
	for _, item := range events {
		if item.Event == nil || item.MessageID == "" || item.ReceivedAt.IsZero() {
			return nil, errors.New("replay event requires message_id, event, and received_at")
		}
		if item.Event.GetSpaceId() != req.SpaceID || item.Event.GetDatasetId() != req.DatasetID {
			return nil, errors.New("replay event is outside requested dataset")
		}
		if err := validateReplayDataTimes(item.Event, req.StartTime, req.EndTime); err != nil {
			return nil, err
		}
		for _, row := range item.Event.GetRows() {
			if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
				continue
			}
			key := row.GetKey().GetTimeSeries()
			groupKey := strings.Join([]string{key.GetSubjectId(), key.GetFreq(), key.GetDataTime()}, "\x00")
			groups[groupKey] = append(groups[groupKey], ReplayEvent{
				MessageID: item.MessageID,
				Event: &storagepb.RowsUpserted{
					SpaceId: item.Event.GetSpaceId(), DatasetId: item.Event.GetDatasetId(), Rows: []*storagepb.RowFieldUpsert{row},
				},
				ReceivedAt: item.ReceivedAt,
			})
		}
	}
	groupKeys := make([]string, 0, len(groups))
	for groupKey := range groups {
		groupKeys = append(groupKeys, groupKey)
	}
	sort.Strings(groupKeys)
	tasks := make([]Task, 0, len(groupKeys))
	for _, groupKey := range groupKeys {
		replayBatcher := NewEventBatcher(window, bindings)
		for _, item := range groups[groupKey] {
			replayBatcher.ingestMemoryWithDeadline(item.MessageID, item.Event, item.ReceivedAt, req.EndTime)
		}
		groupTasks, err := replayBatcher.FlushPending(ctx, req.EndTime.Add(window))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, groupTasks...)
	}
	for i := range tasks {
		tasks[i].TriggerType = "replay"
		tasks[i].FactorVersion = req.FactorVersion
		tasks[i].TargetRunID = req.TargetRunID
	}
	return tasks, nil
}

func validateReplayDataTimes(event *storagepb.RowsUpserted, start, end time.Time) error {
	for _, row := range event.GetRows() {
		if row == nil || row.GetKey() == nil || row.GetKey().GetTimeSeries() == nil {
			return errors.New("replay row must contain a time-series key")
		}
		dataTime, err := time.Parse(time.RFC3339Nano, row.GetKey().GetTimeSeries().GetDataTime())
		if err != nil {
			return fmt.Errorf("parse replay data_time: %w", err)
		}
		if dataTime.Before(start) || dataTime.After(end) {
			return fmt.Errorf("replay data_time %s is outside requested range", dataTime.UTC().Format(time.RFC3339Nano))
		}
	}
	return nil
}
