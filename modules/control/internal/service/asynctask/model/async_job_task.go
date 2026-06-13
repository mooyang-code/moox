package model

import (
	"time"
)

// AsyncJobTask 异步任务Task表（具体的任务）
type AsyncJobTask struct {
	TaskID        string     `gorm:"column:c_task_id;type:text;uniqueIndex;not null" json:"task_id"`
	JobID         string     `gorm:"column:c_job_id;type:text;not null;index:idx_async_job_tasks_job_id" json:"job_id"`
	TaskType      string     `gorm:"column:c_task_type;type:text;not null" json:"task_type"`
	TaskStatus    int        `gorm:"column:c_task_status;type:integer;not null;default:1;index:idx_async_job_tasks_status" json:"task_status"`
	RequestParams string     `gorm:"column:c_request_params;type:text" json:"request_params"`
	ResultData    string     `gorm:"column:c_result_data;type:text" json:"result_data"`
	ErrorMessage  string     `gorm:"column:c_error_message;type:text" json:"error_message"`
	StartedTime   *time.Time `gorm:"column:c_started_time" json:"started_time"`
	CompletedTime *time.Time `gorm:"column:c_completed_time" json:"completed_time"`
	CreatedAt     time.Time  `gorm:"column:c_ctime;autoCreateTime" json:"created_at"`
	UpdatedAt     time.Time  `gorm:"column:c_mtime;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名
func (t *AsyncJobTask) TableName() string {
	return "t_async_job_tasks"
}

// IsCompleted 判断任务是否已完成
func (t *AsyncJobTask) IsCompleted() bool {
	// 2-成功, 3-失败, 4-部分成功
	return t.TaskStatus == 2 ||
		t.TaskStatus == 3 ||
		t.TaskStatus == 4
}

// GetStatusText 获取状态文本
func (t *AsyncJobTask) GetStatusText() string {
	switch t.TaskStatus {
	case 0: // 待处理
		return "待处理"
	case 1: // 处理中
		return "处理中"
	case 2: // 成功
		return "成功"
	case 3: // 失败
		return "失败"
	case 4: // 部分成功
		return "部分成功"
	default:
		return "未知"
	}
}
