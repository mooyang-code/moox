package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collector/model"

	"gorm.io/gorm"
)

// CollectorTaskInstanceDAO 采集任务实例数据访问对象接口
type CollectorTaskInstanceDAO interface {
	// 基础CRUD操作
	CreateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error
	GetTaskInstance(ctx context.Context, instanceID string) (*model.CollectorTaskInstance, error)
	UpdateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error
	DeleteTaskInstance(ctx context.Context, instanceID string) error

	// 查询操作
	GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error)
	GetTaskInstancesByTask(ctx context.Context, taskID string, limit int) ([]*model.CollectorTaskInstance, error)
	GetPendingInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error)
	GetRunningInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error)
	GetRecentInstances(ctx context.Context, hours int) ([]*model.CollectorTaskInstance, error)

	// 状态更新
	UpdateInstanceStatus(ctx context.Context, instanceID string, status int, result string) error
	StartInstance(ctx context.Context, instanceID string) error
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error

	// 批量操作
	BatchCreateInstances(ctx context.Context, instances []*model.CollectorTaskInstance) error
	CleanupOldInstances(ctx context.Context, days int) error
}

type collectorTaskInstanceDaoImpl struct {
	db *gorm.DB
}

// NewCollectorTaskInstanceDAO 创建新的任务实例DAO实例
func NewCollectorTaskInstanceDAO(db *gorm.DB) CollectorTaskInstanceDAO {
	return &collectorTaskInstanceDaoImpl{db: db}
}

// CreateTaskInstance 创建任务实例
func (d *collectorTaskInstanceDaoImpl) CreateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error {
	instance.CreateTime = time.Now()
	instance.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).Create(instance)
	if result.Error != nil {
		return fmt.Errorf("failed to create task instance: %w", result.Error)
	}
	return nil
}

// GetTaskInstance 获取任务实例
func (d *collectorTaskInstanceDaoImpl) GetTaskInstance(ctx context.Context, instanceID string) (*model.CollectorTaskInstance, error) {
	var instance model.CollectorTaskInstance
	result := d.db.WithContext(ctx).
		Where("c_instance_id = ?", instanceID).
		First(&instance)

	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get task instance: %w", result.Error)
	}
	return &instance, nil
}

// UpdateTaskInstance 更新任务实例
func (d *collectorTaskInstanceDaoImpl) UpdateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error {
	instance.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_instance_id = ?", instance.InstanceID).
		Updates(map[string]interface{}{
			"c_target_objects":   instance.TargetObjects,
			"c_execution_params": instance.ExecutionParams,
			"c_status":           instance.Status,
			"c_start_time":       instance.StartTime,
			"c_end_time":         instance.EndTime,
			"c_result":           instance.Result,
			"c_project_id":       instance.ProjectID,
			"c_dataset_id":       instance.DatasetID,
			"c_mtime":            instance.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update task instance: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task instance not found")
	}
	return nil
}

// DeleteTaskInstance 删除任务实例
func (d *collectorTaskInstanceDaoImpl) DeleteTaskInstance(ctx context.Context, instanceID string) error {
	result := d.db.WithContext(ctx).
		Where("c_instance_id = ?", instanceID).
		Delete(&model.CollectorTaskInstance{})

	if result.Error != nil {
		return fmt.Errorf("failed to delete task instance: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task instance not found")
	}
	return nil
}

// GetTaskInstancesByNode 获取节点的任务实例
func (d *collectorTaskInstanceDaoImpl) GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).Where("c_node_id = ?", nodeID)

	if len(status) > 0 {
		query = query.Where("c_status IN ?", status)
	}

	result := query.Order("c_ctime DESC").Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task instances by node: %w", result.Error)
	}
	return instances, nil
}

// GetTaskInstancesByTask 获取任务的实例历史
func (d *collectorTaskInstanceDaoImpl) GetTaskInstancesByTask(ctx context.Context, taskID string, limit int) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).
		Where("c_task_id = ?", taskID).
		Order("c_ctime DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	result := query.Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task instances by task: %w", result.Error)
	}
	return instances, nil
}

// GetPendingInstances 获取待执行的实例
func (d *collectorTaskInstanceDaoImpl) GetPendingInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).Where("c_status = ?", model.TaskInstanceStatusPending)

	if nodeID != "" {
		query = query.Where("c_node_id = ?", nodeID)
	}

	result := query.Order("c_ctime ASC").Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get pending instances: %w", result.Error)
	}
	return instances, nil
}

