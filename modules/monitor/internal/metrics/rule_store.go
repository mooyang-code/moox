package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrMetricRuleNotFound = gorm.ErrRecordNotFound
)

// ValidateMetricRule validates the flat rule language. There is deliberately
// no expression parser: a rule is only a bounded list of conditions and one
// connector.
func ValidateMetricRule(rule *monitorpb.MetricRule) error {
	return validateMetricRule(rule, false)
}

// ValidateMetricRuleForPreview keeps structural validation while allowing an
// unsaved rule without a rule ID or webhook bindings.
func ValidateMetricRuleForPreview(rule *monitorpb.MetricRule) error {
	return validateMetricRule(rule, true)
}
func validateMetricRule(rule *monitorpb.MetricRule, preview bool) error {
	if rule == nil {
		return errors.New("rule is required")
	}
	if strings.TrimSpace(rule.GetSpaceId()) == "" {
		return errors.New("space_id is required")
	}
	if !preview && strings.TrimSpace(rule.GetRuleId()) == "" {
		return errors.New("rule_id is required")
	}
	if strings.TrimSpace(rule.GetName()) == "" {
		return errors.New("name is required")
	}
	if len(rule.GetConditions()) < 1 || len(rule.GetConditions()) > 8 {
		return errors.New("conditions must contain 1 to 8 items")
	}
	if rule.GetConnector() != monitorpb.LogicalOperator_LOGICAL_OPERATOR_AND && rule.GetConnector() != monitorpb.LogicalOperator_LOGICAL_OPERATOR_OR {
		return errors.New("connector must be AND or OR")
	}
	if rule.GetConsecutiveTriggerCount() == 0 || rule.GetConsecutiveRecoveryCount() == 0 {
		return errors.New("consecutive trigger and recovery counts must be positive")
	}
	if rule.GetEvaluationIntervalSeconds() == 0 {
		return errors.New("evaluation_interval_seconds must be positive")
	}
	if !preview && len(rule.GetWebhookIds()) == 0 {
		return errors.New("at least one webhook_id is required")
	}
	seenWebhook := map[string]struct{}{}
	for _, id := range rule.GetWebhookIds() {
		id = strings.TrimSpace(id)
		if id == "" {
			return errors.New("webhook_id must not be empty")
		}
		if _, ok := seenWebhook[id]; ok {
			return fmt.Errorf("duplicate webhook_id %q", id)
		}
		seenWebhook[id] = struct{}{}
	}
	seen := map[string]struct{}{}
	for i, condition := range rule.GetConditions() {
		if condition == nil {
			return fmt.Errorf("condition %d is nil", i)
		}
		id := strings.TrimSpace(condition.GetConditionId())
		if id == "" {
			return fmt.Errorf("condition %d id is required", i)
		}
		if len(id) != 1 || id[0] < 'A' || id[0] > 'H' {
			return fmt.Errorf("condition %q must be between A and H", id)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate condition_id %q", id)
		}
		seen[id] = struct{}{}
		if id[0] != byte('A'+i) {
			return errors.New("condition IDs must be contiguous from A")
		}
		query := condition.GetQuery()
		if query == nil || query.GetSelector() == nil {
			return fmt.Errorf("condition %s selector is required", id)
		}
		selector := query.GetSelector()
		if strings.TrimSpace(selector.GetServiceName()) == "" || strings.TrimSpace(selector.GetMetricName()) == "" {
			return fmt.Errorf("condition %s service_name and metric_name are required", id)
		}
		switch query.GetTimeReducer() {
		case monitorpb.TimeReducer_TIME_REDUCER_CURRENT, monitorpb.TimeReducer_TIME_REDUCER_AVG, monitorpb.TimeReducer_TIME_REDUCER_MIN, monitorpb.TimeReducer_TIME_REDUCER_MAX, monitorpb.TimeReducer_TIME_REDUCER_SUM, monitorpb.TimeReducer_TIME_REDUCER_RATE, monitorpb.TimeReducer_TIME_REDUCER_INCREASE:
		default:
			return fmt.Errorf("condition %s has unknown time reducer %d", id, query.GetTimeReducer())
		}
		switch query.GetSeriesReducer() {
		case monitorpb.SeriesReducer_SERIES_REDUCER_AVG, monitorpb.SeriesReducer_SERIES_REDUCER_MIN, monitorpb.SeriesReducer_SERIES_REDUCER_MAX, monitorpb.SeriesReducer_SERIES_REDUCER_SUM:
		default:
			return fmt.Errorf("condition %s has unknown series reducer %d", id, query.GetSeriesReducer())
		}
		if query.GetTimeReducer() != monitorpb.TimeReducer_TIME_REDUCER_CURRENT && query.GetWindowSeconds() == 0 {
			return fmt.Errorf("condition %s window_seconds must be positive", id)
		}
		switch condition.GetCompare() {
		case monitorpb.CompareOperator_COMPARE_OPERATOR_GT, monitorpb.CompareOperator_COMPARE_OPERATOR_GTE, monitorpb.CompareOperator_COMPARE_OPERATOR_LT, monitorpb.CompareOperator_COMPARE_OPERATOR_LTE, monitorpb.CompareOperator_COMPARE_OPERATOR_EQ, monitorpb.CompareOperator_COMPARE_OPERATOR_NEQ:
		default:
			return fmt.Errorf("condition %s has unknown compare operator %d", id, condition.GetCompare())
		}
		if math.IsNaN(condition.GetThreshold()) || math.IsInf(condition.GetThreshold(), 0) {
			return fmt.Errorf("condition %s threshold must be finite", id)
		}
		switch condition.GetNoDataPolicy() {
		case monitorpb.NoDataPolicy_NO_DATA_POLICY_KEEP_STATE, monitorpb.NoDataPolicy_NO_DATA_POLICY_OK, monitorpb.NoDataPolicy_NO_DATA_POLICY_FIRING:
		default:
			return fmt.Errorf("condition %s has unknown no_data_policy %d", id, condition.GetNoDataPolicy())
		}
		matchers := map[string]struct{}{}
		for _, matcher := range selector.GetMatchers() {
			if matcher == nil || strings.TrimSpace(matcher.GetName()) == "" {
				return fmt.Errorf("condition %s matcher name is required", id)
			}
			if _, ok := matchers[matcher.GetName()]; ok {
				return fmt.Errorf("condition %s duplicate matcher %q", id, matcher.GetName())
			}
			matchers[matcher.GetName()] = struct{}{}
		}
	}
	return nil
}

