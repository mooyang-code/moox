package collectmgr

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/server/internal/service/collectmgr/dto"
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
type DataTypeConfigService interface {
	// GetDataTypeConfigs 获取所有数据类型配置
	GetDataTypeConfigs(ctx context.Context) ([]*DataTypeConfigDTO, error)

	// GetDataTypeConfigWithFields 获取数据类型配置及字段信息
	GetDataTypeConfigWithFields(ctx context.Context, dataType string) (*DataTypeConfigDetailDTO, error)
}

// DataTypeConfigDTO 数据类型配置数据传输对象
type DataTypeConfigDTO struct {
	ID                int       `json:"id"`
	DataType          string    `json:"data_type"`
	TypeName          string    `json:"type_name"`
	TypeDesc          string    `json:"type_desc"`
	DataSourceOptions string    `json:"data_source_options"`
	SortOrder         int       `json:"sort_order"`
	Version           int       `json:"version"`
	CreateTime        time.Time `json:"create_time"`
	ModifyTime        time.Time `json:"modify_time"`
}

// DataTypeConfigDetailDTO 数据类型配置详情传输对象
type DataTypeConfigDetailDTO struct {
	Config *DataTypeConfigDTO `json:"config"`
	Fields []*FieldConfigDTO  `json:"fields"`
}

// FieldConfigDTO 字段配置数据传输对象
type FieldConfigDTO struct {
	ID                int       `json:"id"`
	DataType          string    `json:"data_type"`
	FieldKey          string    `json:"field_key"`
	FieldName         string    `json:"field_name"`
	FieldType         string    `json:"field_type"`
	IsRequired        bool      `json:"is_required"`
	DefaultValue      string    `json:"default_value"`
	FieldOptions      string    `json:"field_options"`
	DataSourceOptions string    `json:"data_source_options"`
	SortOrder         int       `json:"sort_order"`
	CreateTime        time.Time `json:"create_time"`
	ModifyTime        time.Time `json:"modify_time"`
}

// TaskRuleService 任务规则服务接口
type TaskRuleService interface {
	// GetTaskRuleList 获取任务规则列表
	GetTaskRuleList(ctx context.Context, bizType, dataType, dataSource, enabled string) ([]*dto.TaskRuleDTO, error)

	// GetTaskRule 获取单个任务规则
	GetTaskRule(ctx context.Context, ruleID string) (*dto.TaskRuleDTO, error)

	// CreateTaskRule 创建任务规则，返回生成的RuleID
	CreateTaskRule(ctx context.Context, rule *dto.TaskRuleDTO) (string, error)

	// UpdateTaskRule 更新任务规则
	UpdateTaskRule(ctx context.Context, rule *dto.TaskRuleDTO) error

	// DisableTaskRule 关闭任务规则（设置为禁用）
	DisableTaskRule(ctx context.Context, ruleID string) error
}

// TaskInstanceService 任务实例服务接口
type TaskInstanceService interface {
	// CreateTaskInstance 创建任务实例
	CreateTaskInstance(ctx context.Context, instance *TaskInstanceDTO) error

	// GetTaskInstance 获取任务实例
	GetTaskInstance(ctx context.Context, instanceID string) (*TaskInstanceDTO, error)

	// GetTaskInstanceList 获取任务实例列表（旧接口，保留兼容）
	GetTaskInstanceList(ctx context.Context, nodeID string, limit, offset int) ([]*TaskInstanceDTO, error)

	// ListTaskInstances 分页查询任务实例
	// nodeID: 可选，按节点筛选
	// ruleID: 可选，按规则筛选
	// page: 页码（从1开始）
	// size: 每页数量
	// 返回: 实例列表、总数、错误
	ListTaskInstances(ctx context.Context, nodeID, ruleID string, page, size int) ([]*TaskInstanceDTO, int64, error)

	// ListTaskInstancesWithFilter 带筛选条件的分页查询任务实例
	ListTaskInstancesWithFilter(ctx context.Context, filter *TaskInstanceFilterDTO) ([]*TaskInstanceDTO, int64, error)

	// GetTaskInstanceListCache 带缓存的任务实例列表查询（仅支持精确条件、有效数据）
	GetTaskInstanceListCache(ctx context.Context, filter *TaskInstanceFilterDTO) ([]*TaskInstanceDTO, int64, error)

	// UpdateTaskInstance 更新任务实例
	UpdateTaskInstance(ctx context.Context, instanceID string, instance *TaskInstanceDTO) error

	// RemoveTaskInstance 删除任务实例
	RemoveTaskInstance(ctx context.Context, instanceID string) error

	// StartInstance 开始执行实例
	StartInstance(ctx context.Context, instanceID string) error

	// CompleteInstance 完成实例执行
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error

	// ReportTaskStatus 上报任务状态（客户端上报用）
	// v2.0: 新增 nodeID 参数，更新 c_last_exec_node、c_last_exec_status、c_last_exec_time、c_result
	ReportTaskStatus(ctx context.Context, instanceID string, nodeID string, status int, result string) error

	// InvalidateTaskInstance 作废任务实例
	InvalidateTaskInstance(ctx context.Context, taskID string) error

	// InvalidateTaskInstanceCache 失效任务实例缓存
	InvalidateTaskInstanceCache(ctx context.Context) error
}

// TaskInstanceDTO 任务实例数据传输对象
type TaskInstanceDTO struct {
	ID              int
	TaskID          string
	RuleID          string
	BizType         string // 业务类型
	// v2.0 新字段
	PlannedExecNode string // 计划执行节点ID
	LastExecNode    string // 最后执行节点ID
	LastExecStatus  int    // 最后执行状态
	// 其他字段
	Symbol          string // 标的
	CollectDataType string // 采集数据类型（从 task_params 提取）
	DataType        string // 数据类型（从规则表关联获取）
	TaskParams      string
	LastExecTime    *time.Time // 最后执行时间
	Result          string
	Invalid         int // 删除标记
	CreateTime      time.Time
	ModifyTime      time.Time
}

// TaskInstanceFilterDTO 任务实例筛选条件
type TaskInstanceFilterDTO struct {
	BizType         string // 业务类型
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

// TaskPlannerService 任务规划器服务接口
type TaskPlannerService interface {
	// RecalculateAllTaskInstances 重算所有启用规则的任务实例（定时任务调用）
	RecalculateAllTaskInstances(ctx context.Context) error
}