// GetRunningInstances 获取正在执行的实例
func (d *collectorTaskInstanceDaoImpl) GetRunningInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).Where("c_status = ?", model.TaskInstanceStatusRunning)

	if nodeID != "" {
		query = query.Where("c_node_id = ?", nodeID)
	}

	result := query.Order("c_start_time ASC").Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get running instances: %w", result.Error)
	}
	return instances, nil
}

// GetRecentInstances 获取最近的任务实例
func (d *collectorTaskInstanceDaoImpl) GetRecentInstances(ctx context.Context, hours int) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance
	cutoffTime := time.Now().Add(-time.Duration(hours) * time.Hour)

	result := d.db.WithContext(ctx).
		Where("c_ctime > ?", cutoffTime).
		Order("c_ctime DESC").
		Find(&instances)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get recent instances: %w", result.Error)
	}
	return instances, nil
}

// UpdateInstanceStatus 更新实例状态
func (d *collectorTaskInstanceDaoImpl) UpdateInstanceStatus(ctx context.Context, instanceID string, status int, result string) error {
	updates := map[string]interface{}{
		"c_status": status,
		"c_mtime":  time.Now(),
	}

	if result != "" {
		updates["c_result"] = result
	}

	dbResult := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_instance_id = ?", instanceID).
		Updates(updates)

	if dbResult.Error != nil {
		return fmt.Errorf("failed to update instance status: %w", dbResult.Error)
	}

	if dbResult.RowsAffected == 0 {
		return fmt.Errorf("instance not found")
	}
	return nil
}

// StartInstance 开始执行实例
func (d *collectorTaskInstanceDaoImpl) StartInstance(ctx context.Context, instanceID string) error {
	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_instance_id = ? AND c_status = ?", instanceID, model.TaskInstanceStatusPending).
		Updates(map[string]interface{}{
			"c_status":     model.TaskInstanceStatusRunning,
			"c_start_time": now,
			"c_mtime":      now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to start instance: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("instance not found or not in pending status")
	}
	return nil
}

// CompleteInstance 完成实例执行
func (d *collectorTaskInstanceDaoImpl) CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error {
	now := time.Now()
	status := model.TaskInstanceStatusSuccess
	if !success {
		status = model.TaskInstanceStatusFailed
	}

	updates := map[string]interface{}{
		"c_status":   status,
		"c_end_time": now,
		"c_result":   result,
		"c_mtime":    now,
	}

	dbResult := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_instance_id = ? AND c_status = ?", instanceID, model.TaskInstanceStatusRunning).
		Updates(updates)

	if dbResult.Error != nil {
		return fmt.Errorf("failed to complete instance: %w", dbResult.Error)
	}

	if dbResult.RowsAffected == 0 {
		return fmt.Errorf("instance not found or not in running status")
	}
	return nil
}

// BatchCreateInstances 批量创建实例
func (d *collectorTaskInstanceDaoImpl) BatchCreateInstances(ctx context.Context, instances []*model.CollectorTaskInstance) error {
	if len(instances) == 0 {
		return nil
	}

	now := time.Now()
	for _, instance := range instances {
		instance.CreateTime = now
		instance.ModifyTime = now
	}

	result := d.db.WithContext(ctx).CreateInBatches(instances, 100)
	if result.Error != nil {
		return fmt.Errorf("failed to batch create instances: %w", result.Error)
	}
	return nil
}

// CleanupOldInstances 清理旧的实例记录
func (d *collectorTaskInstanceDaoImpl) CleanupOldInstances(ctx context.Context, days int) error {
	cutoffTime := time.Now().AddDate(0, 0, -days)

	result := d.db.WithContext(ctx).
		Where("c_ctime < ? AND c_status IN ?", cutoffTime,
			[]int{model.TaskInstanceStatusSuccess, model.TaskInstanceStatusFailed, model.TaskInstanceStatusTimeout}).
		Delete(&model.CollectorTaskInstance{})

	if result.Error != nil {
		return fmt.Errorf("failed to cleanup old instances: %w", result.Error)
	}
	return nil
}
