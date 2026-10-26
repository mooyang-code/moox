package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gorm.io/gorm"
)

type factorTaskRow struct {
	TaskID          string    `gorm:"column:c_task_id"`
	SpaceID         string    `gorm:"column:c_space_id"`
	SourceDatasetID string    `gorm:"column:c_source_dataset_id"`
	Freq            string    `gorm:"column:c_freq"`
	ResultDatasetID string    `gorm:"column:c_result_dataset_id"`
	OwnerInstanceID string    `gorm:"column:c_owner_instance_id"`
	Status          string    `gorm:"column:c_status"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
}

func (r factorTaskRow) domain() (domain.FactorTask, error) {
	return domain.FactorTask{
		TaskID: r.TaskID, SpaceID: r.SpaceID, SourceDatasetID: r.SourceDatasetID,
		Freq:            r.Freq,
		ResultDatasetID: r.ResultDatasetID, OwnerInstanceID: r.OwnerInstanceID, Status: r.Status,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateTask creates one factor task. A set id or result dataset uniqueness
// violation is returned as ErrConflict.
func (s *Store) CreateTask(ctx context.Context, set domain.FactorTask) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	set.TaskID = strings.TrimSpace(set.TaskID)
	set.SpaceID = strings.TrimSpace(set.SpaceID)
	set.SourceDatasetID = strings.TrimSpace(set.SourceDatasetID)
	set.Freq = strings.TrimSpace(set.Freq)
	set.ResultDatasetID = strings.TrimSpace(set.ResultDatasetID)
	set.OwnerInstanceID = strings.TrimSpace(set.OwnerInstanceID)
	if set.TaskID == "" || set.SpaceID == "" || set.SourceDatasetID == "" || set.Freq == "" || set.ResultDatasetID == "" {
		return fmt.Errorf("factor task identity is incomplete")
	}
	if set.Status == "" {
		set.Status = domain.TaskStatusPending
	}
	now := time.Now().UTC()
	if set.CreatedAt.IsZero() {
		set.CreatedAt = now
	}
	set.UpdatedAt = now
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_factor_tasks
		(c_task_id, c_space_id, c_source_dataset_id, c_freq,
		 c_result_dataset_id, c_owner_instance_id, c_status, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, set.TaskID, set.SpaceID, set.SourceDatasetID, set.Freq,
		set.ResultDatasetID, set.OwnerInstanceID, set.Status, set.CreatedAt, set.UpdatedAt)
	if result.Error != nil {
		if isUniqueConstraint(result.Error) {
			return fmt.Errorf("%w: factor task %q already exists", ErrConflict, set.TaskID)
		}
		return result.Error
	}
	return nil
}

// SetTaskStatus changes a set's lifecycle status only if it still matches
// expectedStatus. The single conditional update makes competing lifecycle
// transitions explicit instead of silently overwriting one another.
func (s *Store) SetTaskStatus(ctx context.Context, taskID, expectedStatus, status string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return fmt.Errorf("set id is required")
	}
	if !validTaskStatus(expectedStatus) || !validTaskStatus(status) {
		return fmt.Errorf("invalid factor task status transition %q to %q", expectedStatus, status)
	}
	result := s.db.WithContext(ctx).Exec(`
		UPDATE t_factor_tasks SET c_status = ?, c_mtime = ? WHERE c_task_id = ? AND c_status = ?
	`, status, time.Now().UTC(), taskID, expectedStatus)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return err
	}
	return fmt.Errorf("%w: factor task %q status changed", ErrConflict, taskID)
}

// GetTask returns a factor task by id.
func (s *Store) GetTask(ctx context.Context, taskID string) (domain.FactorTask, error) {
	if s == nil || s.db == nil {
		return domain.FactorTask{}, fmt.Errorf("factor database is not open")
	}
	var row factorTaskRow
	result := s.db.WithContext(ctx).Raw(`
		SELECT `+setColumns+` FROM t_factor_tasks WHERE c_task_id = ?
	`, strings.TrimSpace(taskID)).Scan(&row)
	if result.Error != nil {
		return domain.FactorTask{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorTask{}, gorm.ErrRecordNotFound
	}
	return row.domain()
}

// ListTasks returns all factor tasks in stable id order.
func (s *Store) ListTasks(ctx context.Context) ([]domain.FactorTask, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("factor database is not open")
	}
	var rows []factorTaskRow
	if err := s.db.WithContext(ctx).Raw(`
		SELECT ` + setColumns + ` FROM t_factor_tasks ORDER BY c_task_id
	`).Scan(&rows).Error; err != nil {
		return nil, err
	}
	sets := make([]domain.FactorTask, 0, len(rows))
	for _, row := range rows {
		set, err := row.domain()
		if err != nil {
			return nil, err
		}
		sets = append(sets, set)
	}
	return sets, nil
}

// DeleteTask removes a factor task without members. SQLite's foreign key rejects
// deletion while members still reference it, which is reported as ErrConflict.
func (s *Store) DeleteTask(ctx context.Context, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID = strings.TrimSpace(taskID)
	result := s.db.WithContext(ctx).Exec("DELETE FROM t_factor_tasks WHERE c_task_id = ?", taskID)
	if result.Error != nil {
		if isForeignKeyConstraint(result.Error) {
			return fmt.Errorf("%w: factor task %q still has members", ErrConflict, taskID)
		}
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

const setColumns = `c_task_id, c_space_id, c_source_dataset_id, c_freq, c_result_dataset_id, c_owner_instance_id, c_status, c_ctime, c_mtime`

// GetTaskByOwner returns the private factor task of a strategy instance.
func (s *Store) GetTaskByOwner(ctx context.Context, instanceID string) (domain.FactorTask, error) {
	if s == nil || s.db == nil {
		return domain.FactorTask{}, fmt.Errorf("factor database is not open")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return domain.FactorTask{}, fmt.Errorf("instance id is required")
	}
	var row factorTaskRow
	result := s.db.WithContext(ctx).Raw(`SELECT `+setColumns+` FROM t_factor_tasks WHERE c_owner_instance_id = ?`, instanceID).Scan(&row)
	if result.Error != nil {
		return domain.FactorTask{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorTask{}, gorm.ErrRecordNotFound
	}
	return row.domain()
}

func marshalStringSlice(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	data, err := json.Marshal(values)
	return string(data), err
}

func unmarshalStringSlice(raw string) ([]string, error) {
	var values []string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

func isUniqueConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique constraint failed")
}

func isForeignKeyConstraint(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "foreign key constraint failed")
}

func validTaskStatus(status string) bool {
	switch status {
	case domain.TaskStatusPending, domain.TaskStatusEnabled, domain.TaskStatusDisabled, domain.TaskStatusDeleting:
		return true
	default:
		return false
	}
}
