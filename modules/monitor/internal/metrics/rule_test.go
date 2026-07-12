package metrics

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func validRule() *monitorpb.MetricRule {
	return &monitorpb.MetricRule{SpaceId: "space", RuleId: "rule", Name: "high", Conditions: []*monitorpb.MetricCondition{{ConditionId: "A", Query: &monitorpb.MetricQuery{Selector: &monitorpb.MetricSelector{ServiceName: "api", MetricName: "requests"}, TimeReducer: monitorpb.TimeReducer_TIME_REDUCER_CURRENT, SeriesReducer: monitorpb.SeriesReducer_SERIES_REDUCER_MAX}, Compare: monitorpb.CompareOperator_COMPARE_OPERATOR_GT, Threshold: 10, NoDataPolicy: monitorpb.NoDataPolicy_NO_DATA_POLICY_OK}}, Connector: monitorpb.LogicalOperator_LOGICAL_OPERATOR_AND, ConsecutiveTriggerCount: 2, ConsecutiveRecoveryCount: 2, EvaluationIntervalSeconds: 30, WebhookIds: []string{"ops"}, Enabled: true}
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

func TestRuleRepositoryCanonicalRoundTripAndPagination(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	repo := NewRuleRepository(mgr.DB())
	ctx := context.Background()
	rule := validRule()
	if err := repo.CreateRule(ctx, rule, nil); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRule(ctx, "space", "rule")
	if err != nil {
		t.Fatal(err)
	}
	if got.GetRuleId() != rule.GetRuleId() || got.GetConditions()[0].GetQuery().GetSelector().GetMetricName() != "requests" {
		t.Fatalf("round trip=%+v", got)
	}
	rows, total, err := repo.ListRules(ctx, "space", false, 0, 1)
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("list rows=%d total=%d err=%v", len(rows), total, err)
	}
}
