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

func TestRuleSchedulerStartStopWithEmptyRules(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	rules := metricRuleStoreForTest(t, mgr)

	fixed := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	sched := NewRuleScheduler(SchedulerOptions{
		Evaluator:      &MetricEvaluator{},
		Rules:          rules,
		InstanceID:     "monitor-a",
		ReloadInterval: 20 * time.Millisecond,
		ActiveInstances: func(context.Context) ([]string, error) {
			return []string{"monitor-a", "monitor-b"}, nil
		},
		Now: func() time.Time { return fixed },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sched.Start(ctx)
	time.Sleep(50 * time.Millisecond)
	sched.Stop()
	sched.Stop() // once

	(*RuleScheduler)(nil).Start(ctx)
	(*RuleScheduler)(nil).Stop()
	NewRuleScheduler(SchedulerOptions{}).Start(ctx) // missing deps → no-op
}
