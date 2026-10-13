package replay

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/stretchr/testify/require"
)

func TestPresenceKeepsAgedSubjectAfterWindowGap(t *testing.T) {
	p := newPresence()
	p.indexes["A"] = map[int64]struct{}{10: {}}
	resolved := input.Resolved{MinAgeBars: 1}

	// bar 10：窗口内有行，A 通过年龄判定。
	probe := p.probe(resolved, input.PeriodBoundaries{BarIndex: 10})
	got, err := probe(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")

	// bar 20：窗口内整个数据集都没有行，但 A 已通过判定，不能被重新剔除，也不能整期 history_insufficient。
	probe = p.probe(resolved, input.PeriodBoundaries{BarIndex: 20})
	got, err = probe(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")

	// 从未通过的标的遇到整窗为空，仍然是历史缺口。
	_, err = p.probe(resolved, input.PeriodBoundaries{BarIndex: 20})(context.Background(), []string{"B"})
	var skip *input.SkipError
	require.ErrorAs(t, err, &skip)
	require.Equal(t, input.SkipHistoryInsufficient, skip.Reason)
}

func TestAgePresenceSharesPassedSetAcrossSegments(t *testing.T) {
	passed := map[string]struct{}{"A": {}}
	first, err := agePresence(context.Background(), nil, input.Resolved{}, nil, passed)
	require.NoError(t, err)
	second, err := agePresence(context.Background(), nil, input.Resolved{}, nil, passed)
	require.NoError(t, err)
	first.passed["B"] = struct{}{}
	require.Contains(t, second.passed, "A")
	require.Contains(t, second.passed, "B", "各段共用同一份已通过集合")
}

func TestPresenceOnlyRemembersCandidatesThatPassed(t *testing.T) {
	p := newPresence()
	p.indexes["A"] = map[int64]struct{}{10: {}}
	p.indexes["B"] = map[int64]struct{}{10: {}}
	resolved := input.Resolved{MinAgeBars: 1}

	// bar 10：只有 A 是候选，B 虽然窗口内有行，但本期没参与，不能被记为已通过。
	got, err := p.probe(resolved, input.PeriodBoundaries{BarIndex: 10})(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")
	require.NotContains(t, p.passed, "B")

	// bar 20：窗口内没有任何行，B 重新成为候选：A 已通过所以不会整期跳过，B 仍要重新判定，不满足。
	got, err = p.probe(resolved, input.PeriodBoundaries{BarIndex: 20})(context.Background(), []string{"A", "B"})
	require.NoError(t, err)
	require.Contains(t, got, "A")
	require.NotContains(t, got, "B")
}
