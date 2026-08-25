package domain

import "time"

const GlobalNotificationChannelID = "global"

type NotificationChannel struct {
	ID          int64     `gorm:"column:c_id;primaryKey"`
	ChannelID   string    `gorm:"column:c_channel_id"`
	ChannelType string    `gorm:"column:c_channel_type"`
	WebhookURL  string    `gorm:"column:c_webhook_url"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (NotificationChannel) TableName() string { return "t_monitor_notification_channels" }
