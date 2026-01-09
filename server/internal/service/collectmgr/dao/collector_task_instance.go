package dao

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collectmgr/model"

	"gorm.io/gorm"
)

// CollectorTaskInstanceDAO 采集任务实例数据访问对象接口
type CollectorTaskInstanceDAO interface {
	// ========== 基础CRUD操作 ==========

	// CreateTaskInstance 创建任务实例
	CreateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error

	// GetTaskInstance 获取任务实例
	GetTaskInstance(ctx context.Context, instanceID string) (*model.CollectorTaskInstance, error)

	// UpdateTaskInstance 更新任务实例
	UpdateTaskInstance(ctx context.Context, instance *model.CollectorTaskInstance) error

	// DeleteTaskInstance 删除任务实例
	DeleteTaskInstance(ctx context.Context, instanceID string) error

	// ========== 查询操作 ==========

	// GetTaskInstancesByNode 根据节点获取任务实例
	GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error)

	// GetTaskInstancesByRule 根据规则获取实例列表
	GetTaskInstancesByRule(ctx context.Context, ruleID string, limit int) ([]*model.CollectorTaskInstance, error)

	// GetTaskInstancesByNodeAndStatus 根据节点和状态获取实例
	GetTaskInstancesByNodeAndStatus(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error)

	// GetPendingInstances 获取待执行的实例
	GetPendingInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error)

	// GetRunningInstances 获取正在执行的实例
	GetRunningInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error)

	// GetRecentInstances 获取最近的任务实例
	GetRecentInstances(ctx context.Context, hours int) ([]*model.CollectorTaskInstance, error)

	// GetTaskInstancesByStatus 根据状态获取任务实例
	GetTaskInstancesByStatus(ctx context.Context, status []int) ([]*model.CollectorTaskInstance, error)

	// ========== 分页查询 ==========

	// ListInstancesWithPagination 分页查询任务实例
	// nodeID: 可选，按节点筛选
	// ruleID: 可选，按规则筛选
	// page: 页码（从1开始）
	// size: 每页数量
	// 返回: 实例列表、总数、错误
	ListInstancesWithPagination(ctx context.Context, nodeID, ruleID string, page, size int) ([]*model.CollectorTaskInstance, int64, error)

	// ListInstancesWithFilter 带筛选条件的分页查询任务实例
	ListInstancesWithFilter(ctx context.Context, filter *InstanceFilter) ([]*model.CollectorTaskInstance, int64, error)

	// ========== 状态更新 ==========

	// UpdateInstanceStatus 更新实例状态
	UpdateInstanceStatus(ctx context.Context, instanceID string, status int, result string) error

	// StartInstance 开始执行实例
	StartInstance(ctx context.Context, instanceID string) error

	// CompleteInstance 完成实例执行
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error

	// ReportInstanceStatus 上报实例状态（客户端上报用，无状态前置条件限制）
	// v2.0: 新增 nodeID 参数，更新 c_last_exec_node、c_last_exec_status、c_last_exec_time、c_result
	ReportInstanceStatus(ctx context.Context, instanceID string, nodeID string, status int, result string) error

	// ========== 批量操作 ==========

	// BatchCreateInstances 批量创建实例
	BatchCreateInstances(ctx context.Context, instances []*model.CollectorTaskInstance) error

	// BatchUpdateStatus 批量更新实例状态
	BatchUpdateStatus(ctx context.Context, instanceIDs []string, status int, result string) error

	// CleanupOldInstances 清理旧的实例记录
	CleanupOldInstances(ctx context.Context, days int) error

	// GetInstanceStatistics 获取实例统计信息
	GetInstanceStatistics(ctx context.Context, ruleID string) (map[string]interface{}, error)

	// ========== 任务规划器相关 ==========

	// GetActiveInstancesByRule 获取规则的有效实例（用于增量更新）
	// 只返回 c_invalid = 0 的实例
	GetActiveInstancesByRule(ctx context.Context, ruleID string) ([]*model.CollectorTaskInstance, error)

	// InvalidateInstancesByRule 批量标记规则实例为无效
	// 将 c_invalid 设置为 1
	InvalidateInstancesByRule(ctx context.Context, ruleID string) error

	// BatchUpdateParams 批量更新实例参数
	BatchUpdateParams(ctx context.Context, updates []*InstanceParamUpdate) error

	// BatchInvalidate 批量使实例失效
	BatchInvalidate(ctx context.Context, taskIDs []string) error

	// ========== 任务转移相关 ==========

	// FindAllSuccessNodesByRule 查找同规则下所有执行成功的节点
	// 返回所有成功节点列表，用于负载均衡分配
	FindAllSuccessNodesByRule(ctx context.Context, ruleID string, excludeNodeID string) ([]string, error)

	// UpdateInstanceNodeID 更新任务实例的节点ID（用于任务转移）
	// 同时重置状态为待执行
	UpdateInstanceNodeID(ctx context.Context, taskID string, newNodeID string) error

	// TruncateAllInstances 清空所有任务实例（物理删除）
	// 用于重算前清空表
	TruncateAllInstances(ctx context.Context) error

	// TruncateAndBatchCreate 原子操作：清空表并批量创建实例
	// 用于全量重算，保证truncate和create之间不会被其他操作打断
	TruncateAndBatchCreate(ctx context.Context, instances []*model.CollectorTaskInstance) error

	// ========== 差异更新相关（任务ID稳定化）==========

	// GetAllTaskInstances 获取所有有效的任务实例
	// 用于差异对比，只返回 c_invalid = 0 的实例
	GetAllTaskInstances(ctx context.Context) ([]*model.CollectorTaskInstance, error)

	// BatchDeleteByTaskIDs 批量删除指定task_id的任务实例
	BatchDeleteByTaskIDs(ctx context.Context, taskIDs []string) error

	// BatchUpsertInstances 批量插入或更新任务实例
	// 如果task_id已存在则更新，否则插入
	BatchUpsertInstances(ctx context.Context, instances []*model.CollectorTaskInstance) error

	// DiffUpdateInstances 差异更新任务实例（原子操作）
	// 根据差异情况：删除、创建、更新
	DiffUpdateInstances(ctx context.Context, toCreate, toUpdate []*model.CollectorTaskInstance, toDelete []string) error
}

