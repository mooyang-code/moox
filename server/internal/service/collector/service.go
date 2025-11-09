package collector

import (
	"context"
	"time"
)

// Service 采集器服务总接口
type Service interface {
	TaskRuleService
	TaskInstanceService
	DataTypeConfigService
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
	ID                 int       `json:"id"`
	DataType           string    `json:"data_type"`
	TypeName           string    `json:"type_name"`
	TypeDesc           string    `json:"type_desc"`
	DataSourceOptions  string    `json:"data_source_options"`
	SortOrder          int       `json:"sort_order"`
	Version            int       `json:"version"`
	CreateTime         time.Time `json:"create_time"`
	ModifyTime         time.Time `json:"modify_time"`
}

// DataTypeConfigDetailDTO 数据类型配置详情传输对象
type DataTypeConfigDetailDTO struct {
	Config *DataTypeConfigDTO    `json:"config"`
	Fields []*FieldConfigDTO     `json:"fields"`
}

// FieldConfigDTO 字段配置数据传输对象
type FieldConfigDTO struct {
	ID               int       `json:"id"`
	DataType         string    `json:"data_type"`
	FieldKey         string    `json:"field_key"`
	FieldName        string    `json:"field_name"`
	FieldType        string    `json:"field_type"`
	IsRequired       bool      `json:"is_required"`
	DefaultValue     string    `json:"default_value"`
	FieldOptions     string    `json:"field_options"`
	DataSourceOptions string    `json:"data_source_options"`
	SortOrder        int       `json:"sort_order"`
	CreateTime       time.Time `json:"create_time"`
	ModifyTime       time.Time `json:"modify_time"`
}

// TaskRuleService 任务规则服务接口
type TaskRuleService interface {
	// GetTaskRuleList 获取任务规则列表
	GetTaskRuleList(ctx context.Context, dataType, dataSource, enabled string) ([]*TaskRuleDTO, error)

	// GetTaskRule 获取单个任务规则
	GetTaskRule(ctx context.Context, ruleID string) (*TaskRuleDTO, error)

	// CreateTaskRule 创建任务规则，返回生成的RuleID
	CreateTaskRule(ctx context.Context, rule *TaskRuleDTO) (string, error)

	// UpdateTaskRule 更新任务规则
	UpdateTaskRule(ctx context.Context, rule *TaskRuleDTO) error

	// DisableTaskRule 关闭任务规则（设置为禁用）
	DisableTaskRule(ctx context.Context, ruleID string) error
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
	ID             int    `json:"id"`
	RuleID         string `json:"rule_id"`
	DataType       string `json:"data_type"`
	DataSource     string `json:"data_source"`
	CollectParams  string `json:"collect_params"`
	AssignmentType string `json:"assignment_type"`
	AssignedNodes  string `json:"assigned_nodes"`
	NodePattern    string `json:"node_pattern"`
	Enabled        string `json:"enabled"`
	Creator        string `json:"creator"`
	CreateTime     time.Time `json:"create_time"`
	ModifyTime     time.Time `json:"modify_time"`
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

