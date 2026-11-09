package replay

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/stretchr/testify/require"
)

// 年龄通过记录按 N 分开：通过 20 根判定的标的之后永远不再算“年龄小于 20”（max_age_bars 不会因窗口缺口把老标的误判成新标的），
// 但它在 5 根判定上要单独判定，不能借 20 根的记录。
func TestPresencePassedIsPerLevel(t *testing.T) {
	p := newPresence()
	p.indexes["A"] = map[int64]struct{}{81: {}, 96: {}}
	// bar 100：20 根的目标是 81，A 通过；5 根的目标是 96，A 也通过。
	got, err := p.probe(20, input.PeriodBoundaries{BarIndex: 100})(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")
	require.Contains(t, p.passed[20], "A")
	require.NotContains(t, p.passed[5], "A")
	// bar 130：窗口里都没有行。20 根已通过，仍算通过；5 根没有记录，重新判定，整窗为空是历史缺口。
	got, err = p.probe(20, input.PeriodBoundaries{BarIndex: 130})(context.Background(), []string{"A"})
	require.NoError(t, err)
	require.Contains(t, got, "A")
	_, err = p.probe(5, input.PeriodBoundaries{BarIndex: 130})(context.Background(), []string{"A"})
	var skip *input.SkipError
	require.ErrorAs(t, err, &skip)
}
