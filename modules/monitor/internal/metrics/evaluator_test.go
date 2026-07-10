package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

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

func TestMetricRuleStateTransitionsAndKeepState(t *testing.T) {
	mgr, err := monstorage.Open(filepath.Join(t.TempDir(), "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(500, 0).UTC()
	repo := NewRuleRepository(mgr.DB())
	eval := NewMetricEvaluator(EvaluatorOptions{Repository: repo, InstanceID: "m1", Now: func() time.Time { return now }})
	rule := validRule()
	ctx := context.Background()
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Result: true}); err != nil {
		t.Fatal(err)
	}
	state, _ := repo.GetState(ctx, "space", "rule")
	if state.TriggerCount != 1 || state.Status != domain.AlertStatusOK {
		t.Fatalf("state after one trigger=%+v", state)
	}
	now = now.Add(time.Second)
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Result: true}); err != nil {
		t.Fatal(err)
	}
	state, _ = repo.GetState(ctx, "space", "rule")
	if state.Status != domain.AlertStatusFiring {
		t.Fatalf("state did not fire=%+v", state)
	}
	now = now.Add(time.Second)
	if err := eval.applyState(ctx, rule, &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now, Result: false}); err != nil {
		t.Fatal(err)
	}
	state, _ = repo.GetState(ctx, "space", "rule")
	if state.RecoveryCount != 1 || state.Status != domain.AlertStatusFiring {
		t.Fatalf("state after first recovery=%+v", state)
	}
	keep := &RuleEvaluation{SpaceID: "space", RuleID: "rule", EvaluatedAt: now.Add(time.Second), Result: false, KeepState: true}
	if err := eval.applyState(ctx, rule, keep); err != nil {
		t.Fatal(err)
	}
	state, _ = repo.GetState(ctx, "space", "rule")
	if state.RecoveryCount != 1 || state.TriggerCount != 0 {
		t.Fatalf("keep-state advanced counters=%+v", state)
	}
}
