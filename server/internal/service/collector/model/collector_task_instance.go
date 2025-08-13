package model

import (
	"time"
	
	cloudnodemodel "github.com/mooyang-code/moox/server/internal/service/cloudnode/model"
)

// CollectorTaskInstance 采集任务实例表（记录实际分配的任务）
type CollectorTaskInstance struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// InstanceID 实例唯一标识
	InstanceID string `gorm:"column:c_instance_id;uniqueIndex:idx_instance_id;not null" json:"instance_id"`
	// TaskID 任务ID（关联配置表）
	TaskID string `gorm:"column:c_task_id;index:idx_task_node;not null" json:"task_id"`
	// ProjectID 项目ID（关联到项目表）
	ProjectID string `gorm:"column:c_project_id;index:idx_project_dataset;not null" json:"project_id"`
	// DatasetID 数据集ID（关联到数据集表）
	DatasetID string `gorm:"column:c_dataset_id;index:idx_project_dataset;not null" json:"dataset_id"`
	// NodeID 执行节点ID
	NodeID string `gorm:"column:c_node_id;index:idx_task_node,idx_node_status;not null" json:"node_id"`
	// TargetObjects 分配的对象列表（JSON数组）
	TargetObjects string `gorm:"column:c_target_objects;type:text;not null;default:'[]'" json:"target_objects"`
	// ExecutionParams 执行参数（合并后的最终参数）
	ExecutionParams string `gorm:"column:c_execution_params;type:text;not null;default:'{}'" json:"execution_params"`
	// Status 状态（0=待执行，1=执行中，2=成功，3=失败，4=超时，5=已取消）
	Status int `gorm:"column:c_status;index:idx_status_time,idx_node_status;not null;default:0" json:"status"`
	// StartTime 开始时间
	StartTime *time.Time `gorm:"column:c_start_time;type:datetime" json:"start_time"`
	// EndTime 结束时间
	EndTime *time.Time `gorm:"column:c_end_time;type:datetime" json:"end_time"`
	// Result 执行结果（JSON格式）
	Result string `gorm:"column:c_result;type:text;not null;default:'{}'" json:"result"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP;index:idx_status_time,idx_create_time" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
	
	// 外键关联（GORM不自动创建外键，需要手动执行SQL）
	Task CollectorTaskConfig `gorm:"foreignKey:TaskID;references:TaskID" json:"-"`
	Node cloudnodemodel.SCFNode `gorm:"foreignKey:NodeID;references:NodeID" json:"-"`
}

// TableName 指定表名
func (c *CollectorTaskInstance) TableName() string {
	return "t_collector_task_instances"
}