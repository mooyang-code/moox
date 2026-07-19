package metrics

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/assert"
	"path/filepath"
	"testing"
	"time"
)

type retryMetricNotifier struct {
	attempts int
}

func (n *retryMetricNotifier) SendMetric(context.Context, domain.WebhookChannel, MetricEvent) error {
	n.attempts++
	if n.attempts == 1 {
		return errors.New("temporary webhook failure")
	}
	return nil
}

func TestCompareTruthTable(t *testing.T) {
	for _, tc := range []struct {
		op   monitorpb.CompareOperator
		want bool
	}{
		{monitorpb.CompareOperator_COMPARE_OPERATOR_GT, false}, {monitorpb.CompareOperator_COMPARE_OPERATOR_GTE, true},
		{monitorpb.CompareOperator_COMPARE_OPERATOR_LT, false}, {monitorpb.CompareOperator_COMPARE_OPERATOR_LTE, true},
		{monitorpb.CompareOperator_COMPARE_OPERATOR_EQ, true}, {monitorpb.CompareOperator_COMPARE_OPERATOR_NEQ, false},
	} {
		if got := Compare(tc.op, 10, 10); got != tc.want {
			t.Errorf("%v got %v want %v", tc.op, got, tc.want)
		}
	}
}

func TestNoDataPolicyOKDoesNotFire(t *testing.T) {
	rule := validRule()
	if got := noDataResult("ok", rule.GetConditions(), "A"); got {
		t.Fatal("no-data OK policy fired")
	}
}

func TestORKeepStateDoesNotSuppressFiringCondition(t *testing.T) {
	conditions := []*monitorpb.MetricCondition{{ConditionId: "A"}, {ConditionId: "B"}}
	result, keep := combineConditionResults(monitorpb.LogicalOperator_LOGICAL_OPERATOR_OR, []ConditionResult{
		{ConditionID: "A", NoDataReason: "keep_state"},
		{ConditionID: "B", HasData: true, Result: true},
	}, conditions)
	if !result || keep {
		t.Fatalf("OR result=%v keep_state=%v, want firing without freeze", result, keep)
	}
}

func TestORFiringNoDataPolicyWinsOverKeepState(t *testing.T) {
	conditions := []*monitorpb.MetricCondition{{ConditionId: "A"}, {ConditionId: "B"}}
	result, keep := combineConditionResults(monitorpb.LogicalOperator_LOGICAL_OPERATOR_OR, []ConditionResult{
		{ConditionID: "A", NoDataReason: "keep_state"},
		{ConditionID: "B", NoDataReason: "firing"},
	}, conditions)
	if !result || keep {
		t.Fatalf("OR result=%v keep_state=%v, want firing without freeze", result, keep)
	}
}

func TestMetricRuleStateTransitionsAndKeepState(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500, 0).UTC()
	ruleStore := metricRuleStoreForTest(t, mgr)
	eval := NewMetricEvaluator(EvaluatorOptions{RuleStore: ruleStore, Now: func() time.Time { return now }})
	rule := validRule()
	ctx := context.Background()
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Result: true}); err != nil {
		t.Fatal(err)
	}
	state, _ := ruleStore.GetState(ctx, "space", "rule")
	if state.TriggerCount != 1 || state.Status != domain.AlertStatusOK {
		t.Fatalf("state after one trigger=%+v", state)
	}
	now = now.Add(time.Second)
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Result: true}); err != nil {
		t.Fatal(err)
	}
	state, _ = ruleStore.GetState(ctx, "space", "rule")
	if state.Status != domain.AlertStatusFiring {
		t.Fatalf("state did not fire=%+v", state)
	}
	now = now.Add(time.Second)
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Result: false}); err != nil {
		t.Fatal(err)
	}
	state, _ = ruleStore.GetState(ctx, "space", "rule")
	if state.RecoveryCount != 1 || state.Status != domain.AlertStatusFiring {
		t.Fatalf("state after first recovery=%+v", state)
	}
	keep := &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now.Add(time.Second), Result: false, KeepState: true}
	if err := eval.applyState(ctx, rule, keep); err != nil {
		t.Fatal(err)
	}
	state, _ = ruleStore.GetState(ctx, "space", "rule")
	if state.RecoveryCount != 1 || state.TriggerCount != 0 {
		t.Fatalf("keep-state advanced counters=%+v", state)
	}
}

