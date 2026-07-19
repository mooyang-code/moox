package domain

import "time"

const (
	AlertStatusOK       = "ok"
	AlertStatusFiring   = "firing"
	AlertStatusResolved = "resolved"

	AlertEventTriggered  = "triggered"
	AlertEventReminder   = "reminder"
	AlertEventResolved   = "resolved"
	AlertEventSendFailed = "send_failed"
)

type WebhookChannel struct {
	ID           int64     `gorm:"column:c_id;primaryKey"`
	SpaceID      string    `gorm:"column:c_space_id"`
	WebhookID    string    `gorm:"column:c_webhook_id"`
	Name         string    `gorm:"column:c_name"`
	URL          string    `gorm:"column:c_url"`
	Method       string    `gorm:"column:c_method"`
	Headers      string    `gorm:"column:c_headers"`
	BodyTemplate string    `gorm:"column:c_body_template"`
	Enabled      bool      `gorm:"column:c_enabled"`
	IsDeleted    bool      `gorm:"column:c_is_deleted"`
	CreatedAt    time.Time `gorm:"column:c_ctime"`
	UpdatedAt    time.Time `gorm:"column:c_mtime"`
}

func (WebhookChannel) TableName() string {
	return "t_monitor_webhooks"
}

type AlertRule struct {
	ID                             int64     `gorm:"column:c_id;primaryKey"`
	SpaceID                        string    `gorm:"column:c_space_id"`
	RuleID                         string    `gorm:"column:c_rule_id"`
	CheckID                        string    `gorm:"column:c_check_id"`
	WebhookID                      string    `gorm:"column:c_webhook_id"`
	FailureThreshold               int       `gorm:"column:c_failure_threshold"`
	SuccessThreshold               int       `gorm:"column:c_success_threshold"`
	MinimumReminderIntervalSeconds int       `gorm:"column:c_minimum_reminder_interval_seconds"`
	SendOnResolved                 bool      `gorm:"column:c_send_on_resolved"`
	Enabled                        bool      `gorm:"column:c_enabled"`
	Description                    string    `gorm:"column:c_description"`
	IsDeleted                      bool      `gorm:"column:c_is_deleted"`
	CreatedAt                      time.Time `gorm:"column:c_ctime"`
	UpdatedAt                      time.Time `gorm:"column:c_mtime"`
}

func (AlertRule) TableName() string {
	return "t_monitor_alert_rules"
}

type AlertState struct {
	ID              int64      `gorm:"column:c_id;primaryKey"`
	SpaceID         string     `gorm:"column:c_space_id"`
	RuleID          string     `gorm:"column:c_rule_id"`
	CheckID         string     `gorm:"column:c_check_id"`
	Status          string     `gorm:"column:c_status"`
	FailureCount    int        `gorm:"column:c_failure_count"`
	SuccessCount    int        `gorm:"column:c_success_count"`
	TriggeredAt     *time.Time `gorm:"column:c_triggered_at"`
	ResolvedAt      *time.Time `gorm:"column:c_resolved_at"`
	LastReminderAt  *time.Time `gorm:"column:c_last_reminder_at"`
	DedupeKey       string     `gorm:"column:c_dedupe_key"`
	CreatedAt       time.Time  `gorm:"column:c_ctime"`
	UpdatedAt       time.Time  `gorm:"column:c_mtime"`
}

func (AlertState) TableName() string {
	return "t_monitor_alert_states"
}

type AlertEvent struct {
	ID              int64     `gorm:"column:c_id;primaryKey"`
	EventID         string    `gorm:"column:c_event_id"`
	SpaceID         string    `gorm:"column:c_space_id"`
	RuleID          string    `gorm:"column:c_rule_id"`
	CheckID         string    `gorm:"column:c_check_id"`
	EventType       string    `gorm:"column:c_event_type"`
	Status          string    `gorm:"column:c_status"`
	Message         string    `gorm:"column:c_message"`
	Payload         string    `gorm:"column:c_payload"`
	CreatedAt       time.Time `gorm:"column:c_created_at"`
}

func (AlertEvent) TableName() string {
	return "t_monitor_alert_events"
}
