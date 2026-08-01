package domain

import "time"

// TaskRule is the Collector-owned采集规则.
type TaskRule struct {
	ID            int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID       string    `gorm:"column:c_space_id"`
	RuleID        string    `gorm:"column:c_rule_id"`
	DataType      string    `gorm:"column:c_data_type"`
	Provider      string    `gorm:"column:c_provider"`
	MarketType    string    `gorm:"column:c_market_type"`
	CollectParams string    `gorm:"column:c_collect_params"`
	Enabled       bool      `gorm:"column:c_enabled"`
	Creator       string    `gorm:"column:c_creator"`
	CreateTime    time.Time `gorm:"column:c_ctime"`
	ModifyTime    time.Time `gorm:"column:c_mtime"`
}

// TableName returns the Collector task rule table.
func (r *TaskRule) TableName() string {
	return "t_collector_task_rules"
}
