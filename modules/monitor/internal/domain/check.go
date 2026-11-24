package domain

import "time"

// 检查最近一次结果的状态：degraded 按通过记录（不触发也不延续告警），页面显示“需关注”。
const (
	CheckStatusOK       = "ok"
	CheckStatusDegraded = "degraded"
	CheckStatusDown     = "down"
)

// 检查的级别：critical 是服务不可用、数据中断，warning 是还在工作但有风险。
const (
	SeverityCritical = "critical"
	SeverityWarning  = "warning"
)

// Check 是检查表的一行：最近一次结果与告警状态。检查由评估按本轮事实生成，本轮不再产出且没有冻结时当轮删除。
type Check struct {
	SpaceID  string `gorm:"column:c_space_id;primaryKey"`
	CheckID  string `gorm:"column:c_check_id;primaryKey"`
	Name     string `gorm:"column:c_name"`
	Severity string `gorm:"column:c_severity"`
	// 最近一次结果。
	Status    string    `gorm:"column:c_status"`
	Reason    string    `gorm:"column:c_reason"`
	RawError  string    `gorm:"column:c_raw_error"`
	CheckedAt time.Time `gorm:"column:c_checked_at"`
	// 最近一次失败：告警恢复确认期间页面仍显示它。
	FailureReason   string     `gorm:"column:c_failure_reason"`
	FailureRawError string     `gorm:"column:c_failure_raw_error"`
	FailedAt        *time.Time `gorm:"column:c_failed_at"`
	// 连续计数与告警状态。NotifiedAt 为空表示上次推送没有送达，下一轮重发。
	FailureCount int        `gorm:"column:c_failure_count"`
	SuccessCount int        `gorm:"column:c_success_count"`
	AlertStatus  string     `gorm:"column:c_alert_status"`
	TriggeredAt  *time.Time `gorm:"column:c_triggered_at"`
	NotifiedAt   *time.Time `gorm:"column:c_notified_at"`
	CreatedAt    time.Time  `gorm:"column:c_ctime;autoCreateTime:false"`
	UpdatedAt    time.Time  `gorm:"column:c_mtime;autoUpdateTime:false"`
}

func (Check) TableName() string {
	return "t_monitor_checks"
}

// Firing 报告这项检查当前是否在告警。
func (c Check) Firing() bool { return c.AlertStatus == AlertStatusFiring }
