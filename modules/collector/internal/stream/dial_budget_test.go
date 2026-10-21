package stream

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 建连限额在窗口内用完后等到最早一次滑出窗口。
func TestDialBudgetWaitsForTheWindow(t *testing.T) {
	budget := newDialBudget(2, 200*time.Millisecond)
	ctx := context.Background()
	start := time.Now()
	require.NoError(t, budget.wait(ctx))
	require.NoError(t, budget.wait(ctx))
	require.Less(t, time.Since(start), 50*time.Millisecond)
	require.NoError(t, budget.wait(ctx))
	require.GreaterOrEqual(t, time.Since(start), 180*time.Millisecond)
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	require.NoError(t, newDialBudget(0, time.Second).wait(cancelled), "没有限额不等待")
	full := newDialBudget(1, time.Hour)
	require.NoError(t, full.wait(ctx))
	require.ErrorIs(t, full.wait(cancelled), context.Canceled)
}
