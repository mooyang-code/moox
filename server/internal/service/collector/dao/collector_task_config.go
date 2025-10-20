package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collector/model"

	"gorm.io/gorm"
)

// CollectorTaskConfigDAO 采集任务配置数据访问对象接口
type CollectorTaskConfigDAO interface {
	// ========== 基础CRUD操作 ==========
	
	// GetTaskConfigList 获取任务配置列表
	GetTaskConfigList(ctx context.Context, projectID, datasetID string) ([]*model.CollectorTaskConfig, error)
	
	// GetTaskConfig 获取单个任务配置
	GetTaskConfig(ctx context.Context, taskID string) (*model.CollectorTaskConfig, error)
	
	// CreateTaskConfig 创建任务配置
	CreateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error
	
	// UpdateTaskConfig 更新任务配置
	UpdateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error
	
	// DeleteTaskConfig 删除任务配置
	DeleteTaskConfig(ctx context.Context, taskID string) error
	
	// ========== 查询操作 ==========
	
	// GetEnabledTaskConfigs 获取所有启用的任务配置
	GetEnabledTaskConfigs(ctx context.Context) ([]*model.CollectorTaskConfig, error)
	
	// GetTaskConfigsByType 根据类型获取任务配置
	GetTaskConfigsByType(ctx context.Context, taskType string) ([]*model.CollectorTaskConfig, error)
	
	// GetTaskConfigsByNode 根据节点获取任务配置
	GetTaskConfigsByNode(ctx context.Context, nodeID string) ([]*model.CollectorTaskConfig, error)
	
	// GetTaskConfigsByCollectorType 根据采集器类型获取任务配置
	GetTaskConfigsByCollectorType(ctx context.Context, collectorType string) ([]*model.CollectorTaskConfig, error)
	
	// ========== 批量操作 ==========
	
	// BatchUpdateEnabled 批量更新启用状态
	BatchUpdateEnabled(ctx context.Context, taskIDs []string, enabled string) error
	
	// UpdateDispatchResult 更新分发结果
	UpdateDispatchResult(ctx context.Context, taskID string, result string) error
}

type collectorTaskConfigDaoImpl struct {
	db *gorm.DB
}

// NewCollectorTaskConfigDAO 创建新的任务配置DAO实例
func NewCollectorTaskConfigDAO(db *gorm.DB) CollectorTaskConfigDAO {
	return &collectorTaskConfigDaoImpl{db: db}
}

// GetTaskConfigList 获取指定项目和数据集的任务配置列表
func (d *collectorTaskConfigDaoImpl) GetTaskConfigList(ctx context.Context, projectID, datasetID string) ([]*model.CollectorTaskConfig, error) {
	var configs []*model.CollectorTaskConfig
	query := d.db.WithContext(ctx).Where("c_invalid = ?", 0)

	if projectID != "" {
		query = query.Where("c_project_id = ?", projectID)
	}
	if datasetID != "" {
		query = query.Where("c_dataset_id = ?", datasetID)
	}
	result := query.Order("c_priority DESC, c_mtime DESC").Find(&configs)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task config list: %w", result.Error)
	}
	return configs, nil
}

// GetTaskConfig 根据任务ID获取单个配置
func (d *collectorTaskConfigDaoImpl) GetTaskConfig(ctx context.Context, taskID string) (*model.CollectorTaskConfig, error) {
	var config model.CollectorTaskConfig
	result := d.db.WithContext(ctx).
		Where("c_task_id = ? AND c_invalid = ?", taskID, 0).
		First(&config)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get task config: %w", result.Error)
	}
	return &config, nil
}

// CreateTaskConfig 创建新的任务配置
func (d *collectorTaskConfigDaoImpl) CreateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error {
	config.CreateTime = time.Now()
	config.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).Create(config)
	if result.Error != nil {
		return fmt.Errorf("failed to create task config: %w", result.Error)
	}
	return nil
}

// UpdateTaskConfig 更新任务配置
func (d *collectorTaskConfigDaoImpl) UpdateTaskConfig(ctx context.Context, config *model.CollectorTaskConfig) error {
	config.ModifyTime = time.Now()

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskConfig{}).
		Where("c_task_id = ? AND c_invalid = ?", config.TaskID, 0).
		Updates(map[string]interface{}{
			"c_project_id":            config.ProjectID,
			"c_dataset_id":            config.DatasetID,
			"c_task_type":             config.TaskType,
			"c_collector_type":        config.CollectorType,
			"c_source_name":           config.SourceName,
			"c_assignment_type":       config.AssignmentType,
			"c_assigned_nodes":        config.AssignedNodes,
			"c_node_pattern":          config.NodePattern,
			"c_load_balance_strategy": config.LoadBalanceStrategy,
			"c_target_objects":        config.TargetObjects,
			"c_object_pattern":        config.ObjectPattern,
			"c_force_objects":         config.ForceObjects,
			"c_collect_params":        config.CollectParams,
			"c_schedule_config":       config.ScheduleConfig,
			"c_enabled":               config.Enabled,
			"c_priority":              config.Priority,
			"c_mtime":                 config.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update task config: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task config not found or already deleted")
	}
	return nil
}