// InstanceParamUpdate 实例参数更新结构
type InstanceParamUpdate struct {
	TaskID     string // 任务ID
	TaskParams string // 新的任务参数
}

// InstanceFilter 任务实例筛选条件
type InstanceFilter struct {
	TaskID          string // 任务ID
	RuleID          string // 规则ID
	PlannedExecNode string // v2.0: 计划执行节点
	LastExecNode    string // v2.0: 最后执行节点
	LastExecStatus  *int   // v2.0: 最后执行状态（使用指针以区分0值和未设置）
	Symbol          string // 交易标的
	Invalid         *int   // 是否有效（使用指针以区分0值和未设置）
	Page            int    // 页码（从1开始）
	PageSize        int    // 每页数量
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
		Where("c_task_id = ?", instanceID).
		First(&instance)

	if result.Error != nil {
		if errors.Is(result.Error, gorm.ErrRecordNotFound) {
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
		Where("c_task_id = ?", instance.TaskID).
		Updates(map[string]interface{}{
			"c_rule_id":           instance.RuleID,
			"c_planned_exec_node": instance.PlannedExecNode,
			"c_last_exec_node":    instance.LastExecNode,
			"c_task_params":       instance.TaskParams,
			"c_last_exec_status":  instance.LastExecStatus,
			"c_last_exec_time":    instance.LastExecTime,
			"c_result":            instance.Result,
			"c_mtime":             instance.ModifyTime,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update task instance: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task instance not found,taskID:%s", instance.TaskID)
	}
	return nil
}

// DeleteTaskInstance 删除任务实例
func (d *collectorTaskInstanceDaoImpl) DeleteTaskInstance(ctx context.Context, instanceID string) error {

	result := d.db.WithContext(ctx).
		Where("c_task_id = ?", instanceID).
		Delete(&model.CollectorTaskInstance{})

	if result.Error != nil {
		return fmt.Errorf("failed to delete task instance: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task instance not found,instanceID:%s", instanceID)
	}
	return nil
}

// GetTaskInstancesByNode 获取节点的任务实例
func (d *collectorTaskInstanceDaoImpl) GetTaskInstancesByNode(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error) {

	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).Where("c_planned_exec_node = ?", nodeID)

	if len(status) > 0 {
		query = query.Where("c_last_exec_status IN ?", status)
	}

	result := query.Order("c_ctime DESC").Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task instances by node: %w", result.Error)
	}
	return instances, nil
}

// GetTaskInstancesByRule 根据规则获取实例列表
func (d *collectorTaskInstanceDaoImpl) GetTaskInstancesByRule(ctx context.Context, ruleID string, limit int) ([]*model.CollectorTaskInstance, error) {

	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).
		Where("c_rule_id = ?", ruleID).
		Order("c_ctime DESC")

	if limit > 0 {
		query = query.Limit(limit)
	}

	result := query.Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task instances by rule: %w", result.Error)
	}
	return instances, nil
}

