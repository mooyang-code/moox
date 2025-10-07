package model

import (
	"time"
)

// NodeHeartbeat 节点心跳表
type NodeHeartbeat struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// NodeID 节点ID
	NodeID string `gorm:"column:c_node_id;uniqueIndex:uk_node_id;not null" json:"node_id"`
	// LastHeartbeat 最后心跳时间
	LastHeartbeat time.Time `gorm:"column:c_last_heartbeat;type:datetime;not null" json:"last_heartbeat"`
	// TaskVersion 任务版本号 (已废弃，保留字段兼容性)
	// TaskVersion int64 `gorm:"column:c_task_version;default:0" json:"task_version"`
	// TaskHash 任务哈希值 (已废弃，保留字段兼容性)
	// TaskHash string `gorm:"column:c_task_hash;default:''" json:"task_hash"`
	// Status 节点状态: 1=正常,0=离线
	Status int `gorm:"column:c_status;default:1" json:"status"`
	// Metrics 节点指标信息（JSON）
	Metrics string `gorm:"column:c_metrics;type:json" json:"metrics"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (n *NodeHeartbeat) TableName() string {
	return "t_node_heartbeat"
}

// NodeTaskSnapshot 节点任务快照表 (已废弃，新架构不再使用)
// type NodeTaskSnapshot struct {
// 	// ID 主键ID
// 	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
// 	// NodeID 节点ID
// 	NodeID string `gorm:"column:c_node_id;index:idx_node_task;not null" json:"node_id"`
// 	// TaskID 任务ID
// 	TaskID string `gorm:"column:c_task_id;index:idx_node_task;not null" json:"task_id"`
// 	// TaskStatus 任务状态
// 	TaskStatus string `gorm:"column:c_task_status;default:''" json:"task_status"`
// 	// TaskUpdatedAt 任务更新时间
// 	TaskUpdatedAt *time.Time `gorm:"column:c_task_updated_at;type:datetime" json:"task_updated_at"`
// 	// SyncTime 同步时间
// 	SyncTime time.Time `gorm:"column:c_sync_time;type:datetime;not null" json:"sync_time"`
// 	// CreateTime 创建时间
// 	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
// 	// ModifyTime 修改时间
// 	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
// }

// TableName 指定表名
// func (n *NodeTaskSnapshot) TableName() string {
// 	return "t_node_task_snapshot"
// }