// DeleteTaskConfig 软删除任务配置
func (d *collectorTaskConfigDaoImpl) DeleteTaskConfig(ctx context.Context, taskID string) error {
	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskConfig{}).
		Where("c_task_id = ? AND c_invalid = ?", taskID, 0).
		Updates(map[string]interface{}{
			"c_invalid": 1,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to delete task config: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task config not found or already deleted")
	}
	return nil
}

// GetEnabledTaskConfigs 获取所有启用的任务配置
func (d *collectorTaskConfigDaoImpl) GetEnabledTaskConfigs(ctx context.Context) ([]*model.CollectorTaskConfig, error) {
	var configs []*model.CollectorTaskConfig
	result := d.db.WithContext(ctx).
		Where("c_enabled = ? AND c_invalid = ?", model.EnabledTrue, 0).
		Order("c_priority DESC, c_mtime DESC").
		Find(&configs)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get enabled task configs: %w", result.Error)
	}
	return configs, nil
}

// GetTaskConfigsByType 根据任务类型获取配置列表
func (d *collectorTaskConfigDaoImpl) GetTaskConfigsByType(ctx context.Context, taskType string) ([]*model.CollectorTaskConfig, error) {
	var configs []*model.CollectorTaskConfig
	result := d.db.WithContext(ctx).
		Where("c_task_type = ? AND c_invalid = ?", taskType, 0).
		Order("c_priority DESC, c_mtime DESC").
		Find(&configs)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task configs by type: %w", result.Error)
	}
	return configs, nil
}

// GetTaskConfigsByNode 获取指定节点的任务配置
func (d *collectorTaskConfigDaoImpl) GetTaskConfigsByNode(ctx context.Context, nodeID string) ([]*model.CollectorTaskConfig, error) {
	var configs []*model.CollectorTaskConfig

	// 查询固定分配给该节点的任务
	result := d.db.WithContext(ctx).
		Where("c_assignment_type = ? AND c_assigned_nodes LIKE ? AND c_invalid = ?",
			model.AssignmentTypeFixed, "%\""+nodeID+"\"%", 0).
		Order("c_priority DESC, c_mtime DESC").
		Find(&configs)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task configs by node: %w", result.Error)
	}
	return configs, nil
}

// GetTaskConfigsByCollectorType 根据采集器类型获取配置
func (d *collectorTaskConfigDaoImpl) GetTaskConfigsByCollectorType(ctx context.Context, collectorType string) ([]*model.CollectorTaskConfig, error) {
	var configs []*model.CollectorTaskConfig
	result := d.db.WithContext(ctx).
		Where("c_collector_type = ? AND c_invalid = ?", collectorType, 0).
		Order("c_priority DESC, c_mtime DESC").
		Find(&configs)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task configs by collector type: %w", result.Error)
	}
	return configs, nil
}

// BatchUpdateEnabled 批量更新启用状态
func (d *collectorTaskConfigDaoImpl) BatchUpdateEnabled(ctx context.Context, taskIDs []string, enabled string) error {
	if len(taskIDs) == 0 {
		return nil
	}

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskConfig{}).
		Where("c_task_id IN ? AND c_invalid = ?", taskIDs, 0).
		Updates(map[string]interface{}{
			"c_enabled": enabled,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to batch update enabled status: %w", result.Error)
	}
	return nil
}

// UpdateDispatchResult 更新任务分发结果
func (d *collectorTaskConfigDaoImpl) UpdateDispatchResult(ctx context.Context, taskID string, result string) error {
	now := time.Now()
	updates := d.db.WithContext(ctx).
		Model(&model.CollectorTaskConfig{}).
		Where("c_task_id = ? AND c_invalid = ?", taskID, 0).
		Updates(map[string]interface{}{
			"c_last_dispatch_time":   now,
			"c_last_dispatch_result": result,
			"c_mtime":                now,
		})

	if updates.Error != nil {
		return fmt.Errorf("failed to update dispatch result: %w", updates.Error)
	}
	return nil
}