// GetTaskInstancesByNodeAndStatus 根据节点和状态获取实例
func (d *collectorTaskInstanceDaoImpl) GetTaskInstancesByNodeAndStatus(ctx context.Context, nodeID string, status []int) ([]*model.CollectorTaskInstance, error) {

	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).Where("c_planned_exec_node = ?", nodeID)

	if len(status) > 0 {
		query = query.Where("c_last_exec_status IN ?", status)
	}

	result := query.Order("c_ctime DESC").Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task instances by node and status: %w", result.Error)
	}
	return instances, nil
}

// GetPendingInstances 获取待执行的实例
func (d *collectorTaskInstanceDaoImpl) GetPendingInstances(ctx context.Context, nodeID string) ([]*model.CollectorTaskInstance, error) {

	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx).Where("c_last_exec_status = ?", 0) // 0 = 待执行

	if nodeID != "" {
		query = query.Where("c_planned_exec_node = ?", nodeID)
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
	query := d.db.WithContext(ctx).Where("c_last_exec_status = ?", 1) // 1 = 执行中

	if nodeID != "" {
		query = query.Where("c_planned_exec_node = ?", nodeID)
	}

	result := query.Order("c_ctime ASC").Find(&instances)
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

// GetTaskInstancesByStatus 根据状态获取任务实例
func (d *collectorTaskInstanceDaoImpl) GetTaskInstancesByStatus(ctx context.Context, status []int) ([]*model.CollectorTaskInstance, error) {

	var instances []*model.CollectorTaskInstance
	query := d.db.WithContext(ctx)

	if len(status) > 0 {
		query = query.Where("c_last_exec_status IN ?", status)
	}

	result := query.Order("c_ctime DESC").Find(&instances)
	if result.Error != nil {
		return nil, fmt.Errorf("failed to get task instances by status: %w", result.Error)
	}
	return instances, nil
}

// UpdateInstanceStatus 更新实例状态
func (d *collectorTaskInstanceDaoImpl) UpdateInstanceStatus(ctx context.Context, instanceID string, status int, result string) error {

	updates := map[string]interface{}{
		"c_last_exec_status": status,
		"c_mtime":            time.Now(),
	}

	if result != "" {
		updates["c_result"] = result
	}

	dbResult := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id = ?", instanceID).
		Updates(updates)

	if dbResult.Error != nil {
		return fmt.Errorf("failed to update instance status: %w", dbResult.Error)
	}

	if dbResult.RowsAffected == 0 {
		return fmt.Errorf("instance not found:%s", instanceID)
	}
	return nil
}

// StartInstance 开始执行实例
func (d *collectorTaskInstanceDaoImpl) StartInstance(ctx context.Context, instanceID string) error {

	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id = ? AND c_last_exec_status = ?", instanceID, 0).
		Updates(map[string]interface{}{
			"c_last_exec_status": 1, // 执行中
			"c_mtime":            now,
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
	status := 2 // 成功
	if !success {
		status = 3 // 失败
	}

	updates := map[string]interface{}{
		"c_last_exec_status": status,
		"c_last_exec_time":   now,
		"c_result":           result,
		"c_mtime":            now,
	}

	dbResult := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id = ? AND c_last_exec_status = ?", instanceID, 1).
		Updates(updates)

	if dbResult.Error != nil {
		return fmt.Errorf("failed to complete instance: %w", dbResult.Error)
	}

	if dbResult.RowsAffected == 0 {
		return fmt.Errorf("instance not found or not in running status")
	}
	return nil
}

// ReportInstanceStatus 上报实例状态（客户端上报用，无状态前置条件限制）
func (d *collectorTaskInstanceDaoImpl) ReportInstanceStatus(ctx context.Context, instanceID string, nodeID string, status int, result string) error {

	now := time.Now()
	updates := map[string]interface{}{
		"c_last_exec_node":   nodeID,
		"c_last_exec_status": status,
		"c_last_exec_time":   now,
		"c_mtime":            now,
	}

	if result != "" {
		updates["c_result"] = result
	}

	dbResult := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id = ?", instanceID).
		Updates(updates)

	if dbResult.Error != nil {
		return fmt.Errorf("failed to report instance status: %w", dbResult.Error)
	}

	if dbResult.RowsAffected == 0 {
		return fmt.Errorf("instance not found:%s", instanceID)
	}
	return nil
}

// BatchCreateInstances 批量创建实例
func (d *collectorTaskInstanceDaoImpl) BatchCreateInstances(ctx context.Context, instances []*model.CollectorTaskInstance) error {

	return d.batchCreateInstancesWithoutLock(ctx, instances)
}

// BatchUpdateStatus 批量更新实例状态
func (d *collectorTaskInstanceDaoImpl) BatchUpdateStatus(ctx context.Context, instanceIDs []string, status int, result string) error {

	if len(instanceIDs) == 0 {
		return nil
	}

	updates := map[string]interface{}{
		"c_last_exec_status": status,
		"c_mtime":            time.Now(),
	}

	if result != "" {
		updates["c_result"] = result
	}

	dbResult := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id IN ?", instanceIDs).
		Updates(updates)

	if dbResult.Error != nil {
		return fmt.Errorf("failed to batch update instance status: %w", dbResult.Error)
	}
	return nil
}

// CleanupOldInstances 清理旧的实例记录
func (d *collectorTaskInstanceDaoImpl) CleanupOldInstances(ctx context.Context, days int) error {

	cutoffTime := time.Now().AddDate(0, 0, -days)

	result := d.db.WithContext(ctx).
		Where("c_ctime < ? AND c_last_exec_status IN ?", cutoffTime, []int{2, 3, 4}). // 成功、失败、超时
		Delete(&model.CollectorTaskInstance{})

	if result.Error != nil {
		return fmt.Errorf("failed to cleanup old instances: %w", result.Error)
	}
	return nil
}

// GetInstanceStatistics 获取实例统计信息
func (d *collectorTaskInstanceDaoImpl) GetInstanceStatistics(ctx context.Context, ruleID string) (map[string]interface{}, error) {

	query := d.db.WithContext(ctx)

	if ruleID != "" {
		query = query.Where("c_rule_id = ?", ruleID)
	}

	// 统计各状态数量
	var totalCount, pendingCount, runningCount, successCount, failedCount int64

	query.Model(&model.CollectorTaskInstance{}).Count(&totalCount)
	query.Where("c_last_exec_status = ?", 0).Count(&pendingCount) // 待执行
	query.Where("c_last_exec_status = ?", 1).Count(&runningCount) // 执行中
	query.Where("c_last_exec_status = ?", 2).Count(&successCount) // 成功
	query.Where("c_last_exec_status = ?", 3).Count(&failedCount)  // 失败

	statistics := map[string]interface{}{
		"total_count":   totalCount,
		"pending_count": pendingCount,
		"running_count": runningCount,
		"success_count": successCount,
		"failed_count":  failedCount,
		"success_rate":  0.0,
	}

	// 计算成功率
	if totalCount > 0 {
		statistics["success_rate"] = float64(successCount) / float64(totalCount) * 100
	}

	return statistics, nil
}

// GetActiveInstancesByRule 获取规则的有效实例（用于增量更新）
func (d *collectorTaskInstanceDaoImpl) GetActiveInstancesByRule(ctx context.Context, ruleID string) ([]*model.CollectorTaskInstance, error) {

	var instances []*model.CollectorTaskInstance
	result := d.db.WithContext(ctx).
		Where("c_rule_id = ? AND c_invalid = ?", ruleID, 0).
		Order("c_ctime DESC").
		Find(&instances)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to get active instances by rule: %w", result.Error)
	}
	return instances, nil
}

// InvalidateInstancesByRule 批量标记规则实例为无效
func (d *collectorTaskInstanceDaoImpl) InvalidateInstancesByRule(ctx context.Context, ruleID string) error {

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_rule_id = ? AND c_invalid = ?", ruleID, 0).
		Updates(map[string]interface{}{
			"c_invalid": 1,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to invalidate instances by rule: %w", result.Error)
	}
	return nil
}

// BatchUpdateParams 批量更新实例参数
func (d *collectorTaskInstanceDaoImpl) BatchUpdateParams(ctx context.Context, updates []*InstanceParamUpdate) error {

	if len(updates) == 0 {
		return nil
	}

	now := time.Now()
	// 使用事务批量更新
	return d.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, update := range updates {
			result := tx.Model(&model.CollectorTaskInstance{}).
				Where("c_task_id = ?", update.TaskID).
				Updates(map[string]interface{}{
					"c_task_params": update.TaskParams,
					"c_mtime":       now,
				})
			if result.Error != nil {
				return fmt.Errorf("failed to update instance params for %s: %w", update.TaskID, result.Error)
			}
		}
		return nil
	})
}

// BatchInvalidate 批量使实例失效
func (d *collectorTaskInstanceDaoImpl) BatchInvalidate(ctx context.Context, taskIDs []string) error {

	if len(taskIDs) == 0 {
		return nil
	}

	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id IN ?", taskIDs).
		Updates(map[string]interface{}{
			"c_invalid": 1,
			"c_mtime":   time.Now(),
		})

	if result.Error != nil {
		return fmt.Errorf("failed to batch invalidate instances: %w", result.Error)
	}
	return nil
}

// FindAllSuccessNodesByRule 查找同规则下所有执行成功的节点
func (d *collectorTaskInstanceDaoImpl) FindAllSuccessNodesByRule(ctx context.Context, ruleID string, excludeNodeID string) ([]string, error) {

	var instances []model.CollectorTaskInstance
	result := d.db.WithContext(ctx).
		Select("DISTINCT c_last_exec_node").
		Where("c_rule_id = ? AND c_last_exec_status = ? AND c_invalid = ? AND c_last_exec_node != ?",
			ruleID, model.InstanceStatusSuccess, 0, excludeNodeID).
		Order("c_mtime DESC").
		Find(&instances)

	if result.Error != nil {
		return nil, fmt.Errorf("failed to find success nodes: %w", result.Error)
	}

	if len(instances) == 0 {
		return nil, fmt.Errorf("no success nodes found for rule %s", ruleID)
	}

	// 提取节点ID列表
	var nodeIDs []string
	for _, instance := range instances {
		if instance.LastExecNode != "" {
			nodeIDs = append(nodeIDs, instance.LastExecNode)
		}
	}

	if len(nodeIDs) == 0 {
		return nil, fmt.Errorf("no valid success nodes found for rule %s", ruleID)
	}

	return nodeIDs, nil
}

// UpdateInstanceNodeID 更新任务实例的节点ID（用于任务转移）
func (d *collectorTaskInstanceDaoImpl) UpdateInstanceNodeID(ctx context.Context, taskID string, newNodeID string) error {

	now := time.Now()
	result := d.db.WithContext(ctx).
		Model(&model.CollectorTaskInstance{}).
		Where("c_task_id = ?", taskID).
		Updates(map[string]interface{}{
			"c_planned_exec_node": newNodeID,
			"c_last_exec_status":  model.InstanceStatusPending, // 重置为待执行
			"c_mtime":             now,
		})

	if result.Error != nil {
		return fmt.Errorf("failed to update instance node: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return fmt.Errorf("task instance not found: %s", taskID)
	}

	return nil
}

// ListInstancesWithPagination 分页查询任务实例
func (d *collectorTaskInstanceDaoImpl) ListInstancesWithPagination(ctx context.Context, nodeID, ruleID string, page, size int) ([]*model.CollectorTaskInstance, int64, error) {

	// 参数校验
	if page < 1 {
		page = 1
	}
	if size <= 0 {
		size = 10
	}
	if size > 100 {
		size = 100
	}

	var instances []*model.CollectorTaskInstance
	var total int64

	// 构建查询条件
	query := d.db.WithContext(ctx).Model(&model.CollectorTaskInstance{}).Where("c_invalid = ?", 0)

	if nodeID != "" {
		query = query.Where("c_planned_exec_node = ?", nodeID)
	}
	if ruleID != "" {
		query = query.Where("c_rule_id = ?", ruleID)
	}

	// 查询总数
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count instances: %w", err)
	}

	// 分页查询
	offset := (page - 1) * size
	if err := query.Order("c_ctime DESC").Offset(offset).Limit(size).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list instances: %w", err)
	}

	return instances, total, nil
}

