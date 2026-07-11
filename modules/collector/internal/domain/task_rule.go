package domain

import "time"

// TaskRule is the Collector-owned采集规则.
type TaskRule struct {
	ID              int       `gorm:"column:c_id;primaryKey;autoIncrement"`
	SpaceID         string    `gorm:"column:c_space_id"`
	RuleID          string    `gorm:"column:c_rule_id"`
	DataType        string    `gorm:"column:c_data_type"`
	Exchange        string    `gorm:"column:c_exchange"`
	MarketID        string    `gorm:"column:c_market_id"`
	Feed            string    `gorm:"column:c_feed"`
	InstrumentTypes string    `gorm:"column:c_instrument_types"`
	Frequencies     string    `gorm:"column:c_frequencies"`
	HistoryStart    string    `gorm:"column:c_history_start"`
	HistoryEnd      string    `gorm:"column:c_history_end"`
	SubjectFilters  string    `gorm:"column:c_subject_filters"`
	ExchangeFilters string    `gorm:"column:c_exchange_filters"`
	ScheduleSpec    string    `gorm:"column:c_schedule_spec"`
	CollectParams   string    `gorm:"column:c_collect_params"`
	AssignmentType  string    `gorm:"column:c_assignment_type"`
	AssignedNodes   string    `gorm:"column:c_assigned_nodes"`
	NodePattern     string    `gorm:"column:c_node_pattern"`
	NodeTags        string    `gorm:"column:c_node_tags"`
	Enabled         bool      `gorm:"column:c_enabled"`
	Creator         string    `gorm:"column:c_creator"`
	CreateTime      time.Time `gorm:"column:c_ctime"`
	ModifyTime      time.Time `gorm:"column:c_mtime"`
}

// TableName returns the Collector task rule table.
func (r *TaskRule) TableName() string {
	return "t_collector_task_rules"
}
