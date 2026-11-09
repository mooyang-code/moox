package input

import (
	"context"
	"reflect"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/stretchr/testify/require"
)

// applySingleAge 按单市场、只有策略级 min_age_bars 的口径执行年龄判定（现有用例的写法）。
func applySingleAge(sets *Sets, minAgeBars int, probe AgeProbe) error {
	var marketProbe MarketAgeProbe
	if probe != nil {
		marketProbe = func(ctx context.Context, _ int, ids []string) (map[string]map[string]struct{}, error) {
			got, err := probe(ctx, ids)
			if err != nil {
				return nil, err
			}
			return map[string]map[string]struct{}{"spot": got}, nil
		}
	}
	_, err := sets.ApplyAge(context.Background(), Resolved{MinAgeBars: minAgeBars}, AgeListed{"spot": nil}, marketProbe)
	return err
}

func idSet(ids ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

const ruleAgesDSL = `name: ages
market: {select: both, prefer: swap}
universe: {min_age_bars: 5}
rules:
  - id: base
    type: rank
    score: m
    select: {top: 1}
    weight: 0.25
  - id: fresh
    type: rank
    score: m
    select: {top: 1}
    weight: 0.25
    allow_empty: true
    universe: {min_age_bars: 0, max_age_bars: 20}
  - id: listed
    type: rank
    score: m
    select: {top: 1}
    weight: 0.25
    universe: {min_age_bars: 1, age_markets: all}
  - id: fresh_all
    type: rank
    score: m
    select: {top: 1}
    weight: 0.25
    allow_empty: true
    universe: {min_age_bars: 0, max_age_bars: 20, age_markets: all}
`

// 规则级年龄：max_age_bars 剔除已通过 N 根判定的老标的（原因单列）；age_markets: all 缺一个市场就不通过；
// 默认 each 的规则仍按“任一市场通过”判断，结果不受其他规则影响。
func TestApplyAgeRuleLevels(t *testing.T) {
	strategy, err := dsl.Parse([]byte(ruleAgesDSL))
	require.NoError(t, err)
	resolved := Resolved{MinAgeBars: strategy.Universe.MinAgeBars, RuleAges: ruleAges(strategy)}
	require.Equal(t, []int{1, 5, 20}, resolved.AgeLevels())
	require.Equal(t, 20, resolved.LongestAge())
	ids := []string{"OLD", "MID", "NEW", "SPOT"}
	expected := map[string][]string{"base": ids, "fresh": ids, "listed": ids, "fresh_all": ids}
	sets := Sets{Expected: expected, AgedOut: map[string][]string{}}
	// OLD 两个市场都有 20 根以上；MID 只有现货满 20 根、永续满 5 根；NEW 两个市场都只有 1 根；SPOT 是只上了现货的老币。
	listed := AgeListed{"spot": idSet("OLD", "MID", "NEW", "SPOT"), "swap": idSet("OLD", "MID", "NEW")}
	passed := map[int]map[string]map[string]struct{}{
		1:  {"spot": idSet("OLD", "MID", "NEW", "SPOT"), "swap": idSet("OLD", "MID", "NEW")},
		5:  {"spot": idSet("OLD", "MID", "SPOT"), "swap": idSet("OLD", "MID")},
		20: {"spot": idSet("OLD", "MID", "SPOT"), "swap": idSet("OLD")},
	}
	got, err := sets.ApplyAge(context.Background(), resolved, listed, func(_ context.Context, n int, instruments []string) (map[string]map[string]struct{}, error) {
		require.ElementsMatch(t, ids, instruments)
		return passed[n], nil
	})
	require.NoError(t, err)
	require.Equal(t, AgePassed(passed), got)
	require.Equal(t, []string{"NEW"}, sets.AgedOut["base"])
	require.Equal(t, []string{"OLD", "MID", "SPOT"}, sets.AgedOut["fresh"])
	require.Equal(t, []string{"OLD", "MID", "SPOT"}, sets.AgedOutMax["fresh"])
	require.Equal(t, []string{"SPOT"}, sets.AgedOut["listed"])
	require.Empty(t, sets.AgedOutMax["listed"])
	// all + max：年龄按最晚出现的市场算，MID 的永续不满 20 根仍是新标的；只上了现货的老币 SPOT 缺一个市场，min、max 都不通过、剔除。
	require.Equal(t, []string{"OLD", "SPOT"}, sets.AgedOut["fresh_all"])
	require.Equal(t, []string{"OLD", "SPOT"}, sets.AgedOutMax["fresh_all"])

	// 永续本期整体不可用（没有判定结果）：列在永续里的标的无法确认是不是新标的，max 规则都按“不是新标的”剔除；
	// all 规则缺一个判定过的市场，全部剔除；只有 min 的 each 规则与现状相同。
	down := map[int]map[string]map[string]struct{}{}
	for n, byMarket := range passed {
		down[n] = map[string]map[string]struct{}{"spot": byMarket["spot"]}
	}
	unavailable := Sets{Expected: expected, AgedOut: map[string][]string{}}
	unavailable.ApplyAgePassed(resolved, listed, down)
	require.Equal(t, []string{"NEW"}, unavailable.AgedOut["base"])
	require.Equal(t, ids, unavailable.AgedOut["fresh"])
	require.Equal(t, ids, unavailable.AgedOut["fresh_all"])
	require.Equal(t, ids, unavailable.AgedOut["listed"])

	// 没有规则级年龄字段时与现状一致：RuleAges 为空，各规则按策略级 min_age_bars、任一市场通过。
	plain, err := dsl.Parse([]byte("name: p\nuniverse: {min_age_bars: 5}\nrules:\n  - {id: base, type: rank, score: m, select: {top: 1}, weight: 1}\n"))
	require.NoError(t, err)
	require.Nil(t, ruleAges(plain))
	plainSets := Sets{Expected: map[string][]string{"base": ids}, AgedOut: map[string][]string{}}
	plainSets.ApplyAgePassed(Resolved{MinAgeBars: 5}, listed, passed)
	if !reflect.DeepEqual(plainSets.AgedOut["base"], []string{"NEW"}) {
		t.Fatalf("只有策略级 min_age_bars 时结果应与现状一致：%v", plainSets.AgedOut)
	}
}

// 规则级 universe：写了的字段逐项覆盖策略级（这里覆盖 include、沿用 exclude），没写标的范围的规则沿用策略级的默认集合；
// 只写年龄类字段的规则也沿用默认集合。
func TestBuildSetsRuleUniverseOverridesAndMerges(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, `name: rule_universe
universe:
  include: [SOL-USDT, ETH-USDT, BTC-USDT]
  exclude: [ETH-USDT]
rules:
  - id: base
    type: rank
    score: "close"
    select: {top: 1}
    weight: 0.3
  - id: narrow
    type: rank
    score: "close"
    select: {top: 1}
    weight: 0.3
    universe: {include: [BTC-USDT, ETH-USDT]}
  - id: aged
    type: rank
    score: "close"
    select: {top: 1}
    weight: 0.4
    universe: {min_age_bars: 3}
`)
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["ds_factor"], nil)
	require.NoError(t, err)
	require.Equal(t, []string{"BTC-USDT", "SOL-USDT"}, sets.Expected["base"])
	require.Equal(t, []string{"BTC-USDT"}, sets.Expected["narrow"], "include 覆盖、exclude 沿用策略级")
	require.Equal(t, []string{"BTC-USDT", "SOL-USDT"}, sets.Expected["aged"])
	resolved := Resolved{RuleAges: ruleAges(strategy)}
	require.Equal(t, AgeRule{MinBars: 3}, resolved.AgeRuleOf("aged"))
	require.Equal(t, AgeRule{}, resolved.AgeRuleOf("base"))
}

// 只绑定一个市场时写 age_markets: all 在启用时拒绝（与 each 没有区别），绑定两个市场时允许。
func TestCheckAgeMarketsRejectsSingleMarket(t *testing.T) {
	ages := map[string]AgeRule{"r": {MinBars: 1, AllMarkets: true}}
	err := Resolved{Market: dsl.SingleMarket("spot"), RuleAges: ages}.CheckAgeMarkets()
	require.Error(t, err)
	require.Contains(t, err.Error(), "只绑定了 spot 一个市场")
	require.NoError(t, Resolved{Market: dsl.Market{Select: "both", Prefer: "swap"}, RuleAges: ages}.CheckAgeMarkets())
	require.NoError(t, Resolved{Market: dsl.SingleMarket("spot"), RuleAges: map[string]AgeRule{"r": {MaxBars: 3}}}.CheckAgeMarkets())
}
