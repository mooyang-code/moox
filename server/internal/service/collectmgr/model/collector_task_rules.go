package model

import (
	"time"
)

// CollectorTaskRules 采集任务规则配置表
type CollectorTaskRules struct {
	// ID 主键ID
	ID int `gorm:"primaryKey;column:c_id;autoIncrement" json:"id"`
	// RuleID 规则ID
	RuleID string `gorm:"column:c_rule_id;uniqueIndex:idx_rule_id;not null" json:"rule_id"`
	// DataType 数据类型（kline/ticker/orderbook/trade/news/list等）
	DataType string `gorm:"column:c_data_type;index:idx_data_type;not null" json:"data_type"`
	// DataSource 数据源名称（binance/okx等）
	DataSource string `gorm:"column:c_data_source;index:idx_data_source;not null;default:''" json:"data_source"`
	// CollectParams 采集参数（JSON：{intervals:["1m","5m"],depth:20, objects:["BTC-USDT","ETH-USDT"]}）
	CollectParams string `gorm:"column:c_collect_params;type:text;not null;default:'{}'" json:"collect_params"`

	// AssignmentType 分配类型（auto=自动分配，fixed=固定节点，pattern=通配符匹配，tag=标签匹配）
	AssignmentType string `gorm:"column:c_assignment_type;type:text;not null;default:'auto'" json:"assignment_type"`
	// AssignedNodes 指定节点列表（JSON数组，fixed类型时使用）
	AssignedNodes string `gorm:"column:c_assigned_nodes;type:text;not null;default:'[]'" json:"assigned_nodes"`
	// NodePattern 节点匹配模式（pattern类型时使用，如：scf-collector-*）
	NodePattern string `gorm:"column:c_node_pattern;not null;default:''" json:"node_pattern"`
	// NodeTags 节点标签列表（JSON数组，tag类型时使用，如：["国内","海外"]）
	NodeTags string `gorm:"column:c_node_tags;type:text;not null;default:'[]'" json:"node_tags"`

	// Enabled 是否启用（"true"=启用，"false"=禁用）
	Enabled string `gorm:"column:c_enabled;index:idx_enabled_priority;not null;default:'true'" json:"enabled"`
	// Creator 创建人
	Creator string `gorm:"column:c_creator;not null;default:''" json:"creator"`
	// CreateTime 创建时间
	CreateTime time.Time `gorm:"column:c_ctime;type:datetime;default:CURRENT_TIMESTAMP" json:"create_time"`
	// ModifyTime 修改时间
	ModifyTime time.Time `gorm:"column:c_mtime;type:datetime;default:CURRENT_TIMESTAMP" json:"modify_time"`
}

// TableName 指定表名
func (c *CollectorTaskRules) TableName() string {
	return "t_collector_task_rules"
}
