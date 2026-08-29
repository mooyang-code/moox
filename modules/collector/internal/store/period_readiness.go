package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type PeriodReadinessRepository struct {
	db *gorm.DB
}

func NewPeriodReadinessRepository(db *gorm.DB) *PeriodReadinessRepository {
	return &PeriodReadinessRepository{db: db}
}

// EnsurePeriod creates the immutable expected task set. An existing period is
// deliberately left untouched so a later assignment reconciliation cannot
// change the meaning of a period that is already in flight.
func (r *PeriodReadinessRepository) EnsurePeriod(ctx context.Context, seed domain.PeriodSeed) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("period readiness repository is not initialized")
	}
	if strings.TrimSpace(seed.SpaceID) == "" || strings.TrimSpace(seed.DatasetID) == "" || strings.TrimSpace(seed.Frequency) == "" || seed.PeriodTime.IsZero() || seed.DeadlineAt.IsZero() {
		return 0, fmt.Errorf("space_id, dataset_id, frequency, period_time and deadline_at are required")
	}
	if len(seed.Tasks) == 0 {
		return 0, fmt.Errorf("period readiness requires at least one task")
	}
	seenSubjects := make(map[string]struct{}, len(seed.Tasks))
	for _, task := range seed.Tasks {
		if strings.TrimSpace(task.TaskID) == "" || strings.TrimSpace(task.SubjectID) == "" {
			return 0, fmt.Errorf("task_id and subject_id are required")
		}
		if _, exists := seenSubjects[task.SubjectID]; exists {
			return 0, fmt.Errorf("duplicate period subject %q", task.SubjectID)
		}
		seenSubjects[task.SubjectID] = struct{}{}
	}

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		workType := strings.TrimSpace(seed.WorkType)
		if workType == "" {
			workType = "collection"
		}
		parent := &domain.PeriodReadiness{
			SpaceID: seed.SpaceID, DatasetID: seed.DatasetID, Frequency: seed.Frequency, WorkType: workType,
			PeriodTime: seed.PeriodTime.UTC(), DeadlineAt: seed.DeadlineAt.UTC(),
			Status: domain.PeriodStatusWaiting, ReportState: domain.PeriodReportWaiting,
		}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(parent)
		if result.Error != nil {
			return result.Error
		}
		// A period is an immutable subject snapshot.  Do not append newly
		// assigned tasks when the parent already existed; they belong to the
		// next period created after the assignment change.
		if result.RowsAffected == 0 {
			return nil
		}
		if err := tx.Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", seed.SpaceID, seed.DatasetID, seed.Frequency, seed.PeriodTime.UTC()).First(parent).Error; err != nil {
			return err
		}
		for _, task := range seed.Tasks {
			item := &domain.PeriodReadinessItem{
				ReadinessID: parent.ID, TaskID: task.TaskID, SubjectID: task.SubjectID,
				FunctionName: task.FunctionName, WriteSource: task.WriteSource,
				RequiredFields: task.RequiredFields, State: domain.PeriodItemPending,
				UpdatedAt: time.Now().UTC(),
			}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(item).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	var current domain.PeriodReadiness
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?", seed.SpaceID, seed.DatasetID, seed.Frequency, seed.PeriodTime.UTC()).First(&current).Error; err != nil {
		return 0, err
	}
	return current.ID, nil
}

func (r *PeriodReadinessRepository) MarkSubjectSuccess(ctx context.Context, key domain.PeriodKey, subjectID, functionName, writeSource string, at time.Time) error {
	return r.MarkSubjectSuccessWithFields(ctx, key, subjectID, functionName, writeSource, nil, at)
}

