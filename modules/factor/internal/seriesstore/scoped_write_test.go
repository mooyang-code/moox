package seriesstore

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// 统一数据集的现货、永续分两次导入：Scoped 写入只替换本次文件里出现的序列，另一个市场的数据保持不动。
func TestScopedWriteKeepsOtherSeriesTags(t *testing.T) {
	const spot, swap = "venue:binance|market:spot|source:spot_http", "venue:binance|market:swap|source:swap_http"
	s := openTest(t, t.TempDir())
	ctx := context.Background()

	spotRows := NewColumns(testFields, 0)
	for m := 0; m < 3; m++ {
		bar(spotRows, m, spot, float64(100+m))
	}
	_, err := s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(0), To: at(3), Rows: spotRows, Scoped: true}, WriteOptions{})
	require.NoError(t, err)

	swapRows := NewColumns(testFields, 0)
	for m := 0; m < 3; m++ {
		bar(swapRows, m, swap, float64(200+m))
	}
	// 同一区间、同一标的再写永续：现货不能被删。
	_, err = s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(0), To: at(3), Rows: swapRows, Scoped: true}, WriteOptions{})
	require.NoError(t, err)

	got, err := s.ReadRange(ctx, testSrc, []string{"BTCUSDT"}, at(0), at(10))
	require.NoError(t, err)
	cols := got["BTCUSDT"]
	require.Equal(t, 6, cols.Len())
	perTag := map[string][]float64{}
	for i := 0; i < cols.Len(); i++ {
		perTag[cols.Tags[i]] = append(perTag[cols.Tags[i]], cols.ValueAt(1, i).F)
	}
	require.Equal(t, []float64{100, 101, 102}, perTag[spot])
	require.Equal(t, []float64{200, 201, 202}, perTag[swap])

	// 重复导入同一份现货文件：幂等，永续仍在。
	_, err = s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(0), To: at(3), Rows: spotRows, Scoped: true}, WriteOptions{})
	require.NoError(t, err)
	got, err = s.ReadRange(ctx, testSrc, []string{"BTCUSDT"}, at(0), at(10))
	require.NoError(t, err)
	require.Equal(t, 6, got["BTCUSDT"].Len())

	// 不带 Scoped（同步器、对账）仍然整段替换标的的全部序列。
	_, err = s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(0), To: at(3), Rows: spotRows}, WriteOptions{})
	require.NoError(t, err)
	got, err = s.ReadRange(ctx, testSrc, []string{"BTCUSDT"}, at(0), at(10))
	require.NoError(t, err)
	require.Equal(t, 3, got["BTCUSDT"].Len())
}
