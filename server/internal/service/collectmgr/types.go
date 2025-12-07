package collectmgr

import (
	"time"
)

// TaskInstanceStatusInfo 任务实例状态信息
type TaskInstanceStatusInfo struct {
	Status  int    `json:"status"`
	Label   string `json:"label"`
	Color   string `json:"color"`
	IsFinal bool   `json:"is_final"`
}

// TaskSummary 任务摘要信息
type TaskSummary struct {
	TaskID             string     `json:"task_id"`
	ProjectID          string     `json:"project_id"`
	DatasetID          string     `json:"dataset_id"`
	TaskType           string     `json:"task_type"`
	CollectorType      string     `json:"collector_type"`
	SourceName         string     `json:"source_name"`
	Enabled            string     `json:"enabled"`
	Priority           int        `json:"priority"`
	InstanceCount      int64      `json:"instance_count"`
	SuccessCount       int64      `json:"success_count"`
	FailedCount        int64      `json:"failed_count"`
	RunningCount       int64      `json:"running_count"`
	PendingCount       int64      `json:"pending_count"`
	LatestInstanceTime *time.Time `json:"latest_instance_time"`
	LastDispatchTime   *time.Time `json:"last_dispatch_time"`
	CreateTime         time.Time  `json:"create_time"`
}

// TaskExecutionReport 任务执行报告
type TaskExecutionReport struct {
	TaskID          string                  `json:"task_id"`
	ReportTime      time.Time               `json:"report_time"`
	Period          string                  `json:"period"`
	Summary         TaskSummary             `json:"summary"`
	InstanceDetails []*TaskInstanceDetail   `json:"instance_details"`
	Statistics      TaskExecutionStatistics `json:"statistics"`
}

// TaskInstanceDetail 任务实例详情
type TaskInstanceDetail struct {
	InstanceID    string                 `json:"instance_id"`
	TaskID        string                 `json:"task_id"`
	NodeID        string                 `json:"node_id"`
	Status        int                    `json:"status"`
	StatusInfo    TaskInstanceStatusInfo `json:"status_info"`
	StartTime     *time.Time             `json:"start_time"`
	EndTime       *time.Time             `json:"end_time"`
	Duration      int64                  `json:"duration"` // 执行时长（毫秒）
	TargetObjects string                 `json:"target_objects"`
	Result        string                 `json:"result"`
	CreateTime    time.Time              `json:"create_time"`
}

// TaskExecutionStatistics 任务执行统计
type TaskExecutionStatistics struct {
	TotalInstances     int64            `json:"total_instances"`
	SuccessRate        float64          `json:"success_rate"`        // 成功率
	AverageDuration    int64            `json:"average_duration"`    // 平均执行时长（毫秒）
	MaxDuration        int64            `json:"max_duration"`        // 最大执行时长（毫秒）
	MinDuration        int64            `json:"min_duration"`        // 最小执行时长（毫秒）
	NodeDistribution   map[string]int64 `json:"node_distribution"`   // 节点分布
	StatusDistribution map[string]int64 `json:"status_distribution"` // 状态分布
}

// TaskFilter 任务过滤条件
type TaskFilter struct {
	ProjectID      string     `form:"project_id"`
	DatasetID      string     `form:"dataset_id"`
	TaskType       string     `form:"task_type"`
	CollectorType  string     `form:"collector_type"`
	SourceName     string     `form:"source_name"`
	Enabled        string     `form:"enabled"`
	TaskIDs        []string   `form:"task_ids"`
	PriorityMin    *int       `form:"priority_min"`
	PriorityMax    *int       `form:"priority_max"`
	CreatedAfter   *time.Time `form:"created_after"`
	CreatedBefore  *time.Time `form:"created_before"`
	ModifiedAfter  *time.Time `form:"modified_after"`
	ModifiedBefore *time.Time `form:"modified_before"`
	HasDispatched  *bool      `form:"has_dispatched"`
	Keyword        string     `form:"keyword"`
}

// InstanceFilter 实例过滤条件
type InstanceFilter struct {
	TaskID        string     `form:"task_id"`
	ProjectID     string     `form:"project_id"`
	DatasetID     string     `form:"dataset_id"`
	NodeID        string     `form:"node_id"`
	Status        []int      `form:"status"`
	CreatedAfter  *time.Time `form:"created_after"`
	CreatedBefore *time.Time `form:"created_before"`
	StartedAfter  *time.Time `form:"started_after"`
	StartedBefore *time.Time `form:"started_before"`
	EndedAfter    *time.Time `form:"ended_after"`
	EndedBefore   *time.Time `form:"ended_before"`
	DurationMin   *int64     `form:"duration_min"`
	DurationMax   *int64     `form:"duration_max"`
	Keyword       string     `form:"keyword"`
	Limit         int        `form:"limit,default=50"`
	Offset        int        `form:"offset,default=0"`
}