// MarkSubjectSuccessWithFields advances a period item only when its immutable
// required-field snapshot is satisfied by the storage row. A nil field list
// preserves the legacy caller contract for providers without a field schema.
func (r *PeriodReadinessRepository) MarkSubjectSuccessWithFields(ctx context.Context, key domain.PeriodKey, subjectID, functionName, writeSource string, fields []string, at time.Time) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("period readiness repository is not initialized")
	}
	if key.PeriodTime.IsZero() || strings.TrimSpace(subjectID) == "" || at.IsZero() {
		return fmt.Errorf("period, subject_id and event time are required")
	}
	var items []domain.PeriodReadinessItem
	query := r.db.WithContext(ctx).Where("c_readiness_id IN (SELECT c_id FROM t_period_readiness WHERE c_space_id = ? AND c_dataset_id = ? AND c_frequency = ? AND c_period_time = ?)", key.SpaceID, key.DatasetID, key.Frequency, key.PeriodTime.UTC()).Where("c_subject_id = ? AND c_state = ?", subjectID, domain.PeriodItemPending)
	if strings.TrimSpace(functionName) != "" {
		// An empty function/source denotes a subject-level item created from
		// overlapping rules; either writer may satisfy it.
		query = query.Where("(c_function_name = ? OR c_function_name = '')", functionName)
	}
	if strings.TrimSpace(writeSource) != "" {
		query = query.Where("(c_write_source = ? OR c_write_source = '')", writeSource)
	}
	if err := query.Find(&items).Error; err != nil {
		return err
	}
	available := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		available[field] = struct{}{}
		if _, suffix, ok := strings.Cut(field, "."); ok {
			available[suffix] = struct{}{}
		}
	}
	for _, item := range items {
		if fields != nil {
			var required []string
			if strings.TrimSpace(item.RequiredFields) != "" && json.Unmarshal([]byte(item.RequiredFields), &required) != nil {
				return fmt.Errorf("invalid required fields for task %s", item.TaskID)
			}
			complete := true
			for _, field := range required {
				if _, ok := available[field]; !ok {
					complete = false
					break
				}
			}
			if !complete {
				continue
			}
		}
		if err := r.db.WithContext(ctx).Model(&domain.PeriodReadinessItem{}).Where("c_readiness_id = ? AND c_task_id = ? AND c_state = ?", item.ReadinessID, item.TaskID, domain.PeriodItemPending).Updates(map[string]any{"c_state": domain.PeriodItemSuccess, "c_updated_at": at.UTC()}).Error; err != nil {
			return err
		}
	}
	return nil
}

