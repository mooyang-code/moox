package api

import (
	"github.com/mooyang-code/moox/server/internal/common"
	"github.com/mooyang-code/moox/server/internal/service/asynctask"
)

type AsyncTaskResponse = common.UnifiedAPIResponse

// 重新导出types包中的类型

type TaskRequest = asynctask.TaskRequest
type JobQueryResult = asynctask.JobQueryResult
type TaskQueryResult = asynctask.TaskQueryResult

// ========== API专用类型 ==========

// AsyncTaskStatusResponse 任务状态响应
type AsyncTaskStatusResponse struct {
	TaskID        string                `json:"task_id"`
	TaskType      string                `json:"task_type"`
	TaskStatus    int                   `json:"task_status"`
	TotalCount    int                   `json:"total_count"`
	SuccessCount  int                   `json:"success_count"`
	FailedCount   int                   `json:"failed_count"`
	Progress      int                   `json:"progress"`
	ErrorMessage  string                `json:"error_message,omitempty"`
	CreatedAt     string                `json:"created_at"`
	CompletedTime string                `json:"completed_time,omitempty"`
	FailedItems   []AsyncTaskDetailItem `json:"failed_items,omitempty"`
}

// AsyncTaskDetailItem 任务详情项
type AsyncTaskDetailItem struct {
	ItemID       string `json:"item_id"`
	ItemName     string `json:"item_name"`
	Status       int    `json:"status"`
	ErrorMessage string `json:"error_message,omitempty"`
}
