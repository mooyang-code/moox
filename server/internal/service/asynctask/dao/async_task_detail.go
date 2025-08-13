package dao

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/server/internal/service/asynctask/model"
	"gorm.io/gorm"
)

// AsyncTaskDetailDAO 异步任务详情数据访问接口
type AsyncTaskDetailDAO interface {
	CreateAsyncTaskDetail(ctx context.Context, detail *model.AsyncTaskDetail) error
	BatchCreateAsyncTaskDetails(ctx context.Context, details []*model.AsyncTaskDetail) error
	GetAsyncTaskDetail(ctx context.Context, taskID, itemID string) (*model.AsyncTaskDetail, error)
	GetTaskDetails(ctx context.Context, taskID string) ([]*model.AsyncTaskDetail, error)
	GetTaskDetailsByStatus(ctx context.Context, taskID string, status int) ([]*model.AsyncTaskDetail, error)
	UpdateTaskDetail(ctx context.Context, detail *model.AsyncTaskDetail) error
	UpdateTaskDetailStatus(ctx context.Context, taskID, itemID string, status int, errorMessage string) error
	BatchUpdateTaskDetailStatus(ctx context.Context, taskID string, itemIDs []string, status int) error
	DeleteTaskDetails(ctx context.Context, taskID string) error
	CountTaskDetailsByStatus(ctx context.Context, taskID string, status int) (int64, error)
}

type asyncTaskDetailDAOImpl struct {
	db *gorm.DB
}

// NewAsyncTaskDetailDAO 创建新的异步任务详情DAO实例
func NewAsyncTaskDetailDAO(db *gorm.DB) AsyncTaskDetailDAO {
	return &asyncTaskDetailDAOImpl{db: db}
}

// CreateAsyncTaskDetail 创建异步任务详情
func (d *asyncTaskDetailDAOImpl) CreateAsyncTaskDetail(ctx context.Context, detail *model.AsyncTaskDetail) error {
	if err := d.db.WithContext(ctx).Create(detail).Error; err != nil {
		return fmt.Errorf("failed to create async task detail: %w", err)
	}
	return nil
}

// BatchCreateAsyncTaskDetails 批量创建异步任务详情
func (d *asyncTaskDetailDAOImpl) BatchCreateAsyncTaskDetails(ctx context.Context, details []*model.AsyncTaskDetail) error {
	if len(details) == 0 {
		return nil
	}
	
	// 批量插入
	if err := d.db.WithContext(ctx).CreateInBatches(details, 100).Error; err != nil {
		return fmt.Errorf("failed to batch create async task details: %w", err)
	}
	return nil
}

// GetAsyncTaskDetail 根据任务ID和项目ID获取任务详情
func (d *asyncTaskDetailDAOImpl) GetAsyncTaskDetail(ctx context.Context, taskID, itemID string) (*model.AsyncTaskDetail, error) {
	var detail model.AsyncTaskDetail
	if err := d.db.WithContext(ctx).
		Where("c_task_id = ? AND c_item_id = ?", taskID, itemID).
		First(&detail).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get async task detail: %w", err)
	}
	return &detail, nil
}

// GetTaskDetails 获取任务的所有详情
func (d *asyncTaskDetailDAOImpl) GetTaskDetails(ctx context.Context, taskID string) ([]*model.AsyncTaskDetail, error) {
	var details []*model.AsyncTaskDetail
	if err := d.db.WithContext(ctx).
		Where("c_task_id = ?", taskID).
		Order("c_ctime ASC").
		Find(&details).Error; err != nil {
		return nil, fmt.Errorf("failed to get task details: %w", err)
	}
	return details, nil
}

// GetTaskDetailsByStatus 根据状态获取任务详情
func (d *asyncTaskDetailDAOImpl) GetTaskDetailsByStatus(ctx context.Context, taskID string, status int) ([]*model.AsyncTaskDetail, error) {
	var details []*model.AsyncTaskDetail
	if err := d.db.WithContext(ctx).
		Where("c_task_id = ? AND c_status = ?", taskID, status).
		Order("c_ctime ASC").
		Find(&details).Error; err != nil {
		return nil, fmt.Errorf("failed to get task details by status: %w", err)
	}
	return details, nil
}

// UpdateTaskDetail 更新任务详情
func (d *asyncTaskDetailDAOImpl) UpdateTaskDetail(ctx context.Context, detail *model.AsyncTaskDetail) error {
	if err := d.db.WithContext(ctx).Save(detail).Error; err != nil {
		return fmt.Errorf("failed to update task detail: %w", err)
	}
	return nil
}

// UpdateTaskDetailStatus 更新任务详情状态
func (d *asyncTaskDetailDAOImpl) UpdateTaskDetailStatus(ctx context.Context, taskID, itemID string, status int, errorMessage string) error {
	updates := map[string]interface{}{
		"c_status": status,
	}
	if errorMessage != "" {
		updates["c_error_message"] = errorMessage
	}
	
	if err := d.db.WithContext(ctx).
		Model(&model.AsyncTaskDetail{}).
		Where("c_task_id = ? AND c_item_id = ?", taskID, itemID).
		Updates(updates).Error; err != nil {
		return fmt.Errorf("failed to update task detail status: %w", err)
	}
	return nil
}

// BatchUpdateTaskDetailStatus 批量更新任务详情状态
func (d *asyncTaskDetailDAOImpl) BatchUpdateTaskDetailStatus(ctx context.Context, taskID string, itemIDs []string, status int) error {
	if len(itemIDs) == 0 {
		return nil
	}
	
	if err := d.db.WithContext(ctx).
		Model(&model.AsyncTaskDetail{}).
		Where("c_task_id = ? AND c_item_id IN ?", taskID, itemIDs).
		Update("c_status", status).Error; err != nil {
		return fmt.Errorf("failed to batch update task detail status: %w", err)
	}
	return nil
}

// DeleteTaskDetails 删除任务的所有详情
func (d *asyncTaskDetailDAOImpl) DeleteTaskDetails(ctx context.Context, taskID string) error {
	if err := d.db.WithContext(ctx).
		Where("c_task_id = ?", taskID).
		Delete(&model.AsyncTaskDetail{}).Error; err != nil {
		return fmt.Errorf("failed to delete task details: %w", err)
	}
	return nil
}

// CountTaskDetailsByStatus 统计任务详情状态数量
func (d *asyncTaskDetailDAOImpl) CountTaskDetailsByStatus(ctx context.Context, taskID string, status int) (int64, error) {
	var count int64
	query := d.db.WithContext(ctx).Model(&model.AsyncTaskDetail{}).Where("c_task_id = ?", taskID)
	
	if status > 0 {
		query = query.Where("c_status = ?", status)
	}
	
	if err := query.Count(&count).Error; err != nil {
		return 0, fmt.Errorf("failed to count task details by status: %w", err)
	}
	return count, nil
}