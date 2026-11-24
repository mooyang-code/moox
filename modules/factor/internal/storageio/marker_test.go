package storageio

import (
	"fmt"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func testMarker(universe, failed []string, factors ...FactorState) PeriodMarker {
	return PeriodMarker{
		SpaceID: "crypto", ResultDatasetID: "factor_result", SourceDatasetID: "bars", Frequency: "1m", PeriodTime: 1791115200, Status: "degraded",
		UniverseSubjects: universe, FailedSubjects: failed, Factors: factors,
	}
}

func testState(key string, failed ...string) FactorState {
	return FactorState{FactorID: key, MemberKey: key, MemberKind: "fixed", Status: "degraded", FailedSubjects: failed, SourceHash: "s", DefinitionHash: "d"}
}

func decodeDetail(t *testing.T, marker PeriodMarker) (*factorpb.PeriodDetail, []string) {
	t.Helper()
	protoMarker, err := markerToProto(marker)
	require.NoError(t, err)
	detail := &factorpb.PeriodDetail{}
	require.NoError(t, proto.Unmarshal(protoMarker.GetDetail(), detail))
	return detail, protoMarker.GetFailedSubjects()
}

// 明细里成员的失败标的只记周期级之外多出来的部分，经 factorname.MemberFailedSubjects 还原后与完整名单相同。
func TestMarkerToProtoStoresOnlyExtraMemberFailures(t *testing.T) {
	marker := testMarker([]string{"BTC", "ETH", "SOL"}, []string{"ETH"},
		testState("rank", "ETH", "SOL", "ETH"), testState("mean", "ETH"))
	detail, periodFailed := decodeDetail(t, marker)
	require.Equal(t, []string{"ETH"}, periodFailed)
	states := map[string]*factorpb.FactorPeriodState{}
	for _, state := range detail.GetFactors() {
		states[state.GetMemberKey()] = state
	}
	require.Equal(t, []string{"SOL"}, states["rank"].GetFailedSubjects(), "只记多出来的")
	require.Empty(t, states["mean"].GetFailedSubjects(), "与周期级相同时不重复记")
	require.Equal(t, []string{"ETH", "SOL"}, factorname.MemberFailedSubjects(periodFailed, states["rank"].GetFailedSubjects()))
	require.Equal(t, []string{"ETH"}, factorname.MemberFailedSubjects(periodFailed, states["mean"].GetFailedSubjects()))
	require.Equal(t, []string{"ETH", "SOL", "ETH"}, marker.Factors[0].FailedSubjects, "管线内部的完整名单不被改动")
}

// 校验用还原后的名单：周期有失败标的时 complete 成员不合法；成员失败标的不在全集里不合法。
func TestValidatePeriodDetailUsesRestoredMemberFailures(t *testing.T) {
	complete := testState("rank")
	complete.Status = "complete"
	_, err := markerToProto(testMarker([]string{"BTC", "ETH"}, []string{"ETH"}, complete))
	require.ErrorContains(t, err, "has failed subjects")

	_, err = markerToProto(testMarker([]string{"BTC"}, nil, testState("rank", "ETH")))
	require.ErrorContains(t, err, "not in universe_subject_ids")

	detail := &factorpb.PeriodDetail{Factors: []*factorpb.FactorPeriodState{{
		FactorId: "rank", MemberKey: "rank", MemberKind: "fixed", Status: "degraded", SpecHash: "s", DefinitionHash: "d",
	}}}
	require.NoError(t, ValidatePeriodDetail(detail, []string{"BTC"}, []string{"BTC"}, "degraded"))
	require.Error(t, ValidatePeriodDetail(detail, []string{"BTC"}, []string{"ETH"}, "degraded"), "周期级失败标的不在全集里")
}

// markerSubjects 生成 n 个形如线上标的的标识：A 股 600000.SH，加密 SYMBOL0000-USDT。
func markerSubjects(n int, ashare bool) []string {
	out := make([]string, n)
	for i := range out {
		if ashare {
			out[i] = fmt.Sprintf("%06d.SH", 600000+i)
		} else {
			out[i] = fmt.Sprintf("SYMBOL%04d-USDT", i)
		}
	}
	return out
}

// markerMemberLimit 按 proto.Size 算出标记在体积上限内最多能放的成员数：每个成员的增量相同，先量 1 个、2 个成员的体积。
func markerMemberLimit(t *testing.T, build func(members int) PeriodMarker) int {
	t.Helper()
	size := func(members int) int {
		out, err := markerToProto(build(members))
		require.NoError(t, err)
		return proto.Size(out)
	}
	one, two := size(1), size(2)
	per := two - one
	require.Positive(t, per)
	return (MaxMarkerBytes - (one - per)) / per
}

// 体积上限下的成员数（数字写进因子.md）：标的数按线上实际，A 股 5931 只、加密约 500 只。
//   - 全部失败、成员失败相同（上游整期晚到，最常见）：成员名单相减后几乎为空，成员数上限是上万个；
//   - 成员额外失败互不相同、几乎覆盖全部标的（周期级没有失败）：每个成员带一份完整名单，回到约 95 个（A 股）。
func TestMarkerMemberLimitBySubjects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		subjects int
		ashare   bool
		minSame  int
		distinct [2]int // 额外失败互不相同时成员数上限的合理区间
	}{
		{name: "ashare", subjects: 5931, ashare: true, minSame: 10000, distinct: [2]int{80, 110}},
		{name: "crypto", subjects: 500, ashare: false, minSame: 10000, distinct: [2]int{500, 900}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subjects := markerSubjects(tc.subjects, tc.ashare)
			same := func(members int) PeriodMarker {
				marker := testMarker(subjects, subjects)
				for i := 0; i < members; i++ {
					marker.Factors = append(marker.Factors, testState(fmt.Sprintf("factor%05d", i), subjects...))
				}
				return marker
			}
			distinct := func(members int) PeriodMarker {
				marker := testMarker(subjects, nil)
				for i := 0; i < members; i++ {
					// 每个成员都有一个不同的标的没失败，其余全部失败：周期级没有失败标的，成员名单无法相减。
					failed := append(append([]string(nil), subjects[:i%len(subjects)]...), subjects[i%len(subjects)+1:]...)
					marker.Factors = append(marker.Factors, testState(fmt.Sprintf("factor%05d", i), failed...))
				}
				return marker
			}
			sameLimit, distinctLimit := markerMemberLimit(t, same), markerMemberLimit(t, distinct)
			t.Logf("%s %d 只：成员失败相同时上限 %d 个成员，额外失败互不相同时上限 %d 个成员", tc.name, tc.subjects, sameLimit, distinctLimit)
			require.GreaterOrEqual(t, sameLimit, tc.minSame)
			require.GreaterOrEqual(t, distinctLimit, tc.distinct[0])
			require.LessOrEqual(t, distinctLimit, tc.distinct[1])

			_, err := markerToProto(distinct(distinctLimit))
			require.NoError(t, err, "上限内的标记能写出")
			_, err = markerToProto(distinct(distinctLimit + 1))
			require.ErrorContains(t, err, "超过上限", "超过上限时在写入前报清晰的中文错误")
		})
	}
}
