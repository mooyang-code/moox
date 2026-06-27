package types

// TaskInstanceLite 轻量级任务实例（用于心跳下发）
type TaskInstanceLite struct {
	ID              int    `json:"id"`
	TaskID          string `json:"task_id"`
	RuleID          string `json:"rule_id"`
	PlannedExecNode string `json:"planned_exec_node"` // 计划执行节点
	DataType        string `json:"data_type"`         // 数据类型
	Symbol          string `json:"symbol"`            // 标的
	Interval        string `json:"interval"`          // 执行周期
	TaskParams      string `json:"task_params"`
	IsDeleted string    `json:"is_deleted"`
}
