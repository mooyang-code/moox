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
	Name            string    `gorm:"column:c_name"`
	SourceDatasetID string    `gorm:"column:c_source_dataset_id"`
	Freq            string    `gorm:"column:c_freq"`
	ResultDatasetID string    `gorm:"column:c_result_dataset_id"`
	BoundInstanceID string    `gorm:"column:c_bound_instance_id"`
	Status          string    `gorm:"column:c_status"`
	CreatedAt       time.Time `gorm:"column:c_ctime"`
	UpdatedAt       time.Time `gorm:"column:c_mtime"`
}

func (r factorTaskRow) domain() (domain.FactorTask, error) {
	return domain.FactorTask{
		TaskID: r.TaskID, SpaceID: r.SpaceID, Name: r.Name, SourceDatasetID: r.SourceDatasetID, Freq: r.Freq,
		ResultDatasetID: r.ResultDatasetID, BoundInstanceID: r.BoundInstanceID, Status: r.Status,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}, nil
}

// CreateTask 创建一个因子任务。任务 ID、结果数据集或（空间内的）名称重复按 ErrConflict 返回。
// 新任务没有关联实例：BoundInstanceID 只能由 BindTask 写入。
func (s *Store) CreateTask(ctx context.Context, task domain.FactorTask) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	task.TaskID = strings.TrimSpace(task.TaskID)
	task.SpaceID = strings.TrimSpace(task.SpaceID)
	task.Name = strings.TrimSpace(task.Name)
	task.SourceDatasetID = strings.TrimSpace(task.SourceDatasetID)
	task.Freq = strings.TrimSpace(task.Freq)
	task.ResultDatasetID = strings.TrimSpace(task.ResultDatasetID)
	if task.TaskID == "" || task.SpaceID == "" || task.Name == "" || task.SourceDatasetID == "" || task.Freq == "" || task.ResultDatasetID == "" {
		return fmt.Errorf("factor task identity is incomplete")
	}
	if task.Status == "" {
		task.Status = domain.TaskStatusPending
	}
	now := time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = now
	}
	task.UpdatedAt = now
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO t_factor_tasks
		(c_task_id, c_space_id, c_name, c_source_dataset_id, c_freq, c_result_dataset_id, c_status, c_ctime, c_mtime)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, task.TaskID, task.SpaceID, task.Name, task.SourceDatasetID, task.Freq, task.ResultDatasetID, task.Status, task.CreatedAt, task.UpdatedAt)
	if result.Error != nil {
		if isUniqueConstraint(result.Error) {
			return fmt.Errorf("%w: factor task %q or its name %q already exists", ErrConflict, task.TaskID, task.Name)
		}
		return result.Error
	}
	return nil
}

// BindTask 把任务与策略实例永久关联：同一个实例重复调用直接成功；任务已关联别的实例、或该实例已关联别的任务时
// 返回 ErrConflict。关联写入后不能修改或清空。
func (s *Store) BindTask(ctx context.Context, taskID, instanceID string) (domain.FactorTask, error) {
	if s == nil || s.db == nil {
		return domain.FactorTask{}, fmt.Errorf("factor database is not open")
	}
	taskID, instanceID = strings.TrimSpace(taskID), strings.TrimSpace(instanceID)
	if taskID == "" || instanceID == "" {
		return domain.FactorTask{}, fmt.Errorf("task id and instance id are required")
	}
	task, err := s.GetTask(ctx, taskID)
	if err != nil {
		return domain.FactorTask{}, err
	}
	if task.BoundInstanceID == instanceID {
		return task, nil
	}
	if task.BoundInstanceID != "" {
		return domain.FactorTask{}, fmt.Errorf("%w: factor task %q is already bound to another strategy instance", ErrConflict, taskID)
	}
	result := s.db.WithContext(ctx).Exec(
		"UPDATE t_factor_tasks SET c_bound_instance_id = ? WHERE c_task_id = ? AND c_bound_instance_id = ''", instanceID, taskID)
	if result.Error != nil {
		if isUniqueConstraint(result.Error) {
			return domain.FactorTask{}, fmt.Errorf("%w: strategy instance %q is already bound to another factor task", ErrConflict, instanceID)
		}
		return domain.FactorTask{}, result.Error
	}
	if result.RowsAffected == 0 {
		return domain.FactorTask{}, fmt.Errorf("%w: factor task %q was bound concurrently", ErrConflict, taskID)
	}
	return s.GetTask(ctx, taskID)
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
	tasks := make([]domain.FactorTask, 0, len(rows))
	for _, row := range rows {
		task, err := row.domain()
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

// DeleteTask 删除没有成员、也没有关联实例的因子任务。SQLite 的外键在成员还引用它时拒绝删除，按 ErrConflict 返回。
func (s *Store) DeleteTask(ctx context.Context, taskID string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("factor database is not open")
	}
	taskID = strings.TrimSpace(taskID)
	result := s.db.WithContext(ctx).Exec("DELETE FROM t_factor_tasks WHERE c_task_id = ? AND c_bound_instance_id = ''", taskID)
	if result.Error != nil {
		if isForeignKeyConstraint(result.Error) {
			return fmt.Errorf("%w: factor task %q still has members", ErrConflict, taskID)
		}
		return result.Error
	}
	if result.RowsAffected > 0 {
		return nil
	}
	if _, err := s.GetTask(ctx, taskID); err != nil {
		return err
	}
	return fmt.Errorf("%w: factor task %q is bound to a strategy instance", ErrConflict, taskID)
}

const setColumns = `c_task_id, c_space_id, c_name, c_source_dataset_id, c_freq, c_result_dataset_id, c_bound_instance_id, c_status, c_ctime, c_mtime`

// GetTaskByInstance 返回策略实例关联的因子任务；没有关联时返回 gorm.ErrRecordNotFound。
func (s *Store) GetTaskByInstance(ctx context.Context, instanceID string) (domain.FactorTask, error) {
	if s == nil || s.db == nil {
		return domain.FactorTask{}, fmt.Errorf("factor database is not open")
	}
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" {
		return domain.FactorTask{}, fmt.Errorf("instance id is required")
	}
	var row factorTaskRow
	result := s.db.WithContext(ctx).Raw(`SELECT `+setColumns+` FROM t_factor_tasks WHERE c_bound_instance_id = ?`, instanceID).Scan(&row)
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
	case domain.TaskStatusPending, domain.TaskStatusEnabled, domain.TaskStatusDisabled:
		return true
	default:
		return false
	}
}
