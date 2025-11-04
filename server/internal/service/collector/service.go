package collector

import (
	"context"
	"time"
)

// Service 采集器服务总接口
type Service interface {
	TaskRuleService
	TaskInstanceService
}

// TaskRuleService 任务规则服务接口
type TaskRuleService interface {
	// GetTaskRuleList 获取任务规则列表
	GetTaskRuleList(ctx context.Context, dataType, dataSource string) ([]*TaskRuleDTO, error)

	// GetTaskRule 获取单个任务规则
	GetTaskRule(ctx context.Context, ruleID string) (*TaskRuleDTO, error)

	// CreateTaskRule 创建任务规则
	CreateTaskRule(ctx context.Context, config *TaskRuleDTO) error

	// UpdateTaskRule 更新任务规则
	UpdateTaskRule(ctx context.Context, config *TaskRuleDTO) error

	// RemoveTaskRule 删除任务规则
	RemoveTaskRule(ctx context.Context, ruleID string) error
}

// TaskInstanceService 任务实例服务接口
type TaskInstanceService interface {
	// CreateTaskInstance 创建任务实例
	CreateTaskInstance(ctx context.Context, instance *TaskInstanceDTO) error

	// GetTaskInstance 获取任务实例
	GetTaskInstance(ctx context.Context, instanceID string) (*TaskInstanceDTO, error)

	// GetTaskInstanceList 获取任务实例列表
	GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*TaskInstanceDTO, error)

	// UpdateTaskInstance 更新任务实例
	UpdateTaskInstance(ctx context.Context, instanceID string, instance *TaskInstanceDTO) error

	// RemoveTaskInstance 删除任务实例
	RemoveTaskInstance(ctx context.Context, instanceID string) error

	// StartInstance 开始执行实例
	StartInstance(ctx context.Context, instanceID string) error

	// CompleteInstance 完成实例执行
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error
}

// TaskRuleDTO 任务规则数据传输对象
type TaskRuleDTO struct {
	ID             int
	RuleID         string
	DataType       string
	DataSource     string
	CollectParams  string
	AssignmentType string
	AssignedNodes  string
	NodePattern    string
	Enabled        string
	CreateTime     time.Time
	ModifyTime     time.Time
}

// TaskInstanceDTO 任务实例数据传输对象
type TaskInstanceDTO struct {
	ID         int
	TaskID     string
	RuleID     string
	NodeID     string
	TaskParams string
	Status     int
	StartTime  *time.Time
	EndTime    *time.Time
	Result     string
	CreateTime time.Time
	ModifyTime time.Time
}

