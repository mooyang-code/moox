package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

// ErrNotificationChannelExists 表示同一个 Webhook URL 已经添加过。
var ErrNotificationChannelExists = errors.New("该 Webhook URL 已经添加过")

type NotificationRepository struct{ db *gorm.DB }

func NewNotificationRepository(db *gorm.DB) *NotificationRepository {
	return &NotificationRepository{db: db}
}

// List 按添加顺序返回全部通知渠道。
func (r *NotificationRepository) List(ctx context.Context) ([]domain.NotificationChannel, error) {
	var channels []domain.NotificationChannel
	if err := r.db.WithContext(ctx).Order("c_id ASC").Find(&channels).Error; err != nil {
		return nil, err
	}
	return channels, nil
}

// Add 新增一个渠道并返回它。URL 的格式与平台域名由调用方校验，这里只保证类型合法、URL 不重复。
func (r *NotificationRepository) Add(ctx context.Context, channelType, webhookURL string) (*domain.NotificationChannel, error) {
	channelType, webhookURL = strings.TrimSpace(channelType), strings.TrimSpace(webhookURL)
	if err := validateNotificationType(channelType); err != nil {
		return nil, err
	}
	if webhookURL == "" {
		return nil, errors.New("Webhook URL 不能为空")
	}
	var existing int64
	if err := r.db.WithContext(ctx).Model(&domain.NotificationChannel{}).Where("c_webhook_url = ?", webhookURL).Count(&existing).Error; err != nil {
		return nil, err
	}
	if existing > 0 {
		return nil, ErrNotificationChannelExists
	}
	id, err := newNotificationChannelID()
	if err != nil {
		return nil, err
	}
	channel := domain.NotificationChannel{ChannelID: id, ChannelType: channelType, WebhookURL: webhookURL}
	if err := r.db.WithContext(ctx).Create(&channel).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

// Delete 删除一个渠道；不存在时返回 gorm.ErrRecordNotFound。
func (r *NotificationRepository) Delete(ctx context.Context, channelID string) error {
	result := r.db.WithContext(ctx).Where("c_channel_id = ?", strings.TrimSpace(channelID)).Delete(&domain.NotificationChannel{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func newNotificationChannelID() (string, error) {
	var raw [6]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("生成通知渠道 ID 失败：%w", err)
	}
	return "nc_" + hex.EncodeToString(raw[:]), nil
}

func validateNotificationType(value string) error {
	switch strings.TrimSpace(value) {
	case "wecom", "feishu":
		return nil
	default:
		return fmt.Errorf("unsupported notification channel type %q", value)
	}
}
