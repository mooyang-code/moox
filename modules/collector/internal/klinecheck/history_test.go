package klinecheck

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// markMissing 把 [from, to) 内每分钟登记为缺失。
func markMissing(rows *fakeRows, subject string, from, to time.Time) {
	if rows.missing[subject] == nil {
		rows.missing[subject] = map[int64]bool{}
	}
	for t := from; t.Before(to); t = t.Add(time.Minute) {
		rows.missing[subject][t.Unix()] = true
	}
}

// 历史缺口只报每个标的自己第一根 K 线之后的缺失：新上市（前面没有数据）与范围内一根都没有的标的不报，
// 中间的孔洞和末端到生效点之间的缺失要报；范围相同的缺口合并成一组。
func TestCheckHistoryReportsHolesAfterEachSubjectsFirstBar(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	horizon := base.Add(-historyScanBars * time.Minute)
	rows := &fakeRows{missing: map[string]map[int64]bool{}}
	hole := base.Add(-30 * time.Minute)
	markMissing(rows, "A-USDT", hole, hole.Add(3*time.Minute))       // 中间的孔洞
	markMissing(rows, "E-USDT", hole, hole.Add(3*time.Minute))       // 与 A 相同的孔洞，合并成一组
	markMissing(rows, "B-USDT", horizon, base.Add(-100*time.Minute)) // 新上市：前面没有数据
	markMissing(rows, "C-USDT", horizon, base)                       // 范围内一根都没有
	markMissing(rows, "D-USDT", base.Add(-10*time.Minute), base)     // 历史在生效点之前就断了
	checker := &Checker{Plans: fakePlans{[]*store.FetchPlanRecord{
		record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT", "B-USDT", "C-USDT", "D-USDT", "E-USDT"),
		record(t, 2, store.FetchPlanApplied, base.Add(time.Hour).Unix(), "A-USDT", "B-USDT", "C-USDT", "D-USDT", "E-USDT", "F-USDT"),
	}}, Rows: rows, Now: func() time.Time { return base.Add(2 * time.Hour) }}

	result, err := checker.CheckHistory(context.Background(), "crypto", "ds", "1m")
	require.NoError(t, err)
	require.Equal(t, horizon, result.From)
	require.Equal(t, base, result.To, "范围止于最早的生效点，之后的计划段不在其中")
	require.Equal(t, 5, result.Subjects)
	require.Equal(t, 4, result.WithHistory, "C 在范围内没有任何数据")
	require.Equal(t, []HistoryGap{
		{DatasetID: "ds", Frequency: "1m", From: hole, To: hole.Add(3 * time.Minute), Missing: 3, Subjects: []string{"A-USDT", "E-USDT"}},
		{DatasetID: "ds", Frequency: "1m", From: base.Add(-10 * time.Minute), To: base, Missing: 10, Subjects: []string{"D-USDT"}},
	}, result.Gaps)

	// 不在任何计划里的数据集没有可检查的范围。
	none, err := checker.CheckHistory(context.Background(), "crypto", "other", "1m")
	require.NoError(t, err)
	require.Nil(t, none)
}

// 一次只扫一个数据集：同一空间两次扫描至少间隔 historyScanSpacing，同一数据集一天内不重复。
func TestScanHistoryScansOneDatasetPerSpacingAndOncePerInterval(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	rows := &fakeRows{missing: map[string]map[int64]bool{}}
	markMissing(rows, "A-USDT", base.Add(-30*time.Minute), base.Add(-29*time.Minute))
	records := []*store.FetchPlanRecord{
		recordFor(t, 1, "ds1", store.FetchPlanApplied, base.Unix(), "A-USDT"),
		recordFor(t, 2, "ds2", store.FetchPlanApplied, base.Unix(), "A-USDT"),
	}
	now := base.Add(48 * time.Hour)
	backfiller := &Backfiller{Checker: &Checker{Plans: fakePlans{records}, Rows: rows, Now: func() time.Time { return now }}, Plans: fakePlans{records}, Now: func() time.Time { return now }}
	ctx := context.Background()

	first, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "ds1", first.DatasetID)
	require.Len(t, first.Gaps, 1)

	now = now.Add(time.Minute)
	again, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, again, "两次扫描之间不足 historyScanSpacing")

	now = now.Add(historyScanSpacing)
	second, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "ds2", second.DatasetID)

	now = now.Add(historyScanSpacing)
	none, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, none, "两个数据集都已在一天内扫过")

	now = now.Add(historyScanInterval)
	next, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Equal(t, "ds1", next.DatasetID, "过了一天重新扫描")
}

type failingRows struct{ err error }

func (f failingRows) ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error) {
	return nil, f.err
}

// 读取失败返回错误，一小时后重试，不等一整天，也不在每个节拍上反复重试。
func TestScanHistoryRetriesAfterAnHourWhenReadingFails(t *testing.T) {
	base := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	records := []*store.FetchPlanRecord{record(t, 1, store.FetchPlanApplied, base.Unix(), "A-USDT")}
	now := base.Add(48 * time.Hour)
	checker := &Checker{Plans: fakePlans{records}, Rows: failingRows{errors.New("read timeout")}, Now: func() time.Time { return now }}
	backfiller := &Backfiller{Checker: checker, Plans: fakePlans{records}, Now: func() time.Time { return now }}
	ctx := context.Background()

	_, err := backfiller.ScanHistory(ctx, "crypto")
	require.ErrorContains(t, err, "read timeout")

	now = now.Add(historyScanSpacing)
	skipped, err := backfiller.ScanHistory(ctx, "crypto")
	require.NoError(t, err)
	require.Nil(t, skipped, "失败后一小时内不重试")

	now = now.Add(historyRetryAfter)
	_, err = backfiller.ScanHistory(ctx, "crypto")
	require.ErrorContains(t, err, "read timeout", "过了一小时再试一次")
}
