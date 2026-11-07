package localdata

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 统一数据集：同一个数据集里的现货、永续两条序列（生产上的实际 series_tag）。
const (
	spotTag = "venue:binance|market:spot|source:spot_http"
	swapTag = "venue:binance|market:swap|source:swap_http"
)

func unifiedUniverse(subjects ...string) []seriesset.Series {
	var out []seriesset.Series
	for _, subject := range subjects {
		out = append(out, seriesset.Series{SubjectID: subject, SeriesTag: spotTag}, seriesset.Series{SubjectID: subject, SeriesTag: swapTag})
	}
	return out
}

// liveSeries 按统一数据集的事件（按序列的全集与失败名单）跑一期。
func (fx *fixture) liveSeries(t *testing.T, target time.Time, subjects []string, failed ...seriesset.Series) pipeline.Outcome {
	t.Helper()
	failedSubjects := make([]string, 0, len(failed))
	for _, item := range failed {
		failedSubjects = append(failedSubjects, item.SubjectID)
	}
	plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
		Set: fx.set.Set, Members: fx.set.Members, PeriodTime: target, Universe: subjects, UpstreamFailed: failedSubjects,
		UniverseSeries: unifiedUniverse(subjects...), UpstreamFailedSeries: failed,
		CarryColumns: []string{"close", "volume"}, TriggerEventID: "evt",
	})
	require.NoError(t, err)
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	fx.data.Drain()
	return outcome
}

func (fx *fixture) resultTag(subject, tag string, m int) (float64, bool) {
	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	row, ok := fx.st.results[subject+at(m).Format(time.RFC3339)+tag]
	if !ok {
		return 0, false
	}
	value, isNumber := row.Fields["mean"].(float64)
	return value, ok && isNumber
}

func (fx *fixture) tagLatest(t *testing.T, subject, tag string) time.Time {
	t.Helper()
	bounds, err := fx.data.cfg.Store.TagBounds(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}, subject)
	require.NoError(t, err)
	return bounds[tag].Latest
}

// 永续晚到 1 分钟：现货照常算出，永续不被当成已同步（本地最新时间不前进）；下一期永续补齐后按原始行重新同步，没有留下填充行。
func TestUnifiedSwapLateOneMinuteSpotComputedSwapNotSynced(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC", "ETH"}
	for _, s := range subjects {
		fx.st.putTag(s, spotTag, 100, 5, 6, 7, 8, 9)
		fx.st.putTag(s, swapTag, 200, 5, 6, 7, 8, 9)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))

	// 第 10 根：现货到了，永续还没到。
	for _, s := range subjects {
		fx.st.putTag(s, spotTag, 100, 10)
	}
	outcome := fx.liveSeries(t, at(10), subjects)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, subjects, outcome.FailedSubjects, "按标的口径：任一序列失败，标的记失败")
	require.ElementsMatch(t, []seriesset.Series{{SubjectID: "BTC", SeriesTag: swapTag}, {SubjectID: "ETH", SeriesTag: swapTag}}, outcome.FailedSeries)
	for _, s := range subjects {
		value, ok := fx.resultTag(s, spotTag, 10)
		require.True(t, ok, "现货照常算出")
		base := 100.0
		require.InDelta(t, (base+8+base+9+base+10)/3, value, 1e-9)
		_, ok = fx.resultTag(s, swapTag, 10)
		require.False(t, ok, "永续这一期没有源行，不产生结果行")
		require.Equal(t, at(10), fx.tagLatest(t, s, spotTag))
		require.Equal(t, at(9), fx.tagLatest(t, s, swapTag), "永续不被当成已同步")
	}
	require.Equal(t, "degraded", fx.markerStatus(t, 0))

	// 第 11 根：永续补上第 10、11 根，现货到第 11 根；起点按各序列自己的进度，永续从第 10 根起取。
	for _, s := range subjects {
		fx.st.putTag(s, swapTag, 200, 10, 11)
		fx.st.putTag(s, spotTag, 100, 11)
	}
	outcome = fx.liveSeries(t, at(11), subjects)
	require.Equal(t, "complete", outcome.Status)
	for _, s := range subjects {
		value, ok := fx.resultTag(s, swapTag, 11)
		require.True(t, ok)
		require.InDelta(t, (209+210+211)/3.0, value, 1e-9, "永续第 10 根是真实行，不是填充值")
		require.Equal(t, at(11), fx.tagLatest(t, s, swapTag))
	}
}

// 永续缺一个币：现货的序列照常算出，缺的那条序列记为失败，其余永续序列照常。
func TestUnifiedMissingSwapSeriesOnlyFailsThatSeries(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC", "ETH"}
	for _, s := range subjects {
		fx.st.putTag(s, spotTag, 100, 5, 6, 7, 8, 9, 10)
	}
	fx.st.putTag("BTC", swapTag, 200, 5, 6, 7, 8, 9, 10)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))

	outcome := fx.liveSeries(t, at(10), subjects)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, []seriesset.Series{{SubjectID: "ETH", SeriesTag: swapTag}}, outcome.FailedSeries)
	require.Equal(t, []string{"ETH"}, outcome.FailedSubjects)
	for _, tag := range []string{spotTag, swapTag} {
		_, ok := fx.resultTag("BTC", tag, 10)
		require.True(t, ok)
	}
	_, ok := fx.resultTag("ETH", spotTag, 10)
	require.True(t, ok, "ETH 现货照常算出")
	_, ok = fx.resultTag("ETH", swapTag, 10)
	require.False(t, ok)
}

// 两个市场都按时到达时，统一数据集同样走热路径（每条序列新增连续的 1 根），结果与逐期落盘一致。
func TestUnifiedBothMarketsOnTimeUsesHotPath(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC", "ETH"}
	for _, s := range subjects {
		fx.st.putTag(s, spotTag, 100, 5, 6, 7, 8, 9)
		fx.st.putTag(s, swapTag, 200, 5, 6, 7, 8, 9)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	for _, s := range subjects {
		fx.st.putTag(s, spotTag, 100, 10)
		fx.st.putTag(s, swapTag, 200, 10)
	}
	hotBefore := fx.data.hotWrites.Load()
	outcome := fx.liveSeries(t, at(10), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.Equal(t, int64(len(subjects)), fx.data.hotWrites.Load()-hotBefore)
	for _, s := range subjects {
		for _, tag := range []string{spotTag, swapTag} {
			_, ok := fx.resultTag(s, tag, 10)
			require.True(t, ok)
			require.Equal(t, at(10), fx.tagLatest(t, s, tag))
		}
	}
}

func (fx *fixture) markerStatus(t *testing.T, index int) string {
	t.Helper()
	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	require.Greater(t, len(fx.st.markers), index)
	return fx.st.markers[index].Status
}
