package storageio

import (
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

const (
	testSpotTag = "venue:binance|market:spot|source:spot_http"
	testSwapTag = "venue:binance|market:swap|source:swap_http"
)

func seriesRef(subject, tag string) seriesset.Series {
	return seriesset.Series{SubjectID: subject, SeriesTag: tag}
}

// 统一数据集：周期完成标记带按序列的全集与失败名单，明细里的失败序列、空洞序列、跳过的 tag 去重排序后写出。
func TestMarkerToProtoCarriesSeriesLists(t *testing.T) {
	universe := []seriesset.Series{
		seriesRef("ETH", testSwapTag), seriesRef("BTC", testSpotTag), seriesRef("BTC", testSwapTag), seriesRef("ETH", testSpotTag),
	}
	marker := PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m", PeriodTime: 1791115200, Status: "degraded",
		UniverseSubjects: []string{"BTC", "ETH"}, FailedSubjects: []string{"ETH"},
		UniverseSeries: universe, FailedSeries: []seriesset.Series{seriesRef("ETH", testSwapTag)},
		Factors: []FactorState{{
			FactorID: "rank", MemberKey: "rank", MemberKind: "fixed", Status: "degraded", FailedSubjects: []string{"ETH"},
			FailedSeries: []seriesset.Series{seriesRef("ETH", testSwapTag), seriesRef("ETH", testSwapTag)}, GapSeries: []seriesset.Series{seriesRef("ETH", testSwapTag)},
			SkippedSeriesTags: []string{testSwapTag, testSwapTag}, SourceHash: "s", DefinitionHash: "d",
		}},
	}
	protoMarker, err := markerToProto(marker)
	require.NoError(t, err)
	require.Len(t, protoMarker.GetUniverseSeries(), 4)
	require.Equal(t, "BTC", protoMarker.GetUniverseSeries()[0].GetSubjectId(), "按（标的、series_tag）排序")
	require.Equal(t, testSpotTag, protoMarker.GetUniverseSeries()[0].GetSeriesTag())
	require.Len(t, protoMarker.GetFailedSeries(), 1)
	detail := &factorpb.PeriodDetail{}
	require.NoError(t, proto.Unmarshal(protoMarker.GetDetail(), detail))
	state := detail.GetFactors()[0]
	require.Len(t, state.GetFailedSeries(), 1)
	require.Len(t, state.GetGapSeries(), 1)
	require.Equal(t, []string{testSwapTag}, state.GetSkippedSeriesTags())

	// 读回：冻结全集带按序列的名单。
	require.Equal(t, normalizedSeries(universe), seriesFromMarker(protoMarker.GetUniverseSeries()))
}

func TestValidatePeriodDetailSeriesRules(t *testing.T) {
	universe := []seriesset.Series{seriesRef("BTC", testSpotTag), seriesRef("BTC", testSwapTag)}
	ref := func(subject, tag string) *factorpb.SeriesRef {
		return &factorpb.SeriesRef{SubjectId: subject, SeriesTag: tag}
	}
	build := func(mutate func(*factorpb.FactorPeriodState)) *factorpb.PeriodDetail {
		state := &factorpb.FactorPeriodState{
			FactorId: "rank", MemberKey: "rank", MemberKind: "fixed", Status: "degraded", SpecHash: "s", DefinitionHash: "d",
			FailedSubjects: []string{"BTC"}, FailedSeries: []*factorpb.SeriesRef{ref("BTC", testSwapTag)},
			SkippedSeriesTags: []string{testSwapTag},
		}
		mutate(state)
		return &factorpb.PeriodDetail{SourceDatasetId: "bars", Factors: []*factorpb.FactorPeriodState{state}}
	}
	require.NoError(t, ValidatePeriodDetail(build(func(*factorpb.FactorPeriodState) {}), []string{"BTC"}, universe, "degraded"))

	for name, mutate := range map[string]func(*factorpb.FactorPeriodState){
		"failed series not in universe": func(s *factorpb.FactorPeriodState) { s.FailedSeries = []*factorpb.SeriesRef{ref("ETH", testSwapTag)} },
		"gap series not failed":         func(s *factorpb.FactorPeriodState) { s.GapSeries = []*factorpb.SeriesRef{ref("BTC", testSpotTag)} },
		"skipped tag unknown":           func(s *factorpb.FactorPeriodState) { s.SkippedSeriesTags = []string{"venue:okx"} },
		"complete with failed series": func(s *factorpb.FactorPeriodState) {
			s.Status, s.FailedSubjects = "complete", nil
		},
	} {
		require.Error(t, ValidatePeriodDetail(build(mutate), []string{"BTC"}, universe, "degraded"), name)
	}
	// 单序列数据集（没有序列全集）不能带按序列的名单。
	require.Error(t, ValidatePeriodDetail(build(func(*factorpb.FactorPeriodState) {}), []string{"BTC"}, nil, "degraded"))
	require.NoError(t, ValidatePeriodDetail(build(func(s *factorpb.FactorPeriodState) {
		s.FailedSeries, s.SkippedSeriesTags = nil, nil
	}), []string{"BTC"}, nil, "degraded"))
}
