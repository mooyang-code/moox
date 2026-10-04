package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (s *Store) UpsertViewPeriodDatasetState(ctx context.Context, item *pb.ViewPeriodDatasetState) (*pb.ViewPeriodDatasetState, error) {
	if err := validateViewPeriodDatasetState(item); err != nil {
		return nil, err
	}
	next := cloneViewPeriodDatasetState(item)
	next.SubjectIds = uniqueStrings(next.GetSubjectIds())
	next.FailedSubjects = uniqueStrings(next.GetFailedSubjects())
	now := s.now().UTC()
	if next.GetUpdatedAt() == nil {
		next.UpdatedAt = timestamppb.New(now)
	}
	subjects, _ := json.Marshal(next.GetSubjectIds())
	failed, _ := json.Marshal(next.GetFailedSubjects())
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO t_view_period_dataset_states (
			c_space_id, c_view_id, c_dataset_id, c_frequency, c_period_time,
			c_event_id, c_status, c_subject_ids_json, c_failed_subjects_json, c_occurred_at, c_updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id, c_dataset_id, c_frequency, c_period_time) DO NOTHING
	`, next.GetSpaceId(), next.GetViewId(), next.GetDatasetId(), next.GetFrequency(), next.GetPeriodTime(), next.GetEventId(), next.GetStatus(), string(subjects), string(failed), timestampText(next.GetOccurredAt()), timestampText(next.GetUpdatedAt()))
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	existing, err := s.getViewPeriodDatasetState(ctx, next.GetSpaceId(), next.GetViewId(), next.GetDatasetId(), next.GetFrequency(), next.GetPeriodTime())
	if err != nil {
		return nil, err
	}
	if changed == 0 && !sameViewPeriodDatasetState(existing, next) {
		return nil, errors.New("view period dataset state conflict")
	}
	return existing, nil
}

func (s *Store) ListViewPeriodDatasetStates(ctx context.Context, spaceID, viewID, frequency string, periodTime int64) ([]*pb.ViewPeriodDatasetState, error) {
	if spaceID == "" || viewID == "" || frequency == "" || periodTime <= 0 {
		return nil, errors.New("space_id, view_id, frequency and period_time are required")
	}
	rows, err := s.queryDB(ctx).QueryContext(ctx, `
		SELECT c_space_id, c_view_id, c_dataset_id, c_frequency, c_period_time,
		       c_event_id, c_status, c_subject_ids_json, c_failed_subjects_json, c_occurred_at, c_updated_at
		FROM t_view_period_dataset_states
		WHERE c_space_id = ? AND c_view_id = ? AND c_frequency = ? AND c_period_time = ?
		ORDER BY c_dataset_id
	`, spaceID, viewID, frequency, periodTime)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []*pb.ViewPeriodDatasetState
	for rows.Next() {
		item, err := scanViewPeriodDatasetState(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *Store) getViewPeriodDatasetState(ctx context.Context, spaceID, viewID, datasetID, frequency string, periodTime int64) (*pb.ViewPeriodDatasetState, error) {
	return scanViewPeriodDatasetState(s.queryDB(ctx).QueryRowContext(ctx, `
		SELECT c_space_id, c_view_id, c_dataset_id, c_frequency, c_period_time,
		       c_event_id, c_status, c_subject_ids_json, c_failed_subjects_json, c_occurred_at, c_updated_at
		FROM t_view_period_dataset_states
		WHERE c_space_id = ? AND c_view_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?
	`, spaceID, viewID, datasetID, frequency, periodTime))
}

func (s *Store) RecordViewSyncPoint(ctx context.Context, item *pb.ViewSyncPoint) (*pb.ViewSyncPoint, error) {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetDatasetId() == "" || item.GetRequestId() == "" || item.GetSyncPointId() == "" {
		return nil, errors.New("space_id, view_id, dataset_id, request_id and sync_point_id are required")
	}
	appliedAt := item.GetAppliedAt()
	if appliedAt == nil {
		appliedAt = timestamppb.New(s.now().UTC())
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO t_view_sync_points (c_space_id, c_view_id, c_dataset_id, c_request_id, c_sync_point_id, c_applied_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(c_space_id, c_view_id, c_dataset_id, c_request_id) DO NOTHING
	`, item.GetSpaceId(), item.GetViewId(), item.GetDatasetId(), item.GetRequestId(), item.GetSyncPointId(), timestampText(appliedAt))
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	existing, err := s.getViewSyncPoint(ctx, item.GetSpaceId(), item.GetViewId(), item.GetDatasetId(), item.GetRequestId())
	if err != nil {
		return nil, err
	}
	if changed == 0 && existing.GetSyncPointId() != item.GetSyncPointId() {
		return nil, errors.New("view sync point conflict")
	}
	return existing, nil
}

func (s *Store) MissingViewSyncPointDatasets(ctx context.Context, spaceID, viewID, requestID string, datasetIDs []string) ([]string, error) {
	if spaceID == "" || viewID == "" || requestID == "" {
		return nil, errors.New("space_id, view_id and request_id are required")
	}
	wanted := uniqueStrings(datasetIDs)
	missing := make([]string, 0, len(wanted))
	for _, datasetID := range wanted {
		var count int
		if err := s.queryDB(ctx).QueryRowContext(ctx, `
			SELECT COUNT(1) FROM t_view_sync_points
			WHERE c_space_id = ? AND c_view_id = ? AND c_dataset_id = ? AND c_request_id = ?
		`, spaceID, viewID, datasetID, requestID).Scan(&count); err != nil {
			return nil, err
		}
		if count == 0 {
			missing = append(missing, datasetID)
		}
	}
	return missing, nil
}

