package metrics

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"gorm.io/gorm"
)

func validRule() *monitorpb.MetricRule {
	return &monitorpb.MetricRule{SpaceId: "space", RuleId: "rule", Name: "high", Conditions: []*monitorpb.MetricCondition{{ConditionId: "A", Query: &monitorpb.MetricQuery{Selector: &monitorpb.MetricSelector{ServiceName: "api", MetricName: "requests"}, TimeReducer: monitorpb.TimeReducer_TIME_REDUCER_CURRENT, SeriesReducer: monitorpb.SeriesReducer_SERIES_REDUCER_MAX}, Compare: monitorpb.CompareOperator_COMPARE_OPERATOR_GT, Threshold: 10, NoDataPolicy: monitorpb.NoDataPolicy_NO_DATA_POLICY_OK}}, Connector: monitorpb.LogicalOperator_LOGICAL_OPERATOR_AND, ConsecutiveTriggerCount: 2, ConsecutiveRecoveryCount: 2, EvaluationIntervalSeconds: 30, WebhookIds: []string{"ops"}, Enabled: true}
}

func TestMetricRuleHardDeleteCleansRuntimeRowsAndAllowsRecreate(t *testing.T) {
	mgr := openMetricRuleTestDB(t)
	ctx := context.Background()
	rules := metricRuleStoreForTest(t, mgr)
	createMetricRuleWebhook(t, mgr, "space", "ops", true)
	for cycle := 0; cycle < 2; cycle++ {
		rule := validRule()
		if err := rules.CreateRule(ctx, rule); err != nil {
			t.Fatalf("cycle %d create: %v", cycle, err)
		}
		now := time.Now().UTC()
		if err := rules.UpsertState(ctx, &MetricRuleStateRow{
			SpaceID: "space", RuleID: "rule", Status: "firing",
			NotificationEvent: "triggered", NotificationKey: "pending-key", NotificationStatus: "pending",
		}); err != nil {
			t.Fatal(err)
		}
		if err := rules.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Status: "firing", ResultJSON: "{}"}); err != nil {
			t.Fatal(err)
		}
		if err := rules.DeleteRule(ctx, "space", "rule"); err != nil {
			t.Fatalf("cycle %d delete: %v", cycle, err)
		}
		for _, table := range []string{"t_monitor_metric_rules", "t_monitor_metric_rule_states", "t_monitor_metric_rule_channels", "t_monitor_metric_rule_evaluations"} {
			var count int64
			_, err := store.WithDatabase(mgr, func(db *gorm.DB) struct{} {
				if queryErr := db.Table(table).Where("c_space_id = ? AND c_rule_id = ?", "space", "rule").Count(&count).Error; queryErr != nil {
					t.Fatalf("count %s: %v", table, queryErr)
				}
				return struct{}{}
			})
			if err != nil || count != 0 {
				t.Fatalf("cycle %d table %s count=%d err=%v", cycle, table, count, err)
			}
		}
	}
}

func TestMetricRuleRequiresEnabledWebhooksInsideTransaction(t *testing.T) {
	mgr := openMetricRuleTestDB(t)
	ctx := context.Background()
	rules := metricRuleStoreForTest(t, mgr)
	if err := rules.CreateRule(ctx, validRule()); err == nil {
		t.Fatal("missing webhook was accepted")
	}
	createMetricRuleWebhook(t, mgr, "space", "ops", false)
	if err := rules.CreateRule(ctx, validRule()); err == nil {
		t.Fatal("disabled webhook was accepted")
	}
}

func TestWebhookDeleteRejectsMetricRuleChannelReference(t *testing.T) {
	mgr := openMetricRuleTestDB(t)
	ctx := context.Background()
	createMetricRuleWebhook(t, mgr, "space", "ops", true)
	rules := metricRuleStoreForTest(t, mgr)
	if err := rules.CreateRule(ctx, validRule()); err != nil {
		t.Fatal(err)
	}
	webhook := &domain.WebhookChannel{SpaceID: "space", WebhookID: "ops", Name: "ops", URL: "http://127.0.0.1/webhook", Enabled: false}
	if err := mgr.Repositories().Alerts.UpdateWebhook(ctx, webhook); err != nil {
		t.Fatal(err)
	}
	updated := validRule()
	updated.Name = "updated"
	if err := rules.UpdateRule(ctx, updated); err == nil {
		t.Fatal("metric rule update accepted a disabled webhook")
	}
	if err := mgr.Repositories().Alerts.DeleteWebhook(ctx, "space", "ops"); !errors.Is(err, store.ErrResourceReferenced) {
		t.Fatalf("delete metric-bound webhook error=%v", err)
	}
}

