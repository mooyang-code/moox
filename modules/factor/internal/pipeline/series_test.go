package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 统一数据集：同一个数据集里放现货、永续两条序列（生产上的实际 series_tag）。
const (
	tagSpot = "venue:binance|market:spot|source:spot_http"
	tagSwap = "venue:binance|market:swap|source:swap_http"
)

func series(subject, tag string) seriesset.Series {
	return seriesset.Series{SubjectID: subject, SeriesTag: tag}
}

// unifiedPlan 造统一数据集的实时计划：每个标的都有现货、永续两条序列。
func unifiedPlan(target time.Time, factors []domain.FactorDef, subjects []string) Plan {
	plan := computePlan(target, factors, subjects)
	for _, subject := range subjects {
		plan.ExpectedSeries = append(plan.ExpectedSeries, series(subject, tagSpot), series(subject, tagSwap))
	}
	plan.ExpectedSeries = uniqueSeriesSorted(plan.ExpectedSeries)
	return plan
}

// unifiedData 按序列给出源数据：absent 里的序列没有源行（例如永续缺这个币，或永续晚到）。
func unifiedData(subjects []string, target time.Time, bars int, absent ...seriesset.Series) *frameData {
	skip := seriesSet(absent)
	return &frameData{
		read: func(_ context.Context, _ Plan, _ time.Time) (map[string]*storageio.Frame, error) {
			frames := map[string]*storageio.Frame{}
			for s, subject := range subjects {
				frame := &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}}
				for i := bars - 1; i >= 0; i-- {
					at := target.Add(-time.Duration(i) * time.Minute)
					for t, tag := range []string{tagSpot, tagSwap} {
						if _, gone := skip[series(subject, tag)]; gone {
							continue
						}
						frame.Rows = append(frame.Rows, []any{at, tag, float64((s+1)*100 + t*10 + bars - 1 - i)})
					}
				}
				frames[subject] = frame
			}
			return frames, nil
		},
		failedSeries: absent,
	}
}

func runUnified(t *testing.T, plan Plan, data *frameData) (LoadResult, Computation, []storageio.ResultRow) {
	t.Helper()
	runner := NewRunner(&fakeStore{}, data, periodclock.Continuous{}, Config{})
	loaded, err := runner.Load(context.Background(), plan)
	require.NoError(t, err)
	computation, err := runner.Compute(context.Background(), plan, loaded)
	require.NoError(t, err)
	rows, err := runner.Assemble(plan, loaded, computation)
	require.NoError(t, err)
	return loaded, computation, rows
}

func rowFor(t *testing.T, rows []storageio.ResultRow, subject, tag string) storageio.ResultRow {
	t.Helper()
	for _, row := range rows {
		if row.SubjectID == subject && row.SeriesTag == tag {
			return row
		}
	}
	t.Fatalf("没有 %s/%s 的结果行", subject, tag)
	return storageio.ResultRow{}
}

// 永续缺一个币：现货的截面因子照常算出，成员记 degraded，只跳过永续；永续的行写 NULL。
func TestUnifiedCrossSectionSkipsOnlyTheMarketMissingASeries(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	spread := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	mean := testPipelineFactor("mean", domain.FactorTypeTimeSeries, 2, false)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(target, []domain.FactorDef{spread, mean}, subjects)
	loaded, computation, rows := runUnified(t, plan, unifiedData(subjects, target, 2, series("SOL", tagSwap)))

	require.Equal(t, subjects, loaded.Available, "SOL 现货在，标的仍可算")
	require.Equal(t, []string{"SOL"}, loaded.FailedSubjects)
	require.Equal(t, []seriesset.Series{series("SOL", tagSwap)}, loaded.FailedSeries)
	require.Len(t, loaded.AvailableSeries, 5)

	state := computation.FactorStates["spread"]
	require.Equal(t, "degraded", state.Status)
	require.Equal(t, []string{tagSwap}, state.SkippedSeriesTags)
	require.Equal(t, []seriesset.Series{series("SOL", tagSwap)}, state.FailedSeries)
	require.Equal(t, []string{"SOL"}, state.FailedSubjects)
	meanState := computation.FactorStates["mean"]
	require.Equal(t, "degraded", meanState.Status)
	require.Empty(t, meanState.SkippedSeriesTags, "时序因子不跳过 tag，只记失败序列")
	require.Equal(t, []seriesset.Series{series("SOL", tagSwap)}, meanState.FailedSeries)

	cross := computation.Results["spread"][""]
	require.Len(t, cross.Rows, 3, "只有现货三条序列出结果")
	ranks := map[string]any{}
	for _, row := range cross.Rows {
		require.Equal(t, tagSpot, row[1])
		ranks[row[2].(string)] = row[3]
	}
	require.Equal(t, map[string]any{"BTC": 1.0 / 3, "ETH": 2.0 / 3, "SOL": 1.0}, ranks, "现货在三个标的之间排名")

	require.Len(t, rows, 5, "SOL 永续没有源行，不产生行")
	for _, subject := range subjects {
		require.NotNil(t, rowFor(t, rows, subject, tagSpot).Fields["spread"])
	}
	for _, subject := range []string{"BTC", "ETH"} {
		swap := rowFor(t, rows, subject, tagSwap)
		require.Nil(t, swap.Fields["spread"], "被跳过的永续序列写 NULL")
		require.NotNil(t, swap.Fields["mean"], "时序因子逐序列算，永续照常有值")
		require.NotContains(t, string(swap.Fields[factorname.ParamsColumn].(storageio.JSONText)), `"spread"`, "被跳过的序列不记参数")
		spot := rowFor(t, rows, subject, tagSpot)
		require.Contains(t, string(spot.Fields[factorname.ParamsColumn].(storageio.JSONText)), `"spread"`)
	}
}