func marshalMetricRule(rule *monitorpb.MetricRule) (string, error) {
	if err := ValidateMetricRule(rule); err != nil {
		return "", err
	}
	// protojson emits protobuf fields in descriptor order and sorts map keys.
	b, err := (protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}).Marshal(rule)
	return string(b), err
}
func unmarshalMetricRule(raw string) (*monitorpb.MetricRule, error) {
	var rule monitorpb.MetricRule
	if err := protojson.Unmarshal([]byte(raw), &rule); err != nil {
		return nil, err
	}
	if err := ValidateMetricRule(&rule); err != nil {
		return nil, err
	}
	return &rule, nil
}

type MetricRuleStore struct{ db *gorm.DB }

func NewMetricRuleStore(db *gorm.DB) *MetricRuleStore { return &MetricRuleStore{db: db} }

func (r *MetricRuleStore) CreateRule(ctx context.Context, rule *monitorpb.MetricRule) error {
	if r == nil || r.db == nil {
		return ErrMetricsStoreUnavailable
	}
	if err := ValidateMetricRule(rule); err != nil {
		return err
	}
	definition, err := marshalMetricRule(rule)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateMetricRuleWebhooks(tx, rule); err != nil {
			return err
		}
		row := &MetricRuleRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), Name: rule.GetName(), DefinitionJSON: definition, EvaluationIntervalSeconds: int(rule.GetEvaluationIntervalSeconds()), Enabled: rule.GetEnabled(), CreatedAt: now, UpdatedAt: now}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		return r.replaceChannels(tx, rule)
	})
}
func (r *MetricRuleStore) UpdateRule(ctx context.Context, rule *monitorpb.MetricRule) error {
	if r == nil || r.db == nil {
		return ErrMetricsStoreUnavailable
	}
	if err := ValidateMetricRule(rule); err != nil {
		return err
	}
	definition, err := marshalMetricRule(rule)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateMetricRuleWebhooks(tx, rule); err != nil {
			return err
		}
		res := tx.Model(&MetricRuleRow{}).Where("c_space_id = ? AND c_rule_id = ?", rule.GetSpaceId(), rule.GetRuleId()).Updates(map[string]any{"c_name": rule.GetName(), "c_definition_json": definition, "c_evaluation_interval_seconds": rule.GetEvaluationIntervalSeconds(), "c_enabled": rule.GetEnabled(), "c_mtime": time.Now().UTC()})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		return r.replaceChannels(tx, rule)
	})
}

