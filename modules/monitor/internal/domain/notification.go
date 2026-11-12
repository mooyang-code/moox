package domain

import "time"

// NotificationChannel 是一个告警推送渠道（企业微信、飞书、钉钉群机器人或通用 Webhook）；告警发给全部渠道。
// Secret 选填：钉钉的加签密钥或通用 Webhook 的签名密钥，只下发给发送方，不通过列表接口返回。
type NotificationChannel struct {
	ID          int64     `gorm:"column:c_id;primaryKey"`
	ChannelID   string    `gorm:"column:c_channel_id"`
	ChannelType string    `gorm:"column:c_channel_type"`
	WebhookURL  string    `gorm:"column:c_webhook_url"`
	Secret      string    `gorm:"column:c_secret"`
	CreatedAt   time.Time `gorm:"column:c_ctime"`
	UpdatedAt   time.Time `gorm:"column:c_mtime"`
}

func (NotificationChannel) TableName() string { return "t_monitor_notification_channels" }