// ListInstancesWithFilter 带筛选条件的分页查询任务实例
func (d *collectorTaskInstanceDaoImpl) ListInstancesWithFilter(ctx context.Context, filter *InstanceFilter) ([]*model.CollectorTaskInstance, int64, error) {

	// 参数校验
	if filter == nil {
		filter = &InstanceFilter{}
	}
	if filter.Page < 1 {
		filter.Page = 1
	}
	if filter.PageSize <= 0 {
		filter.PageSize = 10
	}
	if filter.PageSize > 100 {
		filter.PageSize = 100
	}

	var instances []*model.CollectorTaskInstance
	var total int64

	// 构建查询条件
	query := d.db.WithContext(ctx).Model(&model.CollectorTaskInstance{})

	// 如果没有指定invalid，默认只查询有效的记录
	if filter.Invalid != nil {
		query = query.Where("c_invalid = ?", *filter.Invalid)
	} else {
		query = query.Where("c_invalid = ?", 0)
	}

	if filter.TaskID != "" {
		query = query.Where("c_task_id LIKE ?", "%"+filter.TaskID+"%")
	}
	if filter.RuleID != "" {
		query = query.Where("c_rule_id LIKE ?", "%"+filter.RuleID+"%")
	}
	if filter.PlannedExecNode != "" {
		query = query.Where("c_planned_exec_node LIKE ?", "%"+filter.PlannedExecNode+"%")
	}
	if filter.LastExecNode != "" {
		query = query.Where("c_last_exec_node LIKE ?", "%"+filter.LastExecNode+"%")
	}
	if filter.Symbol != "" {
		query = query.Where("c_symbol LIKE ?", "%"+filter.Symbol+"%")
	}
	if filter.LastExecStatus != nil {
		query = query.Where("c_last_exec_status = ?", *filter.LastExecStatus)
	}

	// 查询总数
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to count instances: %w", err)
	}

	// 分页查询
	offset := (filter.Page - 1) * filter.PageSize
	if err := query.Order("c_ctime DESC").Offset(offset).Limit(filter.PageSize).Find(&instances).Error; err != nil {
		return nil, 0, fmt.Errorf("failed to list instances: %w", err)
	}

	return instances, total, nil
}

