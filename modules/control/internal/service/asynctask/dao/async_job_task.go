package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AsyncJobTaskDAO 异步Task数据访问对象接口
type AsyncJobTaskDAO interface {
	// ========== Task创建操作 ==========

	// CreateAsyncJobTask 创建单个异步Task
	CreateAsyncJobTask(ctx context.Context, task *model.AsyncJobTask) error

	// BatchCreateAsyncJobTasks 批量创建异步Task
	BatchCreateAsyncJobTasks(ctx context.Context, tasks []*model.AsyncJobTask) error

	// ========== Task查询操作 ==========

	// GetAsyncJobTask 根据TaskID获取Task
	GetAsyncJobTask(ctx context.Context, taskID string) (*model.AsyncJobTask, error)

	// GetTasksByJobID 根据JobID获取所有Task
	GetTasksByJobID(ctx context.Context, jobID string) ([]*model.AsyncJobTask, error)

	// GetTasksByJobIDAndStatus 根据JobID和状态获取Task列表
	GetTasksByJobIDAndStatus(ctx context.Context, jobID string, status int) ([]*model.AsyncJobTask, error)

	// ========== Task状态更新 ==========

	// UpdateTaskStatus 更新Task状态
	UpdateTaskStatus(ctx context.Context, taskID string, status int, resultData, errorMessage string) error

	// SetTaskStarted 设置Task为处理中状态
	SetTaskStarted(ctx context.Context, taskID string) error

	// SetTaskCompleted 设置Task为完成状态
	SetTaskCompleted(ctx context.Context, taskID string, status int, resultData, errorMessage string) error
}

type asyncJobTaskDAOImpl struct {
	db *gorm.DB
}

// NewAsyncJobTaskDAO 创建AsyncJobTaskDAO实例
func NewAsyncJobTaskDAO(db *gorm.DB) AsyncJobTaskDAO {
	return &asyncJobTaskDAOImpl{db: db}
}

// CreateAsyncJobTask 创建单个异步Task
func (d *asyncJobTaskDAOImpl) CreateAsyncJobTask(ctx context.Context, task *model.AsyncJobTask) error {
	return d.db.WithContext(ctx).Create(task).Error
}

// BatchCreateAsyncJobTasks 批量创建异步Task
func (d *asyncJobTaskDAOImpl) BatchCreateAsyncJobTasks(ctx context.Context, tasks []*model.AsyncJobTask) error {
	if len(tasks) == 0 {
		return nil
	}
	// 使用批量插入，提高性能
	return d.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(tasks, 100).Error
}

// GetAsyncJobTask 根据TaskID获取Task
func (d *asyncJobTaskDAOImpl) GetAsyncJobTask(ctx context.Context, taskID string) (*model.AsyncJobTask, error) {
	var task model.AsyncJobTask
	err := d.db.WithContext(ctx).
		Where("c_task_id = ?", taskID).
		First(&task).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &task, nil
}

// GetTasksByJobID 根据JobID获取所有Task
func (d *asyncJobTaskDAOImpl) GetTasksByJobID(ctx context.Context, jobID string) ([]*model.AsyncJobTask, error) {
	var tasks []*model.AsyncJobTask
	err := d.db.WithContext(ctx).
		Where("c_job_id = ?", jobID).
		Order("c_ctime ASC").
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// GetTasksByJobIDAndStatus 根据JobID和状态获取Task列表
func (d *asyncJobTaskDAOImpl) GetTasksByJobIDAndStatus(ctx context.Context, jobID string, status int) ([]*model.AsyncJobTask, error) {
	var tasks []*model.AsyncJobTask
	err := d.db.WithContext(ctx).
		Where("c_job_id = ? AND c_task_status = ?", jobID, status).
		Order("c_ctime ASC").
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}

// UpdateTaskStatus 更新Task状态
func (d *asyncJobTaskDAOImpl) UpdateTaskStatus(ctx context.Context, taskID string, status int, resultData, errorMessage string) error {
	updates := map[string]interface{}{
		"c_task_status": status,
	}
	if resultData != "" {
		updates["c_result_data"] = resultData
	}
	if errorMessage != "" {
		updates["c_error_message"] = errorMessage
	}

	return d.db.WithContext(ctx).
		Model(&model.AsyncJobTask{}).
		Where("c_task_id = ?", taskID).
		Updates(updates).Error
}

// SetTaskStarted 设置Task为处理中状态
func (d *asyncJobTaskDAOImpl) SetTaskStarted(ctx context.Context, taskID string) error {
	now := time.Now()
	return d.db.WithContext(ctx).
		Model(&model.AsyncJobTask{}).
		Where("c_task_id = ?", taskID).
		Updates(map[string]interface{}{
			"c_task_status":  1, // TaskStatusProcessing
			"c_started_time": now,
		}).Error
}

// SetTaskCompleted 设置Task为完成状态
func (d *asyncJobTaskDAOImpl) SetTaskCompleted(ctx context.Context, taskID string, status int, resultData, errorMessage string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"c_task_status":    status,
		"c_completed_time": now,
	}
	if resultData != "" {
		updates["c_result_data"] = resultData
	}
	if errorMessage != "" {
		updates["c_error_message"] = errorMessage
	}

	return d.db.WithContext(ctx).
		Model(&model.AsyncJobTask{}).
		Where("c_task_id = ?", taskID).
		Updates(updates).Error
}

// CountTasksByJobIDAndStatus 统计指定JobID和状态的Task数量（辅助方法）
func (d *asyncJobTaskDAOImpl) CountTasksByJobIDAndStatus(ctx context.Context, jobID string, status int) (int64, error) {
	var count int64
	err := d.db.WithContext(ctx).
		Model(&model.AsyncJobTask{}).
		Where("c_job_id = ? AND c_task_status = ?", jobID, status).
		Count(&count).Error
	if err != nil {
		return 0, err
	}
	return count, nil
}

// GetFailedTasksByJobID 获取Job下所有失败的Task（辅助方法）
func (d *asyncJobTaskDAOImpl) GetFailedTasksByJobID(ctx context.Context, jobID string) ([]*model.AsyncJobTask, error) {
	return d.GetTasksByJobIDAndStatus(ctx, jobID, 3) // TaskStatusFailed
}

// DeleteTasksByJobID 删除Job下的所有Task（清理方法）
func (d *asyncJobTaskDAOImpl) DeleteTasksByJobID(ctx context.Context, jobID string) error {
	return d.db.WithContext(ctx).
		Where("c_job_id = ?", jobID).
		Delete(&model.AsyncJobTask{}).Error
}

// GetTasksByPrefix 根据TaskID前缀获取Task列表（用于查询）
func (d *asyncJobTaskDAOImpl) GetTasksByPrefix(ctx context.Context, prefix string) ([]*model.AsyncJobTask, error) {
	var tasks []*model.AsyncJobTask
	err := d.db.WithContext(ctx).
		Where("c_task_id LIKE ?", fmt.Sprintf("%s%%", prefix)).
		Order("c_ctime DESC").
		Find(&tasks).Error
	if err != nil {
		return nil, err
	}
	return tasks, nil
}
