package model

import (
	"time"
)

// AsyncJob 异步任务Job表（一次提交的批次）
type AsyncJob struct {
	JobID          string    `gorm:"column:c_job_id;type:text;uniqueIndex;not null" json:"job_id"`
	RequestParams  string    `gorm:"column:c_request_params;type:text" json:"request_params"`
	TotalTaskCnt   int       `gorm:"column:c_total_task_cnt;type:integer;default:0" json:"total_task_cnt"`
	SuccessTaskCnt int       `gorm:"column:c_success_task_cnt;type:integer;default:0" json:"success_task_cnt"`
	FailedTaskCnt  int       `gorm:"column:c_failed_task_cnt;type:integer;default:0" json:"failed_task_cnt"`
	IsStarted      int       `gorm:"column:c_is_started;type:integer;default:0" json:"is_started"`
	CreatedAt      time.Time `gorm:"column:c_ctime;autoCreateTime" json:"created_at"`
	UpdatedAt      time.Time `gorm:"column:c_mtime;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名
func (j *AsyncJob) TableName() string {
	return "t_async_jobs"
}

// GetProgress 获取任务进度百分比
func (j *AsyncJob) GetProgress() int {
	if j.TotalTaskCnt == 0 {
		return 0
	}
	completed := j.SuccessTaskCnt + j.FailedTaskCnt
	return completed * 100 / j.TotalTaskCnt
}

// CalculateStatus 基于计数器计算Job状态
// 返回: 0-待处理, 1-处理中, 2-成功, 3-失败, 4-部分成功
func (j *AsyncJob) CalculateStatus() int {
	completed := j.SuccessTaskCnt + j.FailedTaskCnt

	// 所有任务完成
	if completed == j.TotalTaskCnt {
		if j.FailedTaskCnt == 0 {
			return 2 // 全部成功
		}
		if j.SuccessTaskCnt == 0 {
			return 3 // 全部失败
		}
		return 4 // 部分成功
	}

	// 已启动但未完成
	if j.IsStarted == 1 {
		return 1 // 处理中
	}

	// 未启动
	return 0 // 待处理
}

// IsCompleted 判断Job是否已完成
func (j *AsyncJob) IsCompleted() bool {
	return (j.SuccessTaskCnt + j.FailedTaskCnt) == j.TotalTaskCnt
}

// GetStatusText 获取状态文本
func (j *AsyncJob) GetStatusText() string {
	status := j.CalculateStatus()
	switch status {
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
