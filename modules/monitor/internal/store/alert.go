package store

import (
	"context"
	"fmt"
	"strings"
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
		Where("c_space_id = ? AND c_webhook_id = ?", webhook.SpaceID, webhook.WebhookID).
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
		Where("c_space_id = ?", spaceID).
		Order("c_name ASC").
		Find(&webhooks).Error
	return webhooks, err
}

func (r *AlertRepository) GetWebhook(ctx context.Context, spaceID, webhookID string) (*domain.WebhookChannel, error) {
	var webhook domain.WebhookChannel
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_webhook_id = ?", spaceID, webhookID).
		First(&webhook).Error
	if err != nil {
		return nil, err
	}
	return &webhook, nil
}

func (r *AlertRepository) DeleteWebhook(ctx context.Context, spaceID, webhookID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var alertReferences int64
		if err := tx.Model(&domain.AlertRule{}).
			Where("c_space_id = ? AND c_webhook_id = ?", spaceID, webhookID).
			Count(&alertReferences).Error; err != nil {
			return err
		}
		var metricReferences int64
		if err := tx.Table("t_monitor_metric_rule_channels").
			Where("c_space_id = ? AND c_webhook_id = ?", spaceID, webhookID).
			Count(&metricReferences).Error; err != nil {
			return err
		}
		if total := alertReferences + metricReferences; total > 0 {
			return fmt.Errorf("%w: webhook %q is bound to %d rule channel(s)", ErrResourceReferenced, webhookID, total)
		}
		deleted := tx.Where("c_space_id = ? AND c_webhook_id = ?", spaceID, webhookID).
			Delete(&domain.WebhookChannel{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *AlertRepository) CreateRule(ctx context.Context, rule *domain.AlertRule) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateAlertRuleReferences(tx, rule); err != nil {
			return err
		}
		return tx.Create(rule).Error
	})
}

func (r *AlertRepository) UpdateRule(ctx context.Context, rule *domain.AlertRule) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateAlertRuleReferences(tx, rule); err != nil {
			return err
		}
		updated := tx.Model(&domain.AlertRule{}).
			Where("c_space_id = ? AND c_rule_id = ?", rule.SpaceID, rule.RuleID).
			Updates(map[string]any{
				"c_check_id":                          rule.CheckID,
				"c_webhook_id":                        rule.WebhookID,
				"c_failure_threshold":                 rule.FailureThreshold,
				"c_success_threshold":                 rule.SuccessThreshold,
				"c_minimum_reminder_interval_seconds": rule.MinimumReminderIntervalSeconds,
				"c_send_on_resolved":                  rule.SendOnResolved,
				"c_enabled":                           rule.Enabled,
				"c_description":                       rule.Description,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
}

func (r *AlertRepository) ListRules(ctx context.Context, spaceID string) ([]domain.AlertRule, error) {
	var rules []domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ?", spaceID).
		Order("c_ctime ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AlertRepository) ListRulesForCheck(ctx context.Context, spaceID, checkID string) ([]domain.AlertRule, error) {
	var rules []domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ?", spaceID, checkID).
		Order("c_ctime ASC").
		Find(&rules).Error
	return rules, err
}

func (r *AlertRepository) GetRule(ctx context.Context, spaceID, ruleID string) (*domain.AlertRule, error) {
	var rule domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).
		First(&rule).Error
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

func (r *AlertRepository) ListEnabledRulesForCheck(ctx context.Context, spaceID, checkID string) ([]domain.AlertRule, error) {
	var rules []domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_space_id = ? AND c_check_id = ? AND c_enabled = 1", spaceID, checkID).
		Find(&rules).Error
	return rules, err
}

func (r *AlertRepository) DeleteRule(ctx context.Context, spaceID, ruleID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rule domain.AlertRule
		if err := tx.Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).First(&rule).Error; err != nil {
			return err
		}
		if err := tx.Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).
			Delete(&domain.AlertState{}).Error; err != nil {
			return err
		}
		return tx.Delete(&rule).Error
	})
}

func (r *AlertRepository) GetRuleByID(ctx context.Context, ruleID string) (*domain.AlertRule, error) {
	var rule domain.AlertRule
	err := r.db.WithContext(ctx).
		Where("c_rule_id = ?", ruleID).
		First(&rule).Error
	if err != nil {
		return nil, err
	}
	return &rule, nil
}

func validateAlertRuleReferences(tx *gorm.DB, rule *domain.AlertRule) error {
	if rule == nil {
		return fmt.Errorf("alert rule is required")
	}
	if !isHostMetricRuleKey(rule.CheckID) {
		var enabledChecks int64
		if err := tx.Model(&domain.Check{}).
			Where("c_space_id = ? AND c_check_id = ? AND c_enabled = 1", rule.SpaceID, rule.CheckID).
			Count(&enabledChecks).Error; err != nil {
			return err
		}
		if enabledChecks != 1 {
			return fmt.Errorf("%w: check %q", ErrInvalidReference, rule.CheckID)
		}
	}
	var enabledWebhooks int64
	if err := tx.Model(&domain.WebhookChannel{}).
		Where("c_space_id = ? AND c_webhook_id = ? AND c_enabled = 1", rule.SpaceID, rule.WebhookID).
		Count(&enabledWebhooks).Error; err != nil {
		return err
	}
	if enabledWebhooks != 1 {
		return fmt.Errorf("%w: webhook %q", ErrInvalidReference, rule.WebhookID)
	}
	return nil
}

func isHostMetricRuleKey(checkID string) bool {
	parts := strings.Split(checkID, ":")
	if len(parts) != 3 || parts[0] != "host" || strings.TrimSpace(parts[1]) == "" {
		return false
	}
	switch parts[2] {
	case "cpu", "memory", "filesystem_usage", "disk_utilization", "network_errors":
		return true
	default:
		return false
	}
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