func TestDeleteMetricRuleEvaluationsUsesBoundedBatches(t *testing.T) {
	mgr := openMetricRuleTestDB(t)
	ctx := context.Background()
	rules := metricRuleStoreForTest(t, mgr)
	cutoff := time.Now().UTC().Add(-14 * 24 * time.Hour)
	for i := 0; i < 3; i++ {
		if err := rules.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: "space", RuleID: "rule", EvaluatedAt: cutoff.Add(-time.Duration(i+1) * time.Hour), Status: "ok", ResultJSON: "{}"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := rules.InsertEvaluation(ctx, &MetricRuleEvaluationRow{SpaceID: "space", RuleID: "rule", EvaluatedAt: cutoff.Add(time.Hour), Status: "ok", ResultJSON: "{}"}); err != nil {
		t.Fatal(err)
	}
	deleted, err := rules.DeleteEvaluationsOlderThan(ctx, cutoff, 2)
	if err != nil || deleted != 2 {
		t.Fatalf("first batch deleted=%d err=%v", deleted, err)
	}
	deleted, err = rules.DeleteEvaluationsOlderThan(ctx, cutoff, 2)
	if err != nil || deleted != 1 {
		t.Fatalf("second batch deleted=%d err=%v", deleted, err)
	}
	_, total, err := rules.ListEvaluations(ctx, "space", "rule", 0, 10)
	if err != nil || total != 1 {
		t.Fatalf("remaining=%d err=%v", total, err)
	}
}

func openMetricRuleTestDB(t *testing.T) *store.Store {
	t.Helper()
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mgr.Close() })
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	return mgr
}

func createMetricRuleWebhook(t *testing.T, mgr *store.Store, spaceID, webhookID string, enabled bool) {
	t.Helper()
	if err := mgr.Repositories().Alerts.CreateWebhook(context.Background(), &domain.WebhookChannel{
		SpaceID: spaceID, WebhookID: webhookID, Name: webhookID, URL: "http://127.0.0.1/webhook", Enabled: enabled,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateMetricRuleRejectsNestedOrAmbiguousRules(t *testing.T) {
	rule := validRule()
	if err := ValidateMetricRule(rule); err != nil {
		t.Fatalf("valid rule rejected: %v", err)
	}
	rule.Conditions[0].ConditionId = "C"
	if err := ValidateMetricRule(rule); err == nil {
		t.Fatal("non-contiguous condition accepted")
	}
	rule = validRule()
	rule.Conditions[0].Threshold = math.Inf(1)
	if err := ValidateMetricRule(rule); err == nil {
		t.Fatal("infinite threshold accepted")
	}
}

func TestValidateMetricRuleRejectsUnknownEnumNumbers(t *testing.T) {
	checks := []struct {
		name string
		set  func(*monitorpb.MetricRule)
	}{
		{"time reducer", func(r *monitorpb.MetricRule) { r.Conditions[0].Query.TimeReducer = monitorpb.TimeReducer(99) }},
		{"series reducer", func(r *monitorpb.MetricRule) { r.Conditions[0].Query.SeriesReducer = monitorpb.SeriesReducer(99) }},
		{"compare", func(r *monitorpb.MetricRule) { r.Conditions[0].Compare = monitorpb.CompareOperator(99) }},
		{"no data", func(r *monitorpb.MetricRule) { r.Conditions[0].NoDataPolicy = monitorpb.NoDataPolicy(99) }},
	}
	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			rule := validRule()
			tc.set(rule)
			if err := ValidateMetricRule(rule); err == nil {
				t.Fatalf("unknown %s enum accepted", tc.name)
			}
		})
	}
}

func TestValidateMetricRuleForPreviewAllowsUnsavedRule(t *testing.T) {
	rule := validRule()
	rule.RuleId = ""
	rule.WebhookIds = nil
	if err := ValidateMetricRuleForPreview(rule); err != nil {
		t.Fatalf("unsaved preview rejected: %v", err)
	}
	if err := ValidateMetricRule(rule); err == nil {
		t.Fatal("persisted validation accepted unsaved rule")
	}
}

func TestReduceTimeSeriesCounterResetAndBoundaries(t *testing.T) {
	base := time.Unix(100, 0)
	values := []TimedValue{{At: base, Value: 90}, {At: base.Add(time.Second), Value: 95}, {At: base.Add(2 * time.Second), Value: 3}, {At: base.Add(3 * time.Second), Value: 8}}
	if got, ok := ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_INCREASE, values); !ok || got != 13 {
		t.Fatalf("increase=%v ok=%v, want 13", got, ok)
	}
	if got, ok := ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_RATE, values); !ok || got != 13.0/3.0 {
		t.Fatalf("rate=%v ok=%v", got, ok)
	}
	if got, ok := ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_CURRENT, values[:1]); !ok || got != 90 {
		t.Fatalf("current=%v ok=%v", got, ok)
	}
	if _, ok := ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_RATE, values[:1]); ok {
		t.Fatal("rate with one point should be no data")
	}
}

func TestMetricRuleStoreCanonicalRoundTripAndPagination(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	ruleStore := metricRuleStoreForTest(t, mgr)
	ctx := context.Background()
	if err := mgr.Repositories().Alerts.CreateWebhook(ctx, &domain.WebhookChannel{
		SpaceID: "space", WebhookID: "ops", Name: "ops", URL: "http://127.0.0.1/webhook", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	rule := validRule()
	if err := ruleStore.CreateRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	got, err := ruleStore.GetRule(ctx, "space", "rule")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetRuleId() != rule.GetRuleId() || got.GetConditions()[0].GetQuery().GetSelector().GetMetricName() != "requests" {
		t.Fatalf("round trip=%+v", got)
	}
	rows, total, err := ruleStore.ListRules(ctx, "space", false, 0, 1)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list rows=%d total=%d err=%v", len(rows), total, err)
	}
}
