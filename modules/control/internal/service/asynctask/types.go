package asynctask

import (
	"github.com/mooyang-code/moox/modules/control/internal/service/asynctask/model"
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
	TaskTypeCreateNode      = "CREATE_NODE"        // 创建节点
	TaskTypeDeleteNode      = "DELETE_NODE"        // 删除节点
	TaskTypeDeployNode      = "DEPLOY_NODE"        // 部署节点
	TaskTypeUploadFileToCOS = "UPLOAD_FILE_TO_COS" // 文件上传到COS
)

// TaskRequest 任务请求
type TaskRequest struct {
	TaskType      string `json:"task_type"`      // 任务类型
	RequestParams string `json:"request_params"` // 请求参数（JSON字符串）
}

// JobQueryResult Job查询结果
type JobQueryResult struct {
	JobID          string             `json:"job_id"`
	TotalTaskCnt   int                `json:"total_task_cnt"`
	SuccessTaskCnt int                `json:"success_task_cnt"`
	FailedTaskCnt  int                `json:"failed_task_cnt"`
	IsStarted      int                `json:"is_started"`
	JobStatus      int                `json:"job_status"`      // 计算得出的状态
	JobStatusText  string             `json:"job_status_text"` // 状态文本
	Progress       int                `json:"progress"`        // 进度百分比
	CreatedAt      string             `json:"created_at"`
	UpdatedAt      string             `json:"updated_at"`
	Tasks          []*TaskQueryResult `json:"tasks,omitempty"` // 任务详情列表（可选）
}

// TaskQueryResult Task查询结果
type TaskQueryResult struct {
	TaskID         string `json:"task_id"`
	JobID          string `json:"job_id"`
	TaskType       string `json:"task_type"`
	TaskStatus     int    `json:"task_status"`
	TaskStatusText string `json:"task_status_text"`
	RequestParams  string `json:"request_params,omitempty"`
	ResultData     string `json:"result_data,omitempty"`
	ErrorMessage   string `json:"error_message,omitempty"`
	StartedTime    string `json:"started_time,omitempty"`
	CompletedTime  string `json:"completed_time,omitempty"`
	CreatedAt      string `json:"created_at"`
}

// BuildJobQueryResult 构建JobQueryResult
func BuildJobQueryResult(job *model.AsyncJob, tasks []*model.AsyncJobTask) *JobQueryResult {
	result := &JobQueryResult{
		JobID:          job.JobID,
		TotalTaskCnt:   job.TotalTaskCnt,
		SuccessTaskCnt: job.SuccessTaskCnt,
		FailedTaskCnt:  job.FailedTaskCnt,
		IsStarted:      job.IsStarted,
		JobStatus:      job.CalculateStatus(),
		JobStatusText:  job.GetStatusText(),
		Progress:       job.GetProgress(),
		CreatedAt:      job.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:      job.UpdatedAt.Format("2006-01-02 15:04:05"),
	}

	if len(tasks) > 0 {
		result.Tasks = make([]*TaskQueryResult, len(tasks))
		for i, task := range tasks {
			result.Tasks[i] = BuildTaskQueryResult(task)
		}
	}
	return result
}

// BuildTaskQueryResult 构建TaskQueryResult
func BuildTaskQueryResult(task *model.AsyncJobTask) *TaskQueryResult {
	result := &TaskQueryResult{
		TaskID:         task.TaskID,
		JobID:          task.JobID,
		TaskType:       task.TaskType,
		TaskStatus:     task.TaskStatus,
		TaskStatusText: task.GetStatusText(),
		ResultData:     task.ResultData,
		ErrorMessage:   task.ErrorMessage,
		CreatedAt:      task.CreatedAt.Format("2006-01-02 15:04:05"),
	}

	if task.StartedTime != nil {
		result.StartedTime = task.StartedTime.Format("2006-01-02 15:04:05")
	}
	if task.CompletedTime != nil {
		result.CompletedTime = task.CompletedTime.Format("2006-01-02 15:04:05")
	}
	return result
}