// TaskCreateRequest 任务创建请求
type TaskCreateRequest struct {
	ProjectID           string `json:"project_id" binding:"required"`
	DatasetID           string `json:"dataset_id" binding:"required"`
	TaskType            string `json:"task_type" binding:"required"`
	CollectorType       string `json:"collector_type" binding:"required"`
	SourceName          string `json:"source_name" binding:"required"`
	AssignmentType      string `json:"assignment_type" binding:"required"`
	AssignedNodes       string `json:"assigned_nodes"`
	NodePattern         string `json:"node_pattern"`
	LoadBalanceStrategy string `json:"load_balance_strategy"`
	TargetObjects       string `json:"target_objects"`
	ObjectPattern       string `json:"object_pattern"`
	ForceObjects        string `json:"force_objects"`
	CollectParams       string `json:"collect_params"`
	ScheduleConfig      string `json:"schedule_config"`
	Enabled             string `json:"enabled"`
	Priority            int    `json:"priority"`
}

// TaskUpdateRequest 任务更新请求
type TaskUpdateRequest struct {
	ProjectID           string `json:"project_id"`
	DatasetID           string `json:"dataset_id"`
	TaskType            string `json:"task_type"`
	CollectorType       string `json:"collector_type"`
	SourceName          string `json:"source_name"`
	AssignmentType      string `json:"assignment_type"`
	AssignedNodes       string `json:"assigned_nodes"`
	NodePattern         string `json:"node_pattern"`
	LoadBalanceStrategy string `json:"load_balance_strategy"`
	TargetObjects       string `json:"target_objects"`
	ObjectPattern       string `json:"object_pattern"`
	ForceObjects        string `json:"force_objects"`
	CollectParams       string `json:"collect_params"`
	ScheduleConfig      string `json:"schedule_config"`
	Enabled             string `json:"enabled"`
	Priority            int    `json:"priority"`
}

// TaskDispatchRequest 任务分发请求
type TaskDispatchRequest struct {
	TaskID          string              `json:"task_id" binding:"required"`
	NodeAssignments map[string][]string `json:"node_assignments" binding:"required"`
	ForceDispatch   bool                `json:"force_dispatch"`
	Async           bool                `json:"async"`
}

// TaskSyncRequest 任务同步请求
type TaskSyncRequest struct {
	TaskID   string `json:"task_id" binding:"required"`
	NodeID   string `json:"node_id" binding:"required"`
	SyncType string `json:"sync_type" binding:"required,oneof=config runtime all"`
}

// BatchOperationRequest 批量操作请求
type BatchOperationRequest struct {
	TaskIDs []string `json:"task_ids" binding:"required"`
	Action  string   `json:"action" binding:"required,oneof=enable disable delete"`
	Reason  string   `json:"reason"`
}

// APIResponse API响应结构
type APIResponse struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
	Total   int64       `json:"total,omitempty"`
	Time    time.Time   `json:"time"`
}

// SuccessResponse 成功响应
func SuccessResponse(message string, data interface{}) *APIResponse {
	return &APIResponse{
		Code:    0,
		Message: message,
		Data:    data,
		Time:    time.Now(),
	}
}

// ErrorResponse 错误响应
func ErrorResponse(code int, message string) *APIResponse {
	return &APIResponse{
		Code:    code,
		Message: message,
		Time:    time.Now(),
	}
}

// PaginatedResponse 分页响应
func PaginatedResponse(message string, data interface{}, total int64) *APIResponse {
	return &APIResponse{
		Code:    0,
		Message: message,
		Data:    data,
		Total:   total,
		Time:    time.Now(),
	}
}

// ========== 任务规划器相关类型 ==========

// SyncResult 单规则同步结果
type SyncResult struct {
	RuleID    string `json:"rule_id"`
	Created   int    `json:"created"`   // 新建实例数
	Updated   int    `json:"updated"`   // 更新实例数
	Deleted   int    `json:"deleted"`   // 删除实例数
	Unchanged int    `json:"unchanged"` // 未变化数
}

// BatchSyncResult 批量同步结果
type BatchSyncResult struct {
	TotalRules   int     `json:"total_rules"`
	SyncedRules  int     `json:"synced_rules"`
	FailedRules  int     `json:"failed_rules"`
	TotalCreated int     `json:"total_created"`
	TotalUpdated int     `json:"total_updated"`
	TotalDeleted int     `json:"total_deleted"`
	Errors       []error `json:"-"`
}
