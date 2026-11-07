package readiness

import (
	"reflect"
	"testing"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

const (
	spotTag = "venue:binance|market:spot|source:spot_http"
	swapTag = "venue:binance|market:swap|source:swap_http"
)

func refs(pairs ...string) []*factorpb.SeriesRef {
	out := make([]*factorpb.SeriesRef, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, &factorpb.SeriesRef{SubjectId: pairs[i], SeriesTag: pairs[i+1]})
	}
	return out
}

func unifiedEvent(shared []*factorpb.SeriesRef, states ...*factorpb.FactorPeriodState) *storagepb.ViewDataReady {
	raw, err := proto.Marshal(&factorpb.PeriodDetail{Factors: states, SharedFailedSeries: shared})
	if err != nil {
		panic(err)
	}
	return &storagepb.ViewDataReady{ViewId: "view_1", Status: "degraded", DetailType: PeriodDetailType, Detail: raw}
}

// 统一数据集：失败序列先与周期级共享部分合并，再按 series_tag 归到市场；按标的折叠的 failed_subjects 不参与，
// 永续上失败的币现货照常。
func TestForMarketSplitsFailedSeriesByMarket(t *testing.T) {
	member := state("ma", "degraded", "h1", "BTC-USDT", "ETH-USDT")
	member.FailedSeries = refs("ETH-USDT", swapTag)
	shared := refs("BTC-USDT", swapTag, "SOL-USDT", spotTag)
	result := Check(Binding{Factors: map[string]string{"ma": "h1|fixed"}}, nil, unifiedEvent(shared, member))
	if result.Reason != "" {
		t.Fatalf("部分序列失败不应整期跳过：%+v", result)
	}
	spot, swap := result.ForMarket("spot"), result.ForMarket("swap")
	if !reflect.DeepEqual(spot.FailedByFactor, map[string][]string{"ma": {"SOL-USDT"}}) {
		t.Fatalf("现货只失败了 SOL：%+v", spot)
	}
	if !reflect.DeepEqual(swap.FailedByFactor, map[string][]string{"ma": {"BTC-USDT", "ETH-USDT"}}) {
		t.Fatalf("永续失败了 BTC、ETH：%+v", swap)
	}
	if spot.Reason != "" || swap.Reason != "" {
		t.Fatalf("失败序列不让市场不可用：%q %q", spot.Reason, swap.Reason)
	}
}

// 统一数据集某一期期望快照里只剩一个 series_tag：Storage 不生成序列名单，因子只给出按标的的失败名单。
// 事件里确定不了是哪个市场，按标的的名单保守地带到每个市场，失败的标的不会被当成可用。
func TestForMarketFallsBackToSubjectsWithoutSeriesList(t *testing.T) {
	member := state("ma", "degraded", "h1", "BTC-USDT")
	result := Check(Binding{Factors: map[string]string{"ma": "h1|fixed"}}, nil, unifiedEvent(nil, member, state("rank", "complete", "h2")))
	if result.Reason != "" || len(result.FailedSeries) != 0 {
		t.Fatalf("没有序列名单：%+v", result)
	}
	for _, market := range []string{"spot", "swap"} {
		got := result.ForMarket(market)
		if !reflect.DeepEqual(got.FailedByFactor, map[string][]string{"ma": {"BTC-USDT"}}) {
			t.Fatalf("%s 市场应保守地把 BTC 记为失败：%+v", market, got)
		}
		if _, ok := got.FailedByFactor["rank"]; ok {
			t.Fatalf("没有失败的因子不受影响：%+v", got)
		}
	}
	// 有序列名单时仍只按序列归市场，不回退。
	member.FailedSeries = refs("BTC-USDT", swapTag)
	withSeries := Check(Binding{Factors: map[string]string{"ma": "h1|fixed"}}, nil, unifiedEvent(nil, member))
	if spot := withSeries.ForMarket("spot"); len(spot.FailedByFactor) != 0 {
		t.Fatalf("有序列名单时现货照常：%+v", spot)
	}
}

// 某个因子在永续的 series_tag 上被跳过（部分 tag 跳过，成员 degraded）：永续本期不可用（数据类原因），现货照常；
// 全部 tag 都跳过（成员 skipped）时每个市场都是整期原因。
func TestForMarketSkippedSeriesTags(t *testing.T) {
	binding := Binding{Factors: map[string]string{"ma": "h1|fixed", "rank": "h2|fixed"}}
	partial := state("rank", "degraded", "h2")
	partial.SkippedSeriesTags = []string{swapTag}
	result := Check(binding, nil, unifiedEvent(nil, state("ma", "complete", "h1"), partial))
	if result.Reason != "" {
		t.Fatalf("部分 tag 跳过不应整期跳过：%+v", result)
	}
	if spot := result.ForMarket("spot"); spot.Reason != "" {
		t.Fatalf("现货照常：%+v", spot)
	}
	swap := result.ForMarket("swap")
	if swap.Reason != ReasonFactorSkipped || !IsDataReason(swap.Reason) || swap.Detail == "" {
		t.Fatalf("永续应不可用，且是数据类原因：%+v", swap)
	}

	all := Check(binding, nil, unifiedEvent(nil, state("ma", "complete", "h1"), state("rank", "skipped", "h2")))
	if all.Reason != ReasonFactorSkipped || all.ForMarket("spot").Reason != ReasonFactorSkipped || all.ForMarket("swap").Reason != ReasonFactorSkipped {
		t.Fatalf("全部 tag 跳过：每个市场都整期跳过：%+v", all)
	}
	// 配置、契约类原因原样带到每个市场。
	changed := Check(Binding{Factors: map[string]string{"ma": "h9|fixed"}}, nil, unifiedEvent(nil, state("ma", "complete", "h1")))
	if changed.ForMarket("swap").Reason != ReasonFactorChanged {
		t.Fatalf("契约变化应原样带到市场：%+v", changed.ForMarket("swap"))
	}
}

// 单市场 View 不受影响：没有序列字段，ForMarket 之外的字段口径不变。
func TestCheckWithoutSeriesFieldsKeepsSubjectFields(t *testing.T) {
	result := Check(Binding{Factors: map[string]string{"ma": "h1|fixed"}}, nil, factorEvent(state("ma", "degraded", "h1", "ETH-USDT")))
	if len(result.FailedSeries) != 0 || len(result.SkippedTags) != 0 || !reflect.DeepEqual(result.FailedByFactor, map[string][]string{"ma": {"ETH-USDT"}}) {
		t.Fatalf("单市场 View 只有按标的的失败：%+v", result)
	}
}