// FinalizeDue atomically moves ready or deadline-expired parents to
// report_pending and fixes the collection timestamp. The timestamp is fixed
// before the reporter constructs its payload, so a crash/retry cannot change
// the event's timestamp.
func (r *PeriodReadinessRepository) FinalizeDue(ctx context.Context, now time.Time, limit int) ([]domain.PeriodReport, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("period readiness repository is not initialized")
	}
	if limit <= 0 {
		limit = 100
	}
	var parents []domain.PeriodReadiness
	if err := r.db.WithContext(ctx).
		Where("c_report_state = ? AND (c_deadline_at <= ? OR c_id IN (SELECT c_readiness_id FROM t_period_readiness_items GROUP BY c_readiness_id HAVING SUM(CASE WHEN c_state = 'pending' THEN 1 ELSE 0 END) = 0))", domain.PeriodReportWaiting, now.UTC()).
		Order("c_deadline_at ASC, c_id ASC").Limit(limit).Find(&parents).Error; err != nil {
		return nil, err
	}
	result := make([]domain.PeriodReport, 0, len(parents))
	for i := range parents {
		parent := &parents[i]
		// Resample source windows may legitimately arrive after the normal
		// collection deadline. Keep the parent waiting and retry its deadline
		// later instead of timing out items and publishing an irreversible
		// degraded marker.
		if parent.WorkType == "resample" && !parent.DeadlineAt.After(now.UTC()) {
			var pending int64
			if err := r.db.WithContext(ctx).Model(&domain.PeriodReadinessItem{}).
				Where("c_readiness_id = ? AND c_state = ?", parent.ID, domain.PeriodItemPending).
				Count(&pending).Error; err != nil {
				return nil, err
			}
			if pending > 0 {
				terminal, err := r.resamplePendingTasksTerminal(ctx, parent.ID, parent.SpaceID, pending)
				if err != nil {
					return nil, err
				}
				if terminal {
					// A retention-expired/malformed source is a terminal source
					// failure, not a degraded realtime marker. Close the internal
					// readiness row as suppressed (report_state=reported keeps it
					// out of the marker reporter) and let retention cleanup remove it.
					if err := r.db.WithContext(ctx).Model(&domain.PeriodReadinessItem{}).
						Where("c_readiness_id = ? AND c_state = ?", parent.ID, domain.PeriodItemPending).
						Updates(map[string]any{"c_state": domain.PeriodItemTimedOut, "c_updated_at": now.UTC()}).Error; err != nil {
						return nil, err
					}
					if err := r.db.WithContext(ctx).Model(parent).Where("c_report_state = ?", domain.PeriodReportWaiting).Updates(map[string]any{
						"c_status": domain.PeriodStatusDegraded, "c_report_state": domain.PeriodReportReported,
						"c_collected_at": now.UTC(), "c_mtime": now.UTC(),
					}).Error; err != nil {
						return nil, err
					}
					continue
				}
				if err := r.db.WithContext(ctx).Model(parent).Where("c_report_state = ?", domain.PeriodReportWaiting).Updates(map[string]any{
					"c_deadline_at": now.UTC().Add(time.Minute), "c_mtime": now.UTC(),
				}).Error; err != nil {
					return nil, err
				}
				continue
			}
		}
		if !parent.DeadlineAt.After(now.UTC()) {
			if err := r.db.WithContext(ctx).Model(&domain.PeriodReadinessItem{}).
				Where("c_readiness_id = ? AND c_state = ?", parent.ID, domain.PeriodItemPending).
				Updates(map[string]any{"c_state": domain.PeriodItemTimedOut, "c_updated_at": now.UTC()}).Error; err != nil {
				return nil, err
			}
		}
		var items []domain.PeriodReadinessItem
		if err := r.db.WithContext(ctx).Where("c_readiness_id = ?", parent.ID).Order("c_subject_id ASC").Find(&items).Error; err != nil {
			return nil, err
		}
		status := domain.PeriodStatusComplete
		for _, item := range items {
			if item.State != domain.PeriodItemSuccess {
				status = domain.PeriodStatusDegraded
				break
			}
		}
		if err := r.db.WithContext(ctx).Model(parent).Where("c_report_state = ?", domain.PeriodReportWaiting).Updates(map[string]any{
			"c_status": status, "c_report_state": domain.PeriodReportPending,
			"c_event_id":     periodEventID(parent.SpaceID, parent.DatasetID, parent.Frequency, parent.PeriodTime),
			"c_collected_at": now.UTC(), "c_mtime": now.UTC(),
		}).Error; err != nil {
			return nil, err
		}
		result = append(result, domain.PeriodReport{Readiness: *parent, Items: items})
		result[len(result)-1].Readiness.Status = status
		result[len(result)-1].Readiness.ReportState = domain.PeriodReportPending
		result[len(result)-1].Readiness.CollectedAt = now.UTC()
		result[len(result)-1].Readiness.EventID = periodEventID(parent.SpaceID, parent.DatasetID, parent.Frequency, parent.PeriodTime)
	}
	return result, nil
}

func (r *PeriodReadinessRepository) resamplePendingTasksTerminal(ctx context.Context, readinessID int64, spaceID string, pending int64) (bool, error) {
	var failed int64
	err := r.db.WithContext(ctx).Table("t_period_readiness_items AS items").
		Joins("LEFT JOIN t_collector_task_instances AS tasks ON tasks.c_space_id = ? AND tasks.c_task_id = items.c_task_id", spaceID).
		Where("items.c_readiness_id = ? AND items.c_state = ? AND (tasks.c_is_deleted = 1 OR (json_valid(tasks.c_result) AND json_extract(tasks.c_result, '$.state') = ?))", readinessID, domain.PeriodItemPending, domain.ResampleTaskStateFailed).
		Count(&failed).Error
	if err != nil {
		return false, err
	}
	return pending > 0 && failed == pending, nil
}

func (r *PeriodReadinessRepository) PersistPayload(ctx context.Context, readinessID int64, payloadJSON string) error {
	if readinessID <= 0 || strings.TrimSpace(payloadJSON) == "" {
		return fmt.Errorf("readiness_id and payload are required")
	}
	return r.db.WithContext(ctx).Model(&domain.PeriodReadiness{}).
		Where("c_id = ? AND c_report_state = ? AND (c_payload_json = '{}' OR c_payload_json = '')", readinessID, domain.PeriodReportPending).
		Updates(map[string]any{"c_payload_json": payloadJSON, "c_mtime": time.Now().UTC()}).Error
}

