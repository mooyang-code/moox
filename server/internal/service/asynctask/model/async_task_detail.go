package model

import (
	"time"
)

// 任务详情状态常量
const (
	TaskDetailStatusPending    = 1 // 待处理
	TaskDetailStatusProcessing = 2 // 处理中
	TaskDetailStatusSuccess    = 3 // 成功
	TaskDetailStatusFailed     = 4 // 失败
)

// AsyncTaskDetail 异步任务详情表
type AsyncTaskDetail struct {
	TaskID       string    `gorm:"column:c_task_id;type:text;primaryKey;not null" json:"task_id"`
	ItemID       string    `gorm:"column:c_item_id;type:text;primaryKey;not null" json:"item_id"`
	ItemName     string    `gorm:"column:c_item_name;type:text;default:''" json:"item_name"`
	Status       int       `gorm:"column:c_status;type:integer;not null;default:1" json:"status"`
	ErrorMessage string    `gorm:"column:c_error_message;type:text" json:"error_message"`
	CreatedAt    time.Time `gorm:"column:c_ctime;autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time `gorm:"column:c_mtime;autoUpdateTime" json:"updated_at"`
}

// TableName 指定表名
func (AsyncTaskDetail) TableName() string {
	return "t_async_task_details"
}

// GetStatusText 获取状态文本
func (d *AsyncTaskDetail) GetStatusText() string {
	switch d.Status {
	case TaskDetailStatusPending:
		return "待处理"
	case TaskDetailStatusProcessing:
		return "处理中"
	case TaskDetailStatusSuccess:
		return "成功"
	case TaskDetailStatusFailed:
		return "失败"
	default:
		return "未知"
	}
}

// IsCompleted 判断详情是否已完成
func (d *AsyncTaskDetail) IsCompleted() bool {
	return d.Status == TaskDetailStatusSuccess || d.Status == TaskDetailStatusFailed
}
