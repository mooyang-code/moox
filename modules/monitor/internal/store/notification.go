package store

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type NotificationRepository struct{ db *gorm.DB }

func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

func (r *NotificationRepository) GetGlobal(ctx context.Context) (*domain.NotificationChannel, error) {
	var channel domain.NotificationChannel
	err := r.db.WithContext(ctx).Where("c_channel_id = ?", domain.GlobalNotificationChannelID).First(&channel).Error
	if err != nil {
		return nil, err
	}
	return &channel, nil
}

func (r *NotificationRepository) SeedIfAbsent(ctx context.Context, channel domain.NotificationChannel) error {
	if channel.ChannelID == "" {
		channel.ChannelID = domain.GlobalNotificationChannelID
	}
	if channel.ChannelID != domain.GlobalNotificationChannelID {
		return fmt.Errorf("notification channel id must be %q", domain.GlobalNotificationChannelID)
	}
	if err := validateNotificationType(channel.ChannelType); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&channel).Error
}

func (r *NotificationRepository) UpdateGlobal(ctx context.Context, channelType, webhookURL string) error {
	if err := validateNotificationType(channelType); err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&domain.NotificationChannel{}).
		Where("c_channel_id = ?", domain.GlobalNotificationChannelID).
		Updates(map[string]any{"c_channel_type": channelType, "c_webhook_url": strings.TrimSpace(webhookURL)}).Error
}

func validateNotificationType(value string) error {
	switch strings.TrimSpace(value) {
	case "wecom", "feishu":
		return nil
	default:
		return fmt.Errorf("unsupported notification channel type %q", value)
	}
}
