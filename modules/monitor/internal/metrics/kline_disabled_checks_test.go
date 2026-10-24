package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 规则存在但关闭了新鲜度监控的检查会在每轮评估里被补建，评估器把它们的检查 ID 记下来供清理时保留；
// 还没成功评估过时第二个返回值是 false。
func TestKlineFreshnessEvaluatorRecordsRuleDisabledChecks(t *testing.T) {
	now := time.Date(2026, 9, 8, 10, 2, 30, 0, time.UTC)
	disabled := KlineFreshnessRule{Enabled: false, SpaceID: "stockcn", DatasetID: "dataset", ViewID: "view_off", Frequency: "1m", MarketID: "stockcn"}
	enabled := KlineFreshnessRule{Enabled: true, SpaceID: "crypto", DatasetID: "dataset", ViewID: "view_on", Frequency: "1m", MarketID: "crypto", Lateness: 2 * time.Minute}
	query := newViewSummaryQuery(t, []viewSummaryTestSample{{Latest: now.Add(-time.Minute), Subjects: 1, ObservedAt: now}})
	evaluator := NewKlineFreshnessEvaluator(query, []KlineFreshnessRule{disabled, enabled}, 20)

	_, known := evaluator.RuleDisabledCheckIDs()
	require.False(t, known, "评估之前不知道")

	_, err := evaluator.Evaluate(context.Background(), now)
	require.NoError(t, err)
	ids, known := evaluator.RuleDisabledCheckIDs()
	require.True(t, known)
	require.Equal(t, map[string]struct{}{KlineFreshnessCheckID(disabled): {}}, ids)

	var nilEvaluator *KlineFreshnessEvaluator
	_, known = nilEvaluator.RuleDisabledCheckIDs()
	require.False(t, known)
}
