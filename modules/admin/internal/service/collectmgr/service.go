package collectmgr

import (
	"context"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// Service 采集器服务总接口
type Service interface {
	TaskRuleService
	TaskInstanceService
	DataTypeConfigService
}

// CloudFunctionInvoker 云函数调用接口（用于解决循环依赖）
// 该接口由 cloudnode.Service 实现
type CloudFunctionInvoker interface {
	// InvokeFunction 调用云函数
	InvokeFunction(ctx context.Context, nodeID string, eventData interface{}) (interface{}, error)
}

// DataTypeConfigService 数据类型配置服务接口
//
// 设计说明：collectmgr service 层直接以 admingen PB 类型作为入参/出参，
// 不再维护中间 DTO，消除 RPC 层与 service 层之间的翻译映射。
// dao/model 层仍保留内部 model（带 gorm tag，负责 DB 映射），
// service 实现在内部做一次性 model→PB 转换。
type DataTypeConfigService interface {
	// GetDataTypeConfigs 获取所有数据类型配置
	GetDataTypeConfigs(ctx context.Context) ([]*pb.DataTypeConfig, error)

	// GetDataTypeConfigWithFields 获取数据类型配置及字段信息
	GetDataTypeConfigWithFields(ctx context.Context, dataType string) (*pb.DataTypeConfigDetail, error)
}

// TaskRuleService 任务规则服务接口
type TaskRuleService interface {
	// GetTaskRuleList 获取任务规则列表
	GetTaskRuleList(ctx context.Context, spaceID, bizType, ruleID, dataType, dataSource, enabled string) ([]*pb.TaskRule, error)

	// GetTaskRule 获取单个任务规则
	GetTaskRule(ctx context.Context, ruleID string) (*pb.TaskRule, error)

	// CreateTaskRule 创建任务规则，返回生成的RuleID
	CreateTaskRule(ctx context.Context, rule *pb.TaskRule) (string, error)

	// UpdateTaskRule 更新任务规则
	UpdateTaskRule(ctx context.Context, rule *pb.TaskRule) error

	// DisableTaskRule 关闭任务规则（设置为禁用）
	DisableTaskRule(ctx context.Context, ruleID string) error
}

// TaskInstanceService 任务实例服务接口
type TaskInstanceService interface {
	// CreateTaskInstance 创建任务实例
	CreateTaskInstance(ctx context.Context, instance *pb.TaskInstance) error

	// GetTaskInstance 获取任务实例
	GetTaskInstance(ctx context.Context, instanceID string) (*pb.TaskInstance, error)

	// GetTaskInstanceList 获取任务实例列表（旧接口，保留兼容）
	GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*pb.TaskInstance, error)

	// ListTaskInstances 分页查询任务实例
	// nodeID: 可选，按节点筛选
	// ruleID: 可选，按规则筛选
	// page: 页码（从1开始）
	// size: 每页数量
	// 返回: 实例列表、总数、错误
	ListTaskInstances(ctx context.Context, nodeID, ruleID string, page, size int) ([]*pb.TaskInstance, int64, error)

	// ListTaskInstancesWithFilter 带筛选条件的分页查询任务实例
	ListTaskInstancesWithFilter(ctx context.Context, filter *pb.TaskInstanceFilter) ([]*pb.TaskInstance, int64, error)

	// GetTaskInstanceListCache 带缓存的任务实例列表查询（仅支持精确条件、有效数据）
	GetTaskInstanceListCache(ctx context.Context, filter *pb.TaskInstanceFilter) ([]*pb.TaskInstance, int64, error)

	// UpdateTaskInstance 更新任务实例
	UpdateTaskInstance(ctx context.Context, instanceID string, instance *pb.TaskInstance) error

	// RemoveTaskInstance 删除任务实例
	RemoveTaskInstance(ctx context.Context, instanceID string) error

	// StartInstance 开始执行实例
	StartInstance(ctx context.Context, instanceID string) error

	// CompleteInstance 完成实例执行
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error

	// ReportTaskStatus 上报任务状态（客户端上报用）
	// 新增 nodeID 参数，更新 c_last_exec_node、c_last_exec_status、c_last_exec_time、c_result
	ReportTaskStatus(ctx context.Context, instanceID string, nodeID string, status int, result string) error

	// InvalidateTaskInstance 作废任务实例
	InvalidateTaskInstance(ctx context.Context, taskID string) error

	// InvalidateTaskInstanceCache 失效任务实例缓存
	InvalidateTaskInstanceCache(ctx context.Context) error
}

// TaskPlannerService 任务规划器服务接口
type TaskPlannerService interface {
	// RecalculateAllTaskInstances 重算所有启用规则的任务实例（定时任务调用）
	RecalculateAllTaskInstances(ctx context.Context) error
}
