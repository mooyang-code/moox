package store

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type AlertRepository struct {
	db *gorm.DB
}

func NewAlertRepository(db *gorm.DB) *AlertRepository {
	return &AlertRepository{db: db}
}

func (r *AlertRepository) CountFiring(ctx context.Context, spaceID string) (int64, error) {
	query := r.db.WithContext(ctx).Model(&domain.AlertState{}).Where("c_status = ?", domain.AlertStatusFiring)
	if spaceID != "" {
		query = query.Where("c_space_id = ?", spaceID)
	}
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *AlertRepository) CreateWebhook(ctx context.Context, webhook *domain.WebhookChannel) error {
	return r.db.WithContext(ctx).Create(webhook).Error
}

func (r *AlertRepository) UpdateWebhook(ctx context.Context, webhook *domain.WebhookChannel) error {
	return r.db.WithContext(ctx).
		Model(&domain.WebhookChannel{}).
		Where("c_space_id = ? AND c_webhook_id = ? AND c_is_deleted = 0", webhook.SpaceID, webhook.WebhookID).
		Updates(map[string]any{
			"c_name":          webhook.Name,
			"c_url":           webhook.URL,
			"c_method":        webhook.Method,
			"c_headers":       webhook.Headers,
			"c_body_template": webhook.BodyTemplate,
			"c_enabled":       webhook.Enabled,
		}).Error
}

func (r *AlertRepository) ListWebhooks(ctx context.Context, spaceID string) ([]domain.WebhookChannel, error) {
	var webhooks []domain.WebhookChannel
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_is_deleted = 0", spaceID).
		Order("c_name ASC").
		Find(&webhooks).Error
	return webhooks, err
}

func (r *AlertRepository) GetWebhook(ctx context.Context, spaceID, webhookID string) (*domain.WebhookChannel, error) {
	var webhook domain.WebhookChannel
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_webhook_id = ? AND c_is_deleted = 0", spaceID, webhookID).
		First(&webhook).Error
	if err != nil {
		return nil, err
	}
	return &webhook, nil
}

func (r *AlertRepository) DeleteWebhook(ctx context.Context, spaceID, webhookID string) error {
	return r.db.WithContext(ctx).
		Model(&domain.WebhookChannel{}).
		Where("c_space_id = ? AND c_webhook_id = ? AND c_is_deleted = 0", spaceID, webhookID).
		Updates(map[string]any{"c_is_deleted": true}).Error
}

func (r *AlertRepository) CreateRule(ctx context.Context, rule *domain.AlertRule) error {
	return r.db.WithContext(ctx).Create(rule).Error
}

func (r *AlertRepository) UpdateRule(ctx context.Context, rule *domain.AlertRule) error {
	return r.db.WithContext(ctx).
		Model(&domain.AlertRule{}).
		Where("c_space_id = ? AND c_rule_id = ? AND c_is_deleted = 0", rule.SpaceID, rule.RuleID).
		Updates(map[string]any{
			"c_check_id":                          rule.CheckID,
			"c_webhook_id":                        rule.WebhookID,
			"c_failure_threshold":                 rule.FailureThreshold,
			"c_success_threshold":                 rule.SuccessThreshold,
			"c_minimum_reminder_interval_seconds": rule.MinimumReminderIntervalSeconds,
			"c_send_on_resolved":                  rule.SendOnResolved,
			"c_enabled":                           rule.Enabled,
			"c_description":                       rule.Description,
		}).Error
}

func (r *AlertRepository) ListRules(ctx context.Context, spaceID string) ([]domain.AlertRule, error) {
	var rules []domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_is_deleted = 0", spaceID).
		Order("c_ctime ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AlertRepository) ListRulesForCheck(ctx context.Context, spaceID, checkID string) ([]domain.AlertRule, error) {
	var rules []domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ? AND c_is_deleted = 0", spaceID, checkID).
		Order("c_ctime ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AlertRepository) ListEnabledRulesForCheck(ctx context.Context, spaceID, checkID string) ([]domain.AlertRule, error) {
	var rules []domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ? AND c_enabled = 1 AND c_is_deleted = 0", spaceID, checkID).
		Find(&rules).Error
	return rules, err
}

func (r *AlertRepository) DeleteRule(ctx context.Context, spaceID, ruleID string) error {
	return r.db.WithContext(ctx).
		Model(&domain.AlertRule{}).
		Where("c_space_id = ? AND c_rule_id = ? AND c_is_deleted = 0", spaceID, ruleID).
		Updates(map[string]any{"c_is_deleted": true}).Error
}

func (r *AlertRepository) GetRuleByID(ctx context.Context, ruleID string) (*domain.AlertRule, error) {
	var rule domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_rule_id = ? AND c_is_deleted = 0", ruleID).
		First(&rule).Error
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

func (r *AlertRepository) UpsertState(ctx context.Context, state *domain.AlertState) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "c_space_id"},
			{Name: "c_rule_id"},
			{Name: "c_check_id"},
		},
		DoUpdates: clause.AssignmentColumns([]string{
			"c_status",
			"c_failure_count",
			"c_success_count",
			"c_triggered_at",
			"c_resolved_at",
			"c_last_reminder_at",
			"c_dedupe_key",
			"c_mtime",
		}),
	}).Create(state).Error
}

func (r *AlertRepository) GetState(ctx context.Context, spaceID, ruleID, checkID string) (*domain.AlertState, error) {
	var state domain.AlertState
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_rule_id = ? AND c_check_id = ?", spaceID, ruleID, checkID).
		First(&state).Error
	if err != nil {
		return nil, err
	}
	return &state, nil
}

func (r *AlertRepository) CreateEvent(ctx context.Context, event *domain.AlertEvent) error {
	return r.db.WithContext(ctx).Create(event).Error
}

func (r *AlertRepository) CreateEventIdempotent(ctx context.Context, event *domain.AlertEvent) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_event_id"}}, DoNothing: true}).Create(event).Error
}

func (r *AlertRepository) ListEvents(ctx context.Context, spaceID string, limit int) ([]domain.AlertEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	var events []domain.AlertEvent
	err := r.db.WithContext(ctx).
		Where("c_space_id = ?", spaceID).
		Order("c_created_at DESC, c_id DESC").
		Limit(limit).
		Find(&events).Error
	return events, err
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

func (r *AlertRepository) DeleteEventsOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	tx := r.db.WithContext(ctx).
		Where("c_created_at < ?", cutoff).
		Delete(&domain.AlertEvent{})
	return tx.RowsAffected, tx.Error
}
