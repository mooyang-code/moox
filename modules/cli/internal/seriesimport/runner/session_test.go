package runner

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/stretchr/testify/require"
)

func stockRequest(paths ...string) Request {
	return Request{Space: "stockcn", Dataset: "bars", Freq: "5m", Paths: paths, Format: "csv", Step: 5 * time.Minute, BatchRows: 100}
}

// A 股按交易时段判断相邻：跨午休、跨周末不算缺期；交易时段内真正缺的 K 线才算缺；休市时刻的行直接拒绝。
func TestStockCNContinuityFollowsTradingSessions(t *testing.T) {
	dir := t.TempDir()
	// 时间都是 UTC：周五 14:50、14:55，周一 09:30、09:35，11:25 与 13:00（北京时间）。
	continuous := writeCSV(t, dir, "600000.SH.csv", "data_time,close,volume", []string{
		"2026-10-09 06:50:00,1,1", "2026-10-09 06:55:00,1,1",
		"2026-10-12 01:30:00,1,1", "2026-10-12 01:35:00,1,1",
	})
	target := &recorder{name: "factor"}
	sum, err := Run(context.Background(), stockRequest(continuous), Meta{Fields: fields}, []Target{target}, nil)
	require.NoError(t, err)
	require.Zero(t, sum.Files[0].MissingPeriods)
	require.Equal(t, "2026-10-09T06:50:00Z", sum.Files[0].First)
	require.Equal(t, "2026-10-12T01:35:00Z", sum.Files[0].Last)

	gap := writeCSV(t, dir, "600001.SH.csv", "data_time,close,volume", []string{
		"2026-10-12 03:20:00,1,1", "2026-10-12 05:00:00,1,1", "2026-10-12 05:10:00,1,1",
	})
	_, err = Run(context.Background(), stockRequest(gap), Meta{Fields: fields}, []Target{target}, nil)
	require.ErrorContains(t, err, "缺 2 个周期")
	require.ErrorContains(t, err, "2026-10-12T03:25:00Z", "上午最后一根 11:25 缺了")
	require.ErrorContains(t, err, "2026-10-12T05:05:00Z")

	lunch := writeCSV(t, dir, "600002.SH.csv", "data_time,close,volume", []string{"2026-10-12 04:00:00,1,1"})
	_, err = Run(context.Background(), stockRequest(lunch), Meta{Fields: fields}, []Target{target}, nil)
	require.ErrorContains(t, err, "不是交易时段内")
}

// 未登记的空间报错，不当成 7×24 按固定步长检查。
func TestUnknownSpaceIsRejected(t *testing.T) {
	axis, err := newSessionAxis("crypto", 5*time.Minute)
	require.NoError(t, err)
	require.Nil(t, axis)

	_, err = newSessionAxis("no_such_space", 5*time.Minute)
	require.ErrorIs(t, err, barclock.ErrUnknownMarket)
}
