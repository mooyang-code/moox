package domain

import "time"

// NotificationChannel 是一个告警推送渠道（企业微信或飞书群机器人）；告警发给全部渠道。
type NotificationChannel struct {
	ID          int64     `gorm:"column:c_id;primaryKey"`
	ChannelID   string    `gorm:"column:c_channel_id"`
	ChannelType string    `gorm:"column:c_channel_type"`
	WebhookURL  string    `gorm:"column:c_webhook_url"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (NotificationChannel) TableName() string { return "t_monitor_notification_channels" }
