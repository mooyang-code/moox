package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/stretchr/testify/require"
)

func TestRuleSchedulerEvaluateDueOnceWithEmptyRules(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	rules := metricRuleStoreForTest(t, mgr)

	fixed := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	sched := NewRuleScheduler(SchedulerOptions{
		Evaluator: &MetricEvaluator{},
		Rules:     rules,
		Now: func() time.Time { return fixed },
	})
	require.NoError(t, sched.EvaluateDueOnce(context.Background()))
	require.NoError(t, (*RuleScheduler)(nil).EvaluateDueOnce(context.Background()))
	require.NoError(t, NewRuleScheduler(SchedulerOptions{}).EvaluateDueOnce(context.Background()))
}