func (r *PeriodReadinessRepository) ListPendingReports(ctx context.Context, limit int) ([]domain.PeriodReport, error) {
	if limit <= 0 {
		limit = 100
	}
	var parents []domain.PeriodReadiness
	if err := r.db.WithContext(ctx).Where("c_report_state = ?", domain.PeriodReportPending).Order("c_id ASC").Limit(limit).Find(&parents).Error; err != nil {
		return nil, err
	}
	result := make([]domain.PeriodReport, 0, len(parents))
	for _, parent := range parents {
		var items []domain.PeriodReadinessItem
		if err := r.db.WithContext(ctx).Where("c_readiness_id = ?", parent.ID).Order("c_subject_id ASC").Find(&items).Error; err != nil {
			return nil, err
		}
		result = append(result, domain.PeriodReport{Readiness: parent, Items: items})
	}
	return result, nil
}

// CountPendingReports returns the current pending backlog grouped by
// dataset/frequency. It is intentionally unbounded by the delivery page so
// operational gauges cannot report zero merely because the first page drained.
func (r *PeriodReadinessRepository) CountPendingReports(ctx context.Context) (map[string]int, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("period readiness repository is not initialized")
	}
	type countRow struct {
		DatasetID string `gorm:"column:c_dataset_id"`
		Frequency string `gorm:"column:c_frequency"`
		Count     int    `gorm:"column:c_count"`
	}
	var rows []countRow
	if err := r.db.WithContext(ctx).Table("t_period_readiness").Select("c_dataset_id, c_frequency, count(*) AS c_count").Where("c_report_state = ?", domain.PeriodReportPending).Group("c_dataset_id, c_frequency").Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make(map[string]int, len(rows))
	for _, row := range rows {
		result[row.DatasetID+"\x00"+row.Frequency] = row.Count
	}
	return result, nil
}

func (r *PeriodReadinessRepository) MarkReported(ctx context.Context, readinessID int64) error {
	if readinessID <= 0 {
		return fmt.Errorf("readiness_id is required")
	}
	return r.db.WithContext(ctx).Model(&domain.PeriodReadiness{}).
		Where("c_id = ? AND c_report_state = ? AND c_payload_json <> '{}'", readinessID, domain.PeriodReportPending).
		Updates(map[string]any{"c_report_state": domain.PeriodReportReported, "c_mtime": time.Now().UTC()}).Error
}

func (r *PeriodReadinessRepository) DeleteBefore(ctx context.Context, before time.Time) (int64, error) {
	result := r.db.WithContext(ctx).Where("c_report_state = ? AND c_collected_at < ?", domain.PeriodReportReported, before.UTC()).Delete(&domain.PeriodReadiness{})
	return result.RowsAffected, result.Error
}

// DeleteReportedItemsOutsideWindow keeps only the newest N reported period
// snapshots per Dataset/frequency. Pending parents are excluded so a delayed
// report can still be retried with its full subject state.
func (r *PeriodReadinessRepository) DeleteReportedItemsOutsideWindow(ctx context.Context, periods int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, fmt.Errorf("period readiness repository is not initialized")
	}
	if periods <= 0 {
		return 0, nil
	}
	query := `DELETE FROM t_period_readiness_items
WHERE c_readiness_id IN (
  SELECT c_id FROM (
    SELECT c_id,
           ROW_NUMBER() OVER (PARTITION BY c_dataset_id, c_frequency ORDER BY c_period_time DESC, c_id DESC) AS c_rank
      FROM t_period_readiness
     WHERE c_report_state = ?
  )
 WHERE c_rank > ?
)`
	result := r.db.WithContext(ctx).Exec(query, domain.PeriodReportReported, periods)
	return result.RowsAffected, result.Error
}

func periodEventID(spaceID, datasetID, frequency string, period time.Time) string {
	return "dataset-period-collected/" + strings.Join([]string{spaceID, datasetID, frequency, period.UTC().Format(time.RFC3339)}, "/")
}