// 允许部分全集的截面因子不跳过：缺数的那个 tag 在可用序列里排名，成员记 degraded。
func TestUnifiedCrossSectionAllowPartialRunsOnAvailableSeries(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	rank := testPipelineFactor("rank", domain.FactorTypeCrossSection, 2, true)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(target, []domain.FactorDef{rank}, subjects)
	_, computation, rows := runUnified(t, plan, unifiedData(subjects, target, 2, series("SOL", tagSwap)))

	state := computation.FactorStates["rank"]
	require.Equal(t, "degraded", state.Status)
	require.Empty(t, state.SkippedSeriesTags)
	require.Len(t, computation.Results["rank"][""].Rows, 5)
	require.Equal(t, 0.5, rowFor(t, rows, "BTC", tagSwap).Fields["rank"], "永续只在 BTC、ETH 之间排名")
	require.Equal(t, 1.0, rowFor(t, rows, "ETH", tagSwap).Fields["rank"])
}

// 全部 tag 都跳过才记 skipped：现货、永续各缺一个币。
func TestUnifiedCrossSectionAllTagsSkippedIsSkipped(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	spread := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(target, []domain.FactorDef{spread}, subjects)
	_, computation, rows := runUnified(t, plan, unifiedData(subjects, target, 2, series("SOL", tagSwap), series("ETH", tagSpot)))

	state := computation.FactorStates["spread"]
	require.Equal(t, "skipped", state.Status)
	require.Empty(t, state.SkippedSeriesTags, "skipped 表示全部 tag 都跳过，不再逐个列出")
	require.ElementsMatch(t, []seriesset.Series{series("SOL", tagSwap), series("ETH", tagSpot)}, state.FailedSeries)
	require.Empty(t, computation.Results["spread"])
	for _, row := range rows {
		require.Nil(t, row.Fields["spread"])
	}
}

// 永续整体晚到：现货的截面因子与时序因子都照常算出，只有永续的序列记为失败；不会因为失败标的名单里出现全部标的而整体跳过。
func TestUnifiedSwapLateLeavesSpotComputed(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	spread := testPipelineFactor("spread", domain.FactorTypeCrossSection, 2, false)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(target, []domain.FactorDef{spread}, subjects)
	var swapAll []seriesset.Series
	for _, subject := range subjects {
		swapAll = append(swapAll, series(subject, tagSwap))
	}
	// 事件里永续的序列全部上游失败：现货序列照常读取与计算。
	plan.UpstreamFailedSeries = swapAll
	plan.UpstreamFailed = subjects
	data := unifiedData(subjects, target, 2, swapAll...)
	data.failedSeries = nil
	loaded, computation, rows := runUnified(t, plan, data)

	require.Len(t, loaded.AvailableSeries, 3)
	state := computation.FactorStates["spread"]
	require.Equal(t, "degraded", state.Status, "只跳过永续，现货照常")
	require.Equal(t, []string{tagSwap}, state.SkippedSeriesTags)
	require.Len(t, state.FailedSeries, 3)
	require.Len(t, rows, 3)
	for _, row := range rows {
		require.Equal(t, tagSpot, row.SeriesTag)
		require.NotNil(t, row.Fields["spread"])
	}
}

