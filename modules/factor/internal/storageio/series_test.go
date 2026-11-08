package storageio

import (
	"fmt"
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

// 统一数据集：周期完成标记带按序列的全集与失败名单，明细里的失败序列、跳过的 tag 去重排序后写出。
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
			FailedSeries:      []seriesset.Series{seriesRef("ETH", testSwapTag), seriesRef("ETH", testSwapTag)},
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
	require.Len(t, detail.GetSharedFailedSeries(), 1, "失败序列在周期级放一份")
	require.Empty(t, state.GetFailedSeries(), "成员级只放与周期级不同的部分")
	require.Equal(t, []seriesset.Series{seriesRef("ETH", testSwapTag)}, MemberFailedSeries(detail, state), "读回时还原成完整的成员级名单")
	require.Equal(t, []string{testSwapTag}, state.GetSkippedSeriesTags())

	// 读回：冻结全集带按序列的名单。
	require.Equal(t, normalizedSeries(universe), seriesFromMarker(protoMarker.GetUniverseSeries()))

	// 失败序列的标的不在失败标的里：先在因子这边报错，不等 Storage 拒发。
	marker.FailedSeries = append(marker.FailedSeries, seriesRef("BTC", testSpotTag))
	_, err = markerToProto(marker)
	require.ErrorContains(t, err, "不在失败标的里")
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

// 永续整体晚到：每个成员的失败序列都是全部永续序列。230 个成员 × 数百序列逐成员复制会超出事件体积上限，
// 抽成周期级的一份后体积很小，读回成员级名单与原来等价。
func TestMarkerSharesFailedSeriesAcrossMembersAndStaysWithinLimit(t *testing.T) {
	const members, subjects = 230, 500
	var universe, swap []seriesset.Series
	var subjectIDs []string
	for i := 0; i < subjects; i++ {
		subject := fmt.Sprintf("SYMBOL%04d-USDT", i)
		subjectIDs = append(subjectIDs, subject)
		universe = append(universe, seriesRef(subject, testSpotTag), seriesRef(subject, testSwapTag))
		swap = append(swap, seriesRef(subject, testSwapTag))
	}
	marker := PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m", PeriodTime: 1791115200, Status: "degraded",
		UniverseSubjects: subjectIDs, FailedSubjects: subjectIDs, UniverseSeries: universe, FailedSeries: swap,
	}
	for i := 0; i < members; i++ {
		state := FactorState{
			FactorID: fmt.Sprintf("factor%03d", i), MemberKey: fmt.Sprintf("factor%03d", i), MemberKind: "fixed", Status: "degraded",
			FailedSubjects: subjectIDs, FailedSeries: swap, SourceHash: "s", DefinitionHash: "d",
		}
		if i == 7 { // 个别成员多失败一条现货序列
			state.FailedSeries = append(append([]seriesset.Series(nil), swap...), seriesRef(subjectIDs[0], testSpotTag))
		}
		marker.Factors = append(marker.Factors, state)
	}
	protoMarker, err := markerToProto(marker)
	require.NoError(t, err)
	require.Less(t, proto.Size(protoMarker), MaxMarkerBytes)
	detail := &factorpb.PeriodDetail{}
	require.NoError(t, proto.Unmarshal(protoMarker.GetDetail(), detail))
	require.Len(t, detail.GetSharedFailedSeries(), subjects)
	for _, state := range detail.GetFactors() {
		if state.GetMemberKey() == "factor007" {
			require.Equal(t, []seriesset.Series{seriesRef(subjectIDs[0], testSpotTag)}, seriesFromFactorPB(state.GetFailedSeries()), "成员级只放不同的部分")
			require.Len(t, MemberFailedSeries(detail, state), subjects+1)
			continue
		}
		require.Empty(t, state.GetFailedSeries())
		require.Equal(t, normalizedSeries(swap), MemberFailedSeries(detail, state), "还原后等价：%s", state.GetMemberKey())
	}
}

// 标记超过体积上限时在写入前报清晰的中文错误。
func TestMarkerToProtoRejectsOversizedMarker(t *testing.T) {
	var factors []FactorState
	var subjects []string
	for i := 0; i < 20000; i++ {
		subjects = append(subjects, fmt.Sprintf("SYMBOL%06d-USDT", i))
	}
	for i := 0; i < 400; i++ {
		factors = append(factors, FactorState{FactorID: fmt.Sprintf("f%03d", i), MemberKey: fmt.Sprintf("f%03d", i), MemberKind: "fixed", Status: "degraded",
			FailedSubjects: subjects, SourceHash: "s", DefinitionHash: "d"})
	}
	_, err := markerToProto(PeriodMarker{SpaceID: "crypto", ResultDatasetID: "r", SourceDatasetID: "b", Frequency: "1m", PeriodTime: 1, Status: "degraded",
		UniverseSubjects: subjects, FailedSubjects: subjects, Factors: factors})
	require.ErrorContains(t, err, "超过上限")
}

func seriesFromFactorPB(values []*factorpb.SeriesRef) []seriesset.Series {
	out := make([]seriesset.Series, len(values))
	for i, value := range values {
		out[i] = seriesset.Series{SubjectID: value.GetSubjectId(), SeriesTag: value.GetSeriesTag()}
	}
	return out
}