// TruncateAllInstances 清空所有任务实例（物理删除）
func (dao *collectorTaskInstanceDaoImpl) TruncateAllInstances(ctx context.Context) error {
	result := dao.db.WithContext(ctx).Exec("DELETE FROM t_collector_task_instances")
	if result.Error != nil {
		return fmt.Errorf("failed to truncate task instances: %w", result.Error)
	}
	return nil
}

// TruncateAndBatchCreate 原子操作：清空表并批量创建实例
// 用于全量重算，保证truncate和create之间不会被其他操作打断
func (dao *collectorTaskInstanceDaoImpl) TruncateAndBatchCreate(ctx context.Context, instances []*model.CollectorTaskInstance) error {
	// 使用数据库事务保证原子性
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// 1. 清空表
		result := tx.Exec("DELETE FROM t_collector_task_instances")
		if result.Error != nil {
			return fmt.Errorf("failed to truncate task instances: %w", result.Error)
		}

		// 2. 批量创建
		if len(instances) > 0 {
			now := time.Now()
			for _, instance := range instances {
				instance.CreateTime = now
				instance.ModifyTime = now
			}

			result := tx.CreateInBatches(instances, 100)
			if result.Error != nil {
				return fmt.Errorf("failed to batch create instances: %w", result.Error)
			}
		}

		return nil
	})
}

