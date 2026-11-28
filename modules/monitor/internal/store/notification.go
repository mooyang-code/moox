package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/notification"
	"gorm.io/gorm"
)

// NotificationChannelInput 是批量导入通知渠道时的单条输入。
type NotificationChannelInput struct {
	ChannelType string
	WebhookURL  string
	Secret      string
}

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

// Add 新增一个渠道并返回它。URL 的格式、平台域名与密钥由调用方校验，这里只保证类型合法、URL 不重复。
func (r *NotificationRepository) Add(ctx context.Context, channelType, webhookURL, secret string) (*domain.NotificationChannel, error) {
	channelType, webhookURL, secret = strings.TrimSpace(channelType), strings.TrimSpace(webhookURL), strings.TrimSpace(secret)
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
	channel := domain.NotificationChannel{ChannelID: id, ChannelType: channelType, WebhookURL: webhookURL, Secret: secret}
	if err := r.db.WithContext(ctx).Create(&channel).Error; err != nil {
		return nil, err
	}
	return &channel, nil
}

// Import 在单个事务内按 webhook_url 幂等导入通知渠道：不存在则新建，已存在则覆盖类型与密钥并保留原 channel_id。
// URL 的格式、平台域名与密钥规则由调用方校验，这里保证类型合法、URL 非空且同批内不重复。
func (r *NotificationRepository) Import(ctx context.Context, items []NotificationChannelInput) (created, updated int, err error) {
	normalized := make([]NotificationChannelInput, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for i, raw := range items {
		item := NotificationChannelInput{
			ChannelType: strings.TrimSpace(raw.ChannelType),
			WebhookURL:  strings.TrimSpace(raw.WebhookURL),
			Secret:      strings.TrimSpace(raw.Secret),
		}
		if err := validateNotificationType(item.ChannelType); err != nil {
			return 0, 0, fmt.Errorf("第 %d 条渠道无效：%w", i+1, err)
		}
		if item.WebhookURL == "" {
			return 0, 0, fmt.Errorf("第 %d 条渠道的 Webhook URL 不能为空", i+1)
		}
		if _, dup := seen[item.WebhookURL]; dup {
			return 0, 0, fmt.Errorf("第 %d 条渠道的 Webhook URL 与前面重复", i+1)
		}
		seen[item.WebhookURL] = struct{}{}
		normalized = append(normalized, item)
	}
	if len(normalized) == 0 {
		return 0, 0, nil
	}
	now := time.Now().UTC()
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range normalized {
			var existing domain.NotificationChannel
			findErr := tx.Where("c_webhook_url = ?", item.WebhookURL).Take(&existing).Error
			switch {
			case errors.Is(findErr, gorm.ErrRecordNotFound):
				id, idErr := newNotificationChannelID()
				if idErr != nil {
					return idErr
				}
				channel := domain.NotificationChannel{
					ChannelID:   id,
					ChannelType: item.ChannelType,
					WebhookURL:  item.WebhookURL,
					Secret:      item.Secret,
				}
				if err := tx.Create(&channel).Error; err != nil {
					return err
				}
				created++
			case findErr != nil:
				return findErr
			default:
				if err := tx.Model(&domain.NotificationChannel{}).
					Where("c_id = ?", existing.ID).
					Updates(map[string]any{
						"c_channel_type": item.ChannelType,
						"c_secret":       item.Secret,
						"c_mtime":        now,
					}).Error; err != nil {
					return err
				}
				updated++
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return created, updated, nil
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
	if !notification.ValidChannelType(strings.TrimSpace(value)) {
		return fmt.Errorf("unsupported notification channel type %q", value)
	}
	return nil
}