func validateMetricRuleWebhooks(tx *gorm.DB, rule *monitorpb.MetricRule) error {
	for _, webhookID := range rule.GetWebhookIds() {
		var count int64
		if err := tx.Table("t_monitor_webhooks").
			Where("c_space_id = ? AND c_webhook_id = ? AND c_enabled = 1", rule.GetSpaceId(), webhookID).
			Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("webhook %q is missing or disabled", webhookID)
		}
	}
	return nil
}
func (r *MetricRuleStore) replaceChannels(tx *gorm.DB, rule *monitorpb.MetricRule) error {
	if err := tx.Where("c_space_id = ? AND c_rule_id = ?", rule.GetSpaceId(), rule.GetRuleId()).Delete(&MetricRuleChannelRow{}).Error; err != nil {
		return err
	}
	for _, webhookID := range rule.GetWebhookIds() {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&MetricRuleChannelRow{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), WebhookID: webhookID}).Error; err != nil {
			return err
		}
	}
	return nil
}
func (r *MetricRuleStore) GetRule(ctx context.Context, spaceID, ruleID string) (*monitorpb.MetricRule, error) {
	var row MetricRuleRow
	if err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).First(&row).Error; err != nil {
		return nil, err
	}
	rule, err := unmarshalMetricRule(row.DefinitionJSON)
	if err != nil {
		return nil, err
	}
	rule.CreatedAt = row.CreatedAt.UTC().Format(time.RFC3339Nano)
	rule.UpdatedAt = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
	return rule, nil
}
func (r *MetricRuleStore) ListRules(ctx context.Context, spaceID string, enabledOnly bool, offset, limit int) ([]*monitorpb.MetricRule, int64, error) {
	offset, limit = boundedPage(offset, limit)
	q := r.db.WithContext(ctx).Model(&MetricRuleRow{})
	if spaceID != "" {
		q = q.Where("c_space_id = ?", spaceID)
	}
	if enabledOnly {
		q = q.Where("c_enabled = 1")
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []MetricRuleRow
	if err := q.Order("c_space_id ASC,c_rule_id ASC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	out := make([]*monitorpb.MetricRule, 0, len(rows))
	for _, row := range rows {
		rule, err := unmarshalMetricRule(row.DefinitionJSON)
		if err != nil {
			return nil, 0, err
		}
		rule.CreatedAt = row.CreatedAt.UTC().Format(time.RFC3339Nano)
		rule.UpdatedAt = row.UpdatedAt.UTC().Format(time.RFC3339Nano)
		out = append(out, rule)
	}
	return out, total, nil
}
func (r *MetricRuleStore) DeleteRule(ctx context.Context, spaceID, ruleID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rule MetricRuleRow
		if err := tx.Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).First(&rule).Error; err != nil {
			return err
		}
		for _, model := range []any{&MetricRuleStateRow{}, &MetricRuleChannelRow{}, &MetricRuleEvaluationRow{}} {
			if err := tx.Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).Delete(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&rule).Error
	})
}
func (r *MetricRuleStore) ListEnabled(ctx context.Context, spaceID string) ([]*monitorpb.MetricRule, error) {
	rows, _, err := r.ListRules(ctx, spaceID, true, 0, 500)
	return rows, err
}

func (r *MetricRuleStore) UpsertState(ctx context.Context, state *MetricRuleStateRow) error {
	return r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "c_space_id"}, {Name: "c_rule_id"}}, DoUpdates: clause.AssignmentColumns([]string{"c_status", "c_trigger_count", "c_recovery_count", "c_last_evaluated_at", "c_last_triggered_at", "c_last_recovered_at", "c_notification_event", "c_notification_key", "c_notification_status", "c_notification_error", "c_notification_attempts", "c_last_notification_at", "c_mtime"})}).Create(state).Error
}
func (r *MetricRuleStore) GetState(ctx context.Context, spaceID, ruleID string) (*MetricRuleStateRow, error) {
	var row MetricRuleStateRow
	err := r.db.WithContext(ctx).Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID).First(&row).Error
	return &row, err
}
func (r *MetricRuleStore) InsertEvaluation(ctx context.Context, row *MetricRuleEvaluationRow) error {
	return r.db.WithContext(ctx).Create(row).Error
}
func (r *MetricRuleStore) ListEvaluations(ctx context.Context, spaceID, ruleID string, offset, limit int) ([]MetricRuleEvaluationRow, int64, error) {
	offset, limit = boundedPage(offset, limit)
	q := r.db.WithContext(ctx).Model(&MetricRuleEvaluationRow{}).Where("c_space_id = ? AND c_rule_id = ?", spaceID, ruleID)
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []MetricRuleEvaluationRow
	if err := q.Order("c_evaluated_at DESC,c_id DESC").Offset(offset).Limit(limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

func (r *MetricRuleStore) DeleteEvaluationsOlderThan(ctx context.Context, cutoff time.Time, batchSize int) (int64, error) {
	if r == nil || r.db == nil {
		return 0, ErrMetricsStoreUnavailable
	}
	if batchSize <= 0 || batchSize > 500 {
		batchSize = 500
	}
	var ids []uint64
	if err := r.db.WithContext(ctx).Model(&MetricRuleEvaluationRow{}).
		Where("c_evaluated_at < ?", cutoff).
		Order("c_evaluated_at ASC, c_id ASC").
		Limit(batchSize).
		Pluck("c_id", &ids).Error; err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}
	deleted := r.db.WithContext(ctx).Where("c_id IN ?", ids).Delete(&MetricRuleEvaluationRow{})
	return deleted.RowsAffected, deleted.Error
}