// DeleteViewPeriodDatasetStatesBefore removes only old period projections. The
// source/result Views remain authoritative; these rows are a replay aid and
// are safe to expire after the configured recovery window.
func (s *Store) DeleteViewPeriodDatasetStatesBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_view_period_dataset_states WHERE c_updated_at < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DeleteViewSyncPointsBefore removes old import/catchup fences after their
// caller's recovery window. A sync point is not a source-of-truth row.
func (s *Store) DeleteViewSyncPointsBefore(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM t_view_sync_points WHERE c_applied_at < ?`, before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) getViewSyncPoint(ctx context.Context, spaceID, viewID, datasetID, requestID string) (*pb.ViewSyncPoint, error) {
	var item pb.ViewSyncPoint
	var appliedAt string
	err := s.queryDB(ctx).QueryRowContext(ctx, `
		SELECT c_space_id, c_view_id, c_dataset_id, c_request_id, c_sync_point_id, c_applied_at
		FROM t_view_sync_points
		WHERE c_space_id = ? AND c_view_id = ? AND c_dataset_id = ? AND c_request_id = ?
	`, spaceID, viewID, datasetID, requestID).Scan(&item.SpaceId, &item.ViewId, &item.DatasetId, &item.RequestId, &item.SyncPointId, &appliedAt)
	if err != nil {
		return nil, err
	}
	item.AppliedAt, err = parseTimestamp(appliedAt)
	return &item, err
}

func validateViewPeriodDatasetState(item *pb.ViewPeriodDatasetState) error {
	if item == nil || item.GetSpaceId() == "" || item.GetViewId() == "" || item.GetDatasetId() == "" || item.GetFrequency() == "" || item.GetPeriodTime() <= 0 || item.GetEventId() == "" {
		return errors.New("space_id, view_id, dataset_id, frequency, period_time and event_id are required")
	}
	if item.GetStatus() != "complete" && item.GetStatus() != "degraded" {
		return errors.New("status must be complete or degraded")
	}
	if item.GetOccurredAt() == nil || item.GetOccurredAt().CheckValid() != nil {
		return errors.New("occurred_at must be valid")
	}
	return nil
}

func scanViewPeriodDatasetState(row interface{ Scan(...any) error }) (*pb.ViewPeriodDatasetState, error) {
	item := &pb.ViewPeriodDatasetState{}
	var subjectsJSON, failedJSON, occurredAt, updatedAt string
	if err := row.Scan(&item.SpaceId, &item.ViewId, &item.DatasetId, &item.Frequency, &item.PeriodTime, &item.EventId, &item.Status, &subjectsJSON, &failedJSON, &occurredAt, &updatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(subjectsJSON), &item.SubjectIds); err != nil {
		return nil, fmt.Errorf("decode view period subjects: %w", err)
	}
	if err := json.Unmarshal([]byte(failedJSON), &item.FailedSubjects); err != nil {
		return nil, fmt.Errorf("decode view period failed subjects: %w", err)
	}
	var err error
	if item.OccurredAt, err = parseTimestamp(occurredAt); err != nil {
		return nil, err
	}
	if item.UpdatedAt, err = parseTimestamp(updatedAt); err != nil {
		return nil, err
	}
	return item, nil
}

func sameViewPeriodDatasetState(left, right *pb.ViewPeriodDatasetState) bool {
	return left != nil && right != nil && left.GetEventId() == right.GetEventId() && left.GetStatus() == right.GetStatus() &&
		strings.Join(left.GetSubjectIds(), "\x00") == strings.Join(right.GetSubjectIds(), "\x00") &&
		strings.Join(left.GetFailedSubjects(), "\x00") == strings.Join(right.GetFailedSubjects(), "\x00") &&
		left.GetOccurredAt().AsTime().Equal(right.GetOccurredAt().AsTime())
}

func cloneViewPeriodDatasetState(item *pb.ViewPeriodDatasetState) *pb.ViewPeriodDatasetState {
	return &pb.ViewPeriodDatasetState{
		SpaceId: item.GetSpaceId(), ViewId: item.GetViewId(), DatasetId: item.GetDatasetId(), Frequency: item.GetFrequency(), PeriodTime: item.GetPeriodTime(),
		EventId: item.GetEventId(), Status: item.GetStatus(), SubjectIds: append([]string(nil), item.GetSubjectIds()...), FailedSubjects: append([]string(nil), item.GetFailedSubjects()...),
		OccurredAt: item.GetOccurredAt(), UpdatedAt: item.GetUpdatedAt(),
	}
}

func uniqueStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func timestampText(value *timestamppb.Timestamp) string {
	return value.AsTime().UTC().Format(time.RFC3339Nano)
}

func parseTimestamp(value string) (*timestamppb.Timestamp, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, fmt.Errorf("parse metadata timestamp %q: %w", value, err)
	}
	return timestamppb.New(parsed.UTC()), nil
}
