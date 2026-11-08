package seriesstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 统一数据集：同一个标的的两条序列各自同步到哪里要分开看；并集的 Latest 会被走得快的序列盖住。
func TestTagBoundsKeepsLaggingSeriesApart(t *testing.T) {
	store := openTest(t, t.TempDir())
	rows := NewColumns(testFields, 6)
	for minute := 0; minute <= 2; minute++ {
		bar(rows, minute, "spot", 100+float64(minute))
	}
	for minute := 0; minute <= 1; minute++ { // 永续少一根
		bar(rows, minute, "swap", 200+float64(minute))
	}
	_, err := store.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTC", From: at(0), To: at(3), Rows: rows}, WriteOptions{FillGaps: true})
	require.NoError(t, err)

	union, ok, err := store.Bounds(testSrc, "BTC")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(2), union.Latest, "并集的最新时间被现货盖住")

	perTag, err := store.TagBounds(testSrc, "BTC")
	require.NoError(t, err)
	require.Equal(t, at(2), perTag["spot"].Latest)
	require.Equal(t, at(1), perTag["swap"].Latest, "永续没有被当成已同步")
	require.Equal(t, at(0), perTag["swap"].Earliest)

	empty, err := store.TagBounds(testSrc, "ETH")
	require.NoError(t, err)
	require.Empty(t, empty)
}
