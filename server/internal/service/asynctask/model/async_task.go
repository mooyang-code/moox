package model

import (
	"time"
)

// 任务状态常量
const (
	TaskStatusPending    = 0 // 待处理
	TaskStatusProcessing = 1 // 处理中
	TaskStatusRunning    = 1 // 运行中（与处理中相同）
	TaskStatusSuccess    = 2 // 成功
	TaskStatusFailed     = 3 // 失败
	TaskStatusPartial    = 4 // 部分成功
	TaskStatusCancelled  = 5 // 已取消
)

// 任务类型常量
const (
	TaskTypeBatchCreateNode = "BATCH_CREATE_NODE" // 批量创建节点
	TaskTypeBatchUpdateNode = "BATCH_UPDATE_NODE" // 批量更新节点
	TaskTypeBatchDeleteNode = "BATCH_DELETE_NODE" // 批量删除节点
	TaskTypeBatchDeployNode = "BATCH_DEPLOY_NODE" // 批量部署节点
)

// AsyncTask 异步任务表
type AsyncTask struct {
	TaskID        string     `gorm:"column:c_task_id;type:text;uniqueIndex;not null" json:"task_id"`
	TaskType      string     `gorm:"column:c_task_type;type:text;not null" json:"task_type"`
	TaskStatus    int        `gorm:"column:c_task_status;type:integer;not null;default:1" json:"task_status"`
	TotalCount    int        `gorm:"column:c_total_count;type:integer;default:0" json:"total_count"`
	SuccessCount  int        `gorm:"column:c_success_count;type:integer;default:0" json:"success_count"`
	FailedCount   int        `gorm:"column:c_failed_count;type:integer;default:0" json:"failed_count"`
	RequestParams string     `gorm:"column:c_request_params;type:text" json:"request_params"`
	ResultData    string     `gorm:"column:c_result_data;type:text" json:"result_data"`
	ErrorMessage  string     `gorm:"column:c_error_message;type:text" json:"error_message"`
	StartedTime   *time.Time `gorm:"column:c_started_time" json:"started_time"`
	CompletedTime *time.Time `gorm:"column:c_completed_time" json:"completed_time"`
	CreatedAt     time.Time  `gorm:"column:c_ctime;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:c_mtime;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名
func (AsyncTask) TableName() string {
	return "t_async_tasks"
}

// GetProgress 获取任务进度百分比
func (t *AsyncTask) GetProgress() int {
	if t.TotalCount == 0 {
		return 0
	}
	completed := t.SuccessCount + t.FailedCount
	return completed * 100 / t.TotalCount
}

// IsCompleted 判断任务是否已完成
func (t *AsyncTask) IsCompleted() bool {
	return t.TaskStatus != TaskStatusProcessing
}

// GetStatusText 获取状态文本
func (t *AsyncTask) GetStatusText() string {
	switch t.TaskStatus {
	case TaskStatusProcessing:
		return "处理中"
	case TaskStatusSuccess:
		return "成功"
	case TaskStatusFailed:
		return "失败"
	case TaskStatusPartial:
		return "部分成功"
	default:
		return "未知"
	}
}

// GetTaskTypeText 获取任务类型文本
func (t *AsyncTask) GetTaskTypeText() string {
	switch t.TaskType {
	case TaskTypeBatchCreateNode:
		return "批量创建节点"
	case TaskTypeBatchUpdateNode:
		return "批量更新节点"
	case TaskTypeBatchDeleteNode:
		return "批量删除节点"
	case TaskTypeBatchDeployNode:
		return "批量部署节点"
	default:
		return "未知任务类型"
	}
}