// ========== 内部辅助方法（不带锁） ==========

// batchCreateInstancesWithoutLock 批量创建实例（内部方法，不加锁）
func (dao *collectorTaskInstanceDaoImpl) batchCreateInstancesWithoutLock(ctx context.Context, instances []*model.CollectorTaskInstance) error {
	if len(instances) == 0 {
		return nil
	}

	now := time.Now()
	for _, instance := range instances {
		instance.CreateTime = now
		instance.ModifyTime = now
	}

	result := dao.db.WithContext(ctx).CreateInBatches(instances, 100)
	if result.Error != nil {
		return fmt.Errorf("failed to batch create instances: %w", result.Error)
	}
	return nil
}

// ========== 差异更新相关方法实现 ==========

// GetAllTaskInstances 获取所有有效的任务实例
func (dao *collectorTaskInstanceDaoImpl) GetAllTaskInstances(ctx context.Context) ([]*model.CollectorTaskInstance, error) {
	var instances []*model.CollectorTaskInstance
	err := dao.db.WithContext(ctx).
		Where("c_invalid = ?", model.InvalidNo).
		Find(&instances).Error

	if err != nil {
		return nil, fmt.Errorf("failed to get all task instances: %w", err)
	}

	return instances, nil
}

// BatchDeleteByTaskIDs 批量删除指定task_id的任务实例
func (dao *collectorTaskInstanceDaoImpl) BatchDeleteByTaskIDs(ctx context.Context, taskIDs []string) error {
	if len(taskIDs) == 0 {
		return nil
	}

	result := dao.db.WithContext(ctx).
		Where("c_task_id IN ?", taskIDs).
		Delete(&model.CollectorTaskInstance{})

	if result.Error != nil {
		return fmt.Errorf("failed to batch delete instances: %w", result.Error)
	}

	return nil
}

