package model

import (
	"time"
)

// CollectorTaskInstance 采集任务实例表（记录实际分配的任务）
type CollectorTaskInstance struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// TaskID 任务唯一标识
	TaskID string `gorm:"column:c_task_id;uniqueIndex:idx_task_id;not null" json:"task_id"`
	// RuleID 规则ID（关联配置表）
	RuleID string `gorm:"column:c_rule_id;index:idx_rule_id;not null" json:"rule_id"`

	// ===== v2.0 新字段 =====
	// PlannedExecNode 计划执行节点ID（定时重算时写入）
	PlannedExecNode string `gorm:"column:c_planned_exec_node;index:idx_planned_node,idx_planned_node_status,idx_planned_node_interval;not null;default:''" json:"planned_exec_node"`
	// LastExecNode 最后执行节点ID（客户端上报时写入）
	LastExecNode string `gorm:"column:c_last_exec_node;not null;default:''" json:"last_exec_node"`
	// LastExecStatus 最后执行状态（客户端上报时写入）
	LastExecStatus int `gorm:"column:c_last_exec_status;index:idx_planned_node_status;not null;default:0" json:"last_exec_status"`

	// Symbol 标的（用于唯一约束和快速查询，如 BTC-USDT）
	Symbol string `gorm:"column:c_symbol;not null;default:''" json:"symbol"`
	// CollectDataType 采集数据类型（从 c_task_params 中的 data_type 提取，用于快速查询）
	CollectDataType string `gorm:"column:c_collect_data_type;not null;default:''" json:"collect_data_type"`
	// Interval 时间间隔（1m/5m/1h等，非interval类任务为"default"）
	Interval string `gorm:"column:c_interval;index:idx_planned_node_interval;not null;default:'default'" json:"interval"`
	// TaskParams 任务执行参数
	TaskParams string `gorm:"column:c_task_params;type:text;not null;default:'{}'" json:"task_params"`

	// LastExecTime 最后执行时间
	LastExecTime *time.Time `gorm:"column:c_last_exec_time;type:datetime" json:"last_exec_time"`
	// Result 执行结果（JSON格式）
	Result string `gorm:"column:c_result;type:text;not null;default:'{}'" json:"result"`
	// Invalid 删除标记（0=有效，1=无效）
	Invalid int `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP;index:idx_create_time" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (c *CollectorTaskInstance) TableName() string {
	return "t_collector_task_instances"
}
