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
	GetTaskRuleList(ctx context.Context, dataType, dataSource, enabled string) ([]*dto.TaskRuleDTO, error)

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

	// UpdateTaskInstance 更新任务实例
	UpdateTaskInstance(ctx context.Context, instanceID string, instance *TaskInstanceDTO) error

	// RemoveTaskInstance 删除任务实例
	RemoveTaskInstance(ctx context.Context, instanceID string) error

	// StartInstance 开始执行实例
	StartInstance(ctx context.Context, instanceID string) error

	// CompleteInstance 完成实例执行
	CompleteInstance(ctx context.Context, instanceID string, success bool, result string) error

	// ReportTaskStatus 上报任务状态（客户端上报用）
	// 更新 c_status、c_end_time、c_result，无状态前置条件限制
	ReportTaskStatus(ctx context.Context, instanceID string, status int, result string) error
}

// TaskInstanceDTO 任务实例数据传输对象
type TaskInstanceDTO struct {
	ID         int
	TaskID     string
	RuleID     string
	NodeID     string
	Symbol     string // 新增：标的
	TaskParams string
	Status     int
	StartTime  *time.Time
	EndTime    *time.Time
	Result     string
	Invalid    int // 新增：删除标记
	CreateTime time.Time
	ModifyTime time.Time
}

// TaskInstanceFilterDTO 任务实例筛选条件
type TaskInstanceFilterDTO struct {
	TaskID   string // 任务ID
	RuleID   string // 规则ID
	NodeID   string // 节点ID
	Symbol   string // 交易标的
	Status   *int   // 状态（使用指针以区分0值和未设置）
	Invalid  *int   // 是否有效（使用指针以区分0值和未设置）
	Page     int    // 页码（从1开始）
	PageSize int    // 每页数量
}

// TaskPlannerService 任务规划器服务接口
type TaskPlannerService interface {
	// SyncRuleInstances 同步指定规则的任务实例（幂等操作）
	// 用于：用户创建/修改/启用规则时立即调用
	SyncRuleInstances(ctx context.Context, ruleID string) (*SyncResult, error)

	// InvalidateRuleInstances 使规则的所有实例失效（软删除）
	// 用于：用户禁用规则时调用
	InvalidateRuleInstances(ctx context.Context, ruleID string) error

	// SyncAllEnabledRules 同步所有启用的规则（定时任务调用）
	SyncAllEnabledRules(ctx context.Context) (*BatchSyncResult, error)
}
