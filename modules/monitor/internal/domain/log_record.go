package domain

import "time"

// LogRecord 是一条重要日志：某个服务的 Error 及以上级别日志，或它显式标出的关键事件。
type LogRecord struct {
	ID int64 `gorm:"column:c_id;primaryKey"`
	// RecordKey 是“实例 | 启动号 | 序号”，同一条日志重复投递时只保留一条。
	RecordKey  string `gorm:"column:c_record_key"`
	HostID     string `gorm:"column:c_host_id"`
	Component  string `gorm:"column:c_component"`
	InstanceID string `gorm:"column:c_instance_id"`
	Level      string `gorm:"column:c_level"`
	// Event 是关键事件的名称；普通的错误日志为空。
	Event   string `gorm:"column:c_event"`
	Message string `gorm:"column:c_message"`
	// Caller 是打日志的代码位置。
	Caller string `gorm:"column:c_caller"`
	// Fields 是日志带的结构化字段（JSON 对象，值都是字符串）。
	Fields string `gorm:"column:c_fields"`
	// MergedCount 大于 0 表示在这条之前，同一代码位置另有这么多条日志因为太密被合并掉了。
	MergedCount int       `gorm:"column:c_merged_count"`
	LoggedAt    time.Time `gorm:"column:c_logged_at"`
	CreatedAt   time.Time `gorm:"column:c_ctime;autoCreateTime"`
}

func (LogRecord) TableName() string { return "t_monitor_log_records" }
