package domain

import "time"

const (
	AlertStatusOK     = "ok"
	AlertStatusFiring = "firing"

	AlertEventTriggered  = "triggered"
	AlertEventReminder   = "reminder"
	AlertEventResolved   = "resolved"
	AlertEventSendFailed = "send_failed"
)

// AlertEvent 是一条告警事件。标题与级别抄自写事件时的检查，检查删除后告警经过与静默汇总仍能显示。
type AlertEvent struct {
	ID        int64     `gorm:"column:c_id;primaryKey"`
	EventID   string    `gorm:"column:c_event_id"`
	SpaceID   string    `gorm:"column:c_space_id"`
	CheckID   string    `gorm:"column:c_check_id"`
	Title     string    `gorm:"column:c_title"`
	Severity  string    `gorm:"column:c_severity"`
	EventType string    `gorm:"column:c_event_type"`
	Message   string    `gorm:"column:c_message"`
	Payload   string    `gorm:"column:c_payload"`
	CreatedAt time.Time `gorm:"column:c_created_at"`
}

func (AlertEvent) TableName() string {
	return "t_monitor_alert_events"
}