// BatchUpsertInstances 批量插入或更新任务实例
func (dao *collectorTaskInstanceDaoImpl) BatchUpsertInstances(ctx context.Context, instances []*model.CollectorTaskInstance) error {
	if len(instances) == 0 {
		return nil
	}

	now := time.Now()
	for _, instance := range instances {
		if instance.CreateTime.IsZero() {
			instance.CreateTime = now
		}
		instance.ModifyTime = now
	}

	// SQLite 不支持 ON DUPLICATE KEY UPDATE，需要逐条处理
	for _, instance := range instances {
		// 尝试查找已存在的记录
		var existing model.CollectorTaskInstance
		err := dao.db.WithContext(ctx).
			Where("c_task_id = ?", instance.TaskID).
			First(&existing).Error

		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				// 记录不存在，创建新记录
				if err := dao.db.WithContext(ctx).Create(instance).Error; err != nil {
					return fmt.Errorf("failed to create instance %s: %w", instance.TaskID, err)
				}
			} else {
				return fmt.Errorf("failed to query instance %s: %w", instance.TaskID, err)
			}
		} else {
			// 记录存在，更新
			if err := dao.db.WithContext(ctx).Model(&existing).Updates(map[string]interface{}{
				"c_rule_id":           instance.RuleID,
				"c_planned_exec_node": instance.PlannedExecNode,
				"c_symbol":            instance.Symbol,
				"c_collect_data_type": instance.CollectDataType,
				"c_task_params":       instance.TaskParams,
				"c_mtime":             now,
			}).Error; err != nil {
				return fmt.Errorf("failed to update instance %s: %w", instance.TaskID, err)
			}
		}
	}

	return nil
}

// DiffUpdateInstances 差异更新任务实例（原子操作）
func (dao *collectorTaskInstanceDaoImpl) DiffUpdateInstances(ctx context.Context, toCreate, toUpdate []*model.CollectorTaskInstance, toDelete []string) error {
	return dao.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()

		// 1. 删除不再需要的实例
		if len(toDelete) > 0 {
			result := tx.Where("c_task_id IN ?", toDelete).
				Delete(&model.CollectorTaskInstance{})
			if result.Error != nil {
				return fmt.Errorf("failed to delete instances: %w", result.Error)
			}
		}

		// 2. 创建新实例
		if len(toCreate) > 0 {
			for _, instance := range toCreate {
				instance.CreateTime = now
				instance.ModifyTime = now
			}
			result := tx.CreateInBatches(toCreate, 100)
			if result.Error != nil {
				return fmt.Errorf("failed to create instances: %w", result.Error)
			}
		}

		// 3. 更新已有实例（仅更新必要字段，保留执行状态）
		for _, instance := range toUpdate {
			result := tx.Model(&model.CollectorTaskInstance{}).
				Where("c_task_id = ?", instance.TaskID).
				Updates(map[string]interface{}{
					"c_planned_exec_node": instance.PlannedExecNode,
					"c_task_params":       instance.TaskParams,
					"c_mtime":             now,
				})
			if result.Error != nil {
				return fmt.Errorf("failed to update instance %s: %w", instance.TaskID, result.Error)
			}
		}

		return nil
	})
}
