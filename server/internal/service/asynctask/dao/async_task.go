package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"gorm.io/gorm"
)

// AsyncTaskDAO 异步任务数据访问接口
type AsyncTaskDAO interface {
	CreateAsyncTask(ctx context.Context, task *model.AsyncTask) error
	GetAsyncTask(ctx context.Context, taskID string) (*model.AsyncTask, error)
	UpdateAsyncTask(ctx context.Context, task *model.AsyncTask) error
	UpdateTaskStatus(ctx context.Context, taskID string, status int, errorMessage string) error
	UpdateTaskProgress(ctx context.Context, taskID string, successCount, failedCount int) error
	SetTaskStarted(ctx context.Context, taskID string) error
	SetTaskCompleted(ctx context.Context, taskID string, status int, resultData, errorMessage string) error
	ListAsyncTasks(ctx context.Context, taskType string, status int, limit, offset int) ([]*model.AsyncTask, error)
	CountAsyncTasks(ctx context.Context, taskType string, status int) (int64, error)
}

type asyncTaskDAOImpl struct {
	db *gorm.DB
}

// NewAsyncTaskDAO 创建新的异步任务DAO实例
func NewAsyncTaskDAO(db *gorm.DB) AsyncTaskDAO {
	return &asyncTaskDAOImpl{db: db}
}

// CreateAsyncTask 创建异步任务
func (d *asyncTaskDAOImpl) CreateAsyncTask(ctx context.Context, task *model.AsyncTask) error {
	if err := d.db.WithContext(ctx).Create(task).Error; err != nil {
		return fmt.Errorf("failed to create async task: %w", err)
	}
	return nil
}

// GetAsyncTask 根据任务ID获取异步任务
func (d *asyncTaskDAOImpl) GetAsyncTask(ctx context.Context, taskID string) (*model.AsyncTask, error) {
	var task model.AsyncTask
	if err := d.db.WithContext(ctx).Where("c_task_id = ?", taskID).First(&task).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get async task: %w", err)
	}
	return &task, nil
}

// UpdateAsyncTask 更新异步任务
func (d *asyncTaskDAOImpl) UpdateAsyncTask(ctx context.Context, task *model.AsyncTask) error {
	if err := d.db.WithContext(ctx).Save(task).Error; err != nil {
		return fmt.Errorf("failed to update async task: %w", err)
	}
	return nil
}

// UpdateTaskStatus 更新任务状态
func (d *asyncTaskDAOImpl) UpdateTaskStatus(ctx context.Context, taskID string, status int, errorMessage string) error {
	updates := map[string]interface{}{
		"c_task_status": status,
	}
	if errorMessage != "" {
		updates["c_error_message"] = errorMessage
	}
	
	if err := d.db.WithContext(ctx).
		Model(&model.AsyncTask{}).
		Where("c_task_id = ?", taskID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to update task status: %w", err)
	}
	return nil
}

// UpdateTaskProgress 更新任务进度
func (d *asyncTaskDAOImpl) UpdateTaskProgress(ctx context.Context, taskID string, successCount, failedCount int) error {
	if err := d.db.WithContext(ctx).
		Model(&model.AsyncTask{}).
		Where("c_task_id = ?", taskID).
		Updates(map[string]interface{}{
			"c_success_count": successCount,
			"c_failed_count":  failedCount,
		}).Error; err != nil {
		return fmt.Errorf("failed to update task progress: %w", err)
	}
	return nil
}

// SetTaskStarted 设置任务开始执行
func (d *asyncTaskDAOImpl) SetTaskStarted(ctx context.Context, taskID string) error {
	now := time.Now()
	if err := d.db.WithContext(ctx).
		Model(&model.AsyncTask{}).
		Where("c_task_id = ?", taskID).
		Updates(map[string]interface{}{
			"c_started_time": now,
			"c_task_status":  model.TaskStatusProcessing,
		}).Error; err != nil {
		return fmt.Errorf("failed to set task started: %w", err)
	}
	return nil
}

// SetTaskCompleted 设置任务完成
func (d *asyncTaskDAOImpl) SetTaskCompleted(ctx context.Context, taskID string, status int, resultData, errorMessage string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"c_completed_time": now,
		"c_task_status":    status,
	}
	if resultData != "" {
		updates["c_result_data"] = resultData
	}
	if errorMessage != "" {
		updates["c_error_message"] = errorMessage
	}
	
	if err := d.db.WithContext(ctx).
		Model(&model.AsyncTask{}).
		Where("c_task_id = ?", taskID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to set task completed: %w", err)
	}
	return nil
}

// ListAsyncTasks 列出异步任务
func (d *asyncTaskDAOImpl) ListAsyncTasks(ctx context.Context, taskType string, status int, limit, offset int) ([]*model.AsyncTask, error) {
	var tasks []*model.AsyncTask
	query := d.db.WithContext(ctx)
	
	if taskType != "" {
		query = query.Where("c_task_type = ?", taskType)
	}
	if status > 0 {
		query = query.Where("c_task_status = ?", status)
	}
	
	if err := query.Order("c_ctime DESC").Limit(limit).Offset(offset).Find(&tasks).Error; err != nil {
		return nil, fmt.Errorf("failed to list async tasks: %w", err)
	}
	return tasks, nil
}

// CountAsyncTasks 统计异步任务数量
func (d *asyncTaskDAOImpl) CountAsyncTasks(ctx context.Context, taskType string, status int) (int64, error) {
	var count int64
	query := d.db.WithContext(ctx).Model(&model.AsyncTask{})
	
	if taskType != "" {
		query = query.Where("c_task_type = ?", taskType)
	}
	if status > 0 {
		query = query.Where("c_task_status = ?", status)
	}
	
	if err := query.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("failed to count async tasks: %w", err)
	}
	return count, nil
}