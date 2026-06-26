package dto

import "time"

// TaskRuleDTO 任务规则数据传输对象
type TaskRuleDTO struct {
	ID             int       `json:"id"`
	SpaceID        string    `json:"space_id"`
	RuleID         string    `json:"rule_id"`
	BizType        string    `json:"biz_type"`
	DataType       string    `json:"data_type"`
	DataSource     string    `json:"data_source"`
	CollectParams  string    `json:"collect_params"`
	AssignmentType string    `json:"assignment_type"`
	AssignedNodes  string    `json:"assigned_nodes"`
	NodePattern    string    `json:"node_pattern"`
	NodeTags       string    `json:"node_tags"`
	Enabled        string    `json:"enabled"`
	Creator        string    `json:"creator"`
	CreateTime     time.Time `json:"create_time"`
	ModifyTime     time.Time `json:"modify_time"`
}
