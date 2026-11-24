package store

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
)

// AlertRepository 读写告警事件。告警状态在检查表里（CheckRepository）。
type AlertRepository struct {
	db *gorm.DB
}

func NewAlertRepository(db *gorm.DB) *AlertRepository {
	return &AlertRepository{db: db}
}

func (r *AlertRepository) CreateEvent(ctx context.Context, event *domain.AlertEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}

// ListRecentEvents returns recent alert transitions across all spaces.
func (r *AlertRepository) ListRecentEvents(ctx context.Context, limit int) ([]domain.AlertEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var events []domain.AlertEvent
	err := r.db.WithContext(ctx).
		Order("c_created_at DESC, c_id DESC").
		Limit(limit).
		Find(&events).Error
	return events, err
}

// ListEventsSince 返回 since 之后（含）的告警事件，按时间升序；超过 limit 条时只保留最新的 limit 条。
// spaces 为空表示不限空间。
func (r *AlertRepository) ListEventsSince(ctx context.Context, spaces []string, since time.Time, limit int) ([]domain.AlertEvent, error) {
	query := r.db.WithContext(ctx).Where("c_created_at >= ?", since.UTC())
	if len(spaces) > 0 {
		query = query.Where("c_space_id IN ?", spaces)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	var events []domain.AlertEvent
	if err := query.Order("c_created_at DESC, c_id DESC").Find(&events).Error; err != nil {
		return nil, err
	}
	slices.Reverse(events)
	return events, nil
}

// LatestTriggeredEventBefore 返回某个检查在 before 之前最近一次触发事件；没有时返回 nil。
func (r *AlertRepository) LatestTriggeredEventBefore(ctx context.Context, spaceID, checkID string, before time.Time) (*domain.AlertEvent, error) {
	var event domain.AlertEvent
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ? AND c_event_type = ? AND c_created_at < ?", spaceID, checkID, domain.AlertEventTriggered, before.UTC()).
		Order("c_created_at DESC, c_id DESC").
		First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &event, err
}

func (r *AlertRepository) DeleteEventsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	return DeleteBefore(ctx, r.db, "t_monitor_alert_events", "c_created_at", cutoff)
}

// LatestFiringEvent 返回某个检查最近一次触发或提醒事件；没有时返回 nil。
func (r *AlertRepository) LatestFiringEvent(ctx context.Context, spaceID, checkID string) (*domain.AlertEvent, error) {
	var event domain.AlertEvent
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_check_id = ? AND c_event_type IN ?", spaceID, checkID, []string{domain.AlertEventTriggered, domain.AlertEventReminder}).Order("c_created_at DESC, c_id DESC").First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &event, err
}