// 上游判为失败的序列，本地即使已有这一期的数据也写 NULL、不记参数，也不参与截面排名（统一数据集执行计划 3.5）。
func TestUnifiedUpstreamFailedSeriesWritesNullEvenWithLocalData(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	mean := testPipelineFactor("mean", domain.FactorTypeTimeSeries, 2, false)
	rank := testPipelineFactor("rank", domain.FactorTypeCrossSection, 2, true)
	subjects := []string{"BTC", "ETH", "SOL"}
	plan := unifiedPlan(target, []domain.FactorDef{mean, rank}, subjects)
	plan.UpstreamFailedSeries = []seriesset.Series{series("BTC", tagSwap)}
	plan.UpstreamFailed = []string{"BTC"}
	data := unifiedData(subjects, target, 2) // 本地每条序列都有这一期的源行
	loaded, computation, rows := runUnified(t, plan, data)

	require.Contains(t, loaded.FailedSeries, series("BTC", tagSwap))
	require.Contains(t, computation.FactorStates["mean"].FailedSeries, series("BTC", tagSwap))
	failed := rowFor(t, rows, "BTC", tagSwap)
	require.Nil(t, failed.Fields["mean"], "上游失败的序列写 NULL")
	require.Nil(t, failed.Fields["rank"])
	params := string(failed.Fields[factorname.ParamsColumn].(storageio.JSONText))
	require.NotContains(t, params, `"mean"`, "没有取得值，不记参数")
	require.NotContains(t, params, `"rank"`)
	require.NotNil(t, rowFor(t, rows, "BTC", tagSpot).Fields["mean"], "同标的另一个市场照常")
	require.Equal(t, 0.5, rowFor(t, rows, "ETH", tagSwap).Fields["rank"], "永续只在 ETH、SOL 之间排名，BTC 永续不参与")
	require.Equal(t, 1.0, rowFor(t, rows, "SOL", tagSwap).Fields["rank"])
}

// 事件里只有现货的序列失败时，标的仍然可读（永续可用）：Available 按序列推出。
func TestBuildLivePlanUnifiedKeepsSubjectsWithAnyUsableSeries(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set:     domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.TaskStatusEnabled},
		Members: domain.AsMembers(testPipelineFactor("mean", domain.FactorTypeTimeSeries, 2, false)), PeriodTime: target,
		Universe: []string{"BTC", "ETH"}, UpstreamFailed: []string{"BTC"},
		UniverseSeries: []seriesset.Series{
			series("BTC", tagSpot), series("BTC", tagSwap), series("ETH", tagSpot), series("ETH", tagSwap),
		},
		UpstreamFailedSeries: []seriesset.Series{series("BTC", tagSpot)},
		CarryColumns:         []string{"close"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC", "ETH"}, plan.Available, "BTC 的永续还在，仍然读取")
	require.Equal(t, []string{"BTC"}, plan.UpstreamFailed)
	require.Equal(t, []seriesset.Series{series("BTC", tagSpot)}, plan.UpstreamFailedSeries)
	require.Len(t, plan.ExpectedSeries, 4)
	require.True(t, plan.Multi())

	single, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{
		Set:     domain.FactorTask{SpaceID: "crypto", SourceDatasetID: "dataset_bars", Freq: "1m", Status: domain.TaskStatusEnabled},
		Members: domain.AsMembers(testPipelineFactor("mean", domain.FactorTypeTimeSeries, 2, false)), PeriodTime: target,
		Universe: []string{"BTC", "ETH"}, UpstreamFailed: []string{"BTC"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"ETH"}, single.Available, "单序列数据集：失败的标的整体不读（原有行为）")
	require.False(t, single.Multi())
}

// 统一数据集写自己的周期完成标记时带上序列全集与失败序列。
func TestUnifiedReportCarriesSeriesUniverse(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	mean := testPipelineFactor("mean", domain.FactorTypeTimeSeries, 2, false)
	subjects := []string{"BTC", "ETH"}
	plan := unifiedPlan(target, []domain.FactorDef{mean}, subjects)
	plan.TriggerEventID = "event-1"
	plan.Set.ResultDatasetID = "dataset_factor_bars"
	store := &outputStore{}
	runner := NewRunner(store, unifiedData(subjects, target, 2, series("ETH", tagSwap)), periodclock.Continuous{}, Config{})
	outcome, err := runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "degraded", outcome.Status)
	require.Len(t, store.markers, 1)
	marker := store.markers[0]
	require.Equal(t, subjects, marker.UniverseSubjects)
	require.Equal(t, plan.ExpectedSeries, marker.UniverseSeries)
	require.Equal(t, []seriesset.Series{series("ETH", tagSwap)}, marker.FailedSeries)
	require.Equal(t, []string{"ETH"}, marker.FailedSubjects)
	require.Equal(t, []seriesset.Series{series("ETH", tagSwap)}, marker.Factors[0].FailedSeries)
}
