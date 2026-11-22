package rpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 收益拆解的区间对齐到整点：起点向前取整，终点延到所在小时末（含整点终点所在的那一小时）。
func TestAlignPnlWindow(t *testing.T) {
	at := func(hour, minute int) time.Time { return time.Date(2026, 11, 22, hour, minute, 0, 0, time.UTC) }
	start, end := alignPnlWindow(at(8, 25), at(15, 40))
	require.Equal(t, at(8, 0), start)
	require.Equal(t, at(16, 0).Add(-time.Millisecond), end)
	start, end = alignPnlWindow(at(8, 0), at(15, 0))
	require.Equal(t, at(8, 0), start)
	require.Equal(t, at(16, 0).Add(-time.Millisecond), end)
}
