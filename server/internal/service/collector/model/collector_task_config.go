package model

import (
	"time"
)

// CollectorTaskConfig 采集任务配置表
type CollectorTaskConfig struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// TaskID 任务唯一标识
	TaskID string `gorm:"column:c_task_id;uniqueIndex:idx_task_id;not null" json:"task_id"`
	// ProjectID 项目ID（关联到项目表）
	ProjectID string `gorm:"column:c_project_id;index:idx_project_dataset;not null" json:"project_id"`
	// DatasetID 数据集ID（关联到数据集表）
	DatasetID string `gorm:"column:c_dataset_id;index:idx_project_dataset;not null" json:"dataset_id"`
	// TaskType 任务类型（object_list=对象列表采集，data_collect=数据采集）
	TaskType string `gorm:"column:c_task_type;index:idx_task_type;not null" json:"task_type"`
	// CollectorType 采集器类型（kline/ticker/orderbook/trade/news等）
	CollectorType string `gorm:"column:c_collector_type;index:idx_collector_type;not null" json:"collector_type"`
	// SourceName 数据源名称（binance/okx等）
	SourceName string `gorm:"column:c_source_name;index:idx_source_name;not null;default:''" json:"source_name"`
	
	// AssignmentType 分配类型（auto=自动分配，fixed=固定节点，pattern=通配符匹配）
	AssignmentType string `gorm:"column:c_assignment_type;index:idx_assignment_type;not null;default:'auto'" json:"assignment_type"`
	// AssignedNodes 指定节点列表（JSON数组，fixed类型时使用）
	AssignedNodes string `gorm:"column:c_assigned_nodes;type:text;not null;default:'[]'" json:"assigned_nodes"`
	// NodePattern 节点匹配模式（pattern类型时使用，如：scf-collector-*）
	NodePattern string `gorm:"column:c_node_pattern;not null;default:''" json:"node_pattern"`
	// LoadBalanceStrategy 负载均衡策略（round_robin/least_load/random）
	LoadBalanceStrategy string `gorm:"column:c_load_balance_strategy;not null;default:'round_robin'" json:"load_balance_strategy"`
	
	// TargetObjects 目标对象列表（JSON数组，如交易对列表）
	TargetObjects string `gorm:"column:c_target_objects;type:text;not null;default:'[]'" json:"target_objects"`
	// ObjectPattern 对象匹配模式（支持通配符，如：*USDT）
	ObjectPattern string `gorm:"column:c_object_pattern;not null;default:''" json:"object_pattern"`
	// ForceObjects 强制指定对象（JSON：{node_id:[objects]}）
	ForceObjects string `gorm:"column:c_force_objects;type:text;not null;default:'{}'" json:"force_objects"`
	
	// CollectParams 采集参数（JSON：{intervals:["1m","5m"],depth:20}）
	CollectParams string `gorm:"column:c_collect_params;type:text;not null;default:'{}'" json:"collect_params"`
	// ScheduleConfig 调度配置（JSON：{cron:"*/5 * * * *",retry:3,timeout:300}）
	ScheduleConfig string `gorm:"column:c_schedule_config;type:text;not null;default:'{}'" json:"schedule_config"`
	
	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `gorm:"column:c_enabled;index:idx_enabled_priority;not null;default:'true'" json:"enabled"`
	// Priority 优先级（数值越大优先级越高）
	Priority int `gorm:"column:c_priority;index:idx_enabled_priority;not null;default:0" json:"priority"`
	// LastDispatchTime 最后分发时间
	LastDispatchTime *time.Time `gorm:"column:c_last_dispatch_time;type:datetime" json:"last_dispatch_time"`
	// LastDispatchResult 最后分发结果
	LastDispatchResult string `gorm:"column:c_last_dispatch_result;type:text;not null;default:''" json:"last_dispatch_result"`
	
	// Invalid 删除标记
	Invalid int `gorm:"column:c_invalid;not null;default:0" json:"invalid"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (c *CollectorTaskConfig) TableName() string {
	return "t_collector_task_config"
}