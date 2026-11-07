package klinecheck

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/stretchr/testify/require"
)

// 统一数据集：永续确认行情源没有的 K 线，补齐通知带 absent_series（标的加永续的 tag），不带 absent_subjects，
// 因子只会扣永续这一条序列；单序列数据集的确认缺失仍用 absent_subjects（见 TestBackfillNotifiesFilledRangesAfterAJobSucceeds）。
func TestBackfillNotifiesAbsentSeriesOfUnifiedDataset(t *testing.T) {
	backfiller, _, _, base := newUnifiedFixture(t)
	ctx := context.Background()
	var got []FilledRange
	backfiller.Notify = func(_ context.Context, filled []FilledRange) error { got = append(got, filled...); return nil }
	jobID, _, err := backfiller.Submit(ctx, JobRequest{SpaceID: "crypto", From: base, To: base.Add(time.Hour)}, "manual")
	require.NoError(t, err)
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	backfiller.Wait()
	require.NoError(t, backfiller.Tick(ctx, "crypto"))
	require.NoError(t, backfiller.Store.AddAbsences(ctx, []store.SourceAbsence{
		{SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", SubjectID: "BTC-USDT", SeriesTag: swapTag, From: base.Add(3 * time.Minute), To: base.Add(4 * time.Minute), Bars: 1},
	}))
	status, err := backfiller.Status(ctx, jobID)
	require.NoError(t, err)

	got = nil
	backfiller.notifyFilled(ctx, status.Job)
	require.Len(t, got, 2)
	require.Empty(t, got[0].Absent)
	require.Empty(t, got[0].AbsentSeries, "整个补齐范围不带确认缺失")
	require.Empty(t, got[1].Absent, "统一数据集的确认缺失不能按标的带出，否则会连现货一起扣掉")
	require.Equal(t, []FilledSeries{{SubjectID: "BTC-USDT", SeriesTag: swapTag}}, got[1].AbsentSeries)
}

type captureInvoker struct {
	request *factorpb.NotifyPeriodsFilledReq
}

func (c *captureInvoker) Invoke(_ context.Context, _ string, _ string, request any, _ any) error {
	c.request = request.(*factorpb.NotifyPeriodsFilledReq)
	return nil
}

// 通知请求里的 absent_series 原样带出（标的与 series_tag）。
func TestFactorNotifierCarriesAbsentSeries(t *testing.T) {
	invoker := &captureInvoker{}
	from := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	err := FactorNotifier(invoker)(context.Background(), []FilledRange{{
		SpaceID: "crypto", DatasetID: "ds", Frequency: "1m", From: from, To: from.Add(time.Minute),
		AbsentSeries: []FilledSeries{{SubjectID: "BTC-USDT", SeriesTag: swapTag}},
	}})
	require.NoError(t, err)
	require.Len(t, invoker.request.GetFilled(), 1)
	item := invoker.request.GetFilled()[0]
	require.Empty(t, item.GetAbsentSubjects())
	require.Len(t, item.GetAbsentSeries(), 1)
	require.Equal(t, "BTC-USDT", item.GetAbsentSeries()[0].GetSubjectId())
	require.Equal(t, swapTag, item.GetAbsentSeries()[0].GetSeriesTag())
}