func TestMetricNotificationFailureIsPersistedAndRetried(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	notifier := &retryMetricNotifier{}
	now := time.Unix(900, 0).UTC()
	rule := validRule()
	rule.ConsecutiveTriggerCount = 1
	eval := NewMetricEvaluator(EvaluatorOptions{RuleStore: metricRuleStoreForTest(t, mgr), Now: func() time.Time { return now }, Notifier: notifier, Webhook: func(context.Context, string, string) (*domain.WebhookChannel, error) {
		return &domain.WebhookChannel{WebhookID: "ops", Enabled: true}, nil
	}})
	ctx := context.Background()
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: now, Result: true}); err == nil {
		t.Fatal("first notification failure was swallowed")
	}
	state, err := eval.rules.GetState(ctx, rule.GetSpaceId(), rule.GetRuleId())
	if err != nil || state.NotificationStatus != "failed" || state.NotificationKey == "" {
		t.Fatalf("failed notification state=%+v err=%v", state, err)
	}
	now = now.Add(time.Second)
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: rule.GetSpaceId(), RuleID: rule.GetRuleId(), EvaluatedAt: now, Result: true}); err != nil {
		t.Fatal(err)
	}
	state, _ = eval.rules.GetState(ctx, rule.GetSpaceId(), rule.GetRuleId())
	if notifier.attempts != 2 || state.NotificationStatus != "sent" {
		t.Fatalf("retry attempts=%d state=%+v", notifier.attempts, state)
	}
}

func TestReduceTimeSeriesCoversReducersAndCounterReset(t *testing.T) {
	base := time.Unix(100, 0).UTC()
	values := []TimedValue{
		{At: base.Add(20 * time.Second), Value: 9},
		{At: base, Value: 10},
		{At: base.Add(10 * time.Second), Value: 4},
	}

	got, ok := ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_CURRENT, values)
	assert.True(t, ok)
	assert.Equal(t, 9.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_AVG, values)
	assert.True(t, ok)
	assert.Equal(t, 23.0/3.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_MIN, values)
	assert.True(t, ok)
	assert.Equal(t, 4.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_MAX, values)
	assert.True(t, ok)
	assert.Equal(t, 10.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_SUM, values)
	assert.True(t, ok)
	assert.Equal(t, 23.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_INCREASE, values)
	assert.True(t, ok)
	assert.Equal(t, 9.0, got)

	got, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_RATE, values)
	assert.True(t, ok)
	assert.Equal(t, 9.0/20.0, got)

	_, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_RATE, values[:1])
	assert.False(t, ok)
	_, ok = ReduceTimeSeries(monitorpb.TimeReducer(99), values)
	assert.False(t, ok)
	_, ok = ReduceTimeSeries(monitorpb.TimeReducer_TIME_REDUCER_CURRENT, nil)
	assert.False(t, ok)
}

func TestReduceSeriesLabelsAndNoDataHelpers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reducer monitorpb.SeriesReducer
		want    float64
		ok      bool
	}{
		{"avg", monitorpb.SeriesReducer_SERIES_REDUCER_AVG, 4, true},
		{"min", monitorpb.SeriesReducer_SERIES_REDUCER_MIN, 2, true},
		{"max", monitorpb.SeriesReducer_SERIES_REDUCER_MAX, 6, true},
		{"sum", monitorpb.SeriesReducer_SERIES_REDUCER_SUM, 12, true},
		{"unknown", monitorpb.SeriesReducer(99), 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ReduceSeries(tc.reducer, []float64{2, 4, 6})
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
	_, ok := ReduceSeries(monitorpb.SeriesReducer_SERIES_REDUCER_AVG, nil)
	assert.False(t, ok)

	assert.True(t, labelsMatch(`{"env":"prod","zone":"gz"}`, []*monitorpb.LabelMatcher{{Name: "env", Value: "prod"}}))
	assert.True(t, labelsMatch(`{"env":"prod"}`, []*monitorpb.LabelMatcher{{Name: "zone", Value: "gz", Negate: true}}))
	assert.False(t, labelsMatch(`{"env":"prod"}`, []*monitorpb.LabelMatcher{{Name: "env", Value: "prod", Negate: true}}))
	assert.False(t, labelsMatch(`not-json`, []*monitorpb.LabelMatcher{{Name: "env", Value: "prod"}}))
	assert.False(t, labelsMatch(`{"env":"prod"}`, []*monitorpb.LabelMatcher{{Name: "env", Value: "dev"}}))

	assert.Equal(t, "keep_state", noDataReason(monitorpb.NoDataPolicy_NO_DATA_POLICY_KEEP_STATE))
	assert.Equal(t, "ok", noDataReason(monitorpb.NoDataPolicy_NO_DATA_POLICY_OK))
	assert.Equal(t, "firing", noDataReason(monitorpb.NoDataPolicy_NO_DATA_POLICY_FIRING))
	assert.Equal(t, "unspecified", noDataReason(monitorpb.NoDataPolicy(99)))
	assert.False(t, Compare(monitorpb.CompareOperator(99), 1, 1))
}
