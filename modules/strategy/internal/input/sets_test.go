package input

import (
	"context"
	"errors"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/period"
	"github.com/mooyang-code/moox/modules/strategy/internal/upstream"
)

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func shAt(t *testing.T, month time.Month, day, hour, minute int) time.Time {
	t.Helper()
	return time.Date(2026, month, day, hour, minute, 0, 0, shanghai(t)).UTC()
}

func membershipOf(client *fakeClient) Membership {
	return func(ctx context.Context, tagID string) ([]string, error) {
		members, err := client.ListTagMembers(ctx, "space", tagID)
		return upstream.MemberIDs(members), err
	}
}

// S1（装配部分）：U = 事件名单 ∩ 活跃标的；universe 排除的 BTC 仍出现在显式 pool 规则的 E(r) 中；
// 黑名单与标签排除不计入任何集合。
func TestBuildSetsAppliesUniverseAndExplicitPools(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, exampleDSL)
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["dataset_factor_1h"], []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "USDC-USDT", "OLD-USDT", "GHOST-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sets.Universe, []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "USDC-USDT"}) {
		t.Fatalf("U 不符：%v", sets.Universe)
	}
	if !reflect.DeepEqual(sets.Expected["long_momentum"], []string{"ETH-USDT", "SOL-USDT"}) {
		t.Fatalf("rank 规则的 E(r) 应排除稳定币与 BTC：%v", sets.Expected["long_momentum"])
	}
	if !reflect.DeepEqual(sets.Expected["btc_trend"], []string{"BTC-USDT"}) {
		t.Fatalf("显式 pool 的 E(r) 不受 universe 排除影响：%v", sets.Expected["btc_trend"])
	}
	if len(sets.AgedOut["long_momentum"]) != 0 {
		t.Fatalf("没有 min_age_bars 时不应有年龄剔除：%v", sets.AgedOut)
	}
	if !reflect.DeepEqual(sets.Instruments(), []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}) {
		t.Fatalf("∪E(r) 不符：%v", sets.Instruments())
	}
}

func TestBuildSetsUniverseTagsIncludeAndPoolTags(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, `name: tags
universe:
  tags: [majors]
  include: [SOL-USDT]
  exclude: [ETH-USDT]
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.5}
  - id: p
    type: rank
    pool: {tags: [stablecoins]}
    score: "close"
    select: {top: 1}
    weight: {total: 0.5}
`)
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["dataset_factor_1h"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets.Notes) != 1 {
		t.Fatalf("事件没有名单时应记录说明：%v", sets.Notes)
	}
	if !reflect.DeepEqual(sets.Expected["r"], []string{"BTC-USDT", "SOL-USDT"}) {
		t.Fatalf("标签 + 白名单 − 黑名单不符：%v", sets.Expected["r"])
	}
	if !reflect.DeepEqual(sets.Expected["p"], []string{"USDC-USDT"}) {
		t.Fatalf("按标签的 pool 不符：%v", sets.Expected["p"])
	}
	client.errors["tags"] = errors.New("storage down")
	if _, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["dataset_factor_1h"], nil); err == nil || !strings.Contains(err.Error(), "storage down") {
		t.Fatalf("标签查询失败应返回错误：%v", err)
	}
}

// 只写 include 时它就是白名单，不能退化为全部 U。
func TestBuildSetsIncludeAloneIsWhitelist(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, `name: include_only
universe:
  include: [SOL-USDT, ETH-USDT]
  exclude: [ETH-USDT]
rules:
  - id: r
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.5}
`)
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["dataset_factor_1h"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sets.Expected["r"], []string{"SOL-USDT"}) {
		t.Fatalf("include 单独使用应只保留白名单再减黑名单：%v", sets.Expected["r"])
	}
}

// 3F-1 对照测试：规则级 pool 是规则级 universe 的简写（替代策略级标签与黑白名单，保留年龄设置），
// 同一策略分别用 pool 写法和等价的规则级 universe 写法，BuildSets 与每条规则的有效 RuleUniverse 必须完全一致。
func TestBuildSetsPoolEquivalentToRuleUniverse(t *testing.T) {
	cases := []struct {
		name         string
		poolDSL      string
		universeDSL  string
		wantExpected map[string][]string
	}{
		{
			name: "fixed_list",
			poolDSL: `name: fixed_list
universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
rules:
  - id: fixed
    type: rank
    pool: [BTC-USDT, USDC-USDT]
    score: "close"
    select: {top: 1}
    weight: {total: 1.0}
`,
			universeDSL: `name: fixed_list
universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
rules:
  - id: fixed
    type: rank
    universe:
      tags: []
      include: [BTC-USDT, USDC-USDT]
      exclude_tags: []
      exclude: []
    score: "close"
    select: {top: 1}
    weight: {total: 1.0}
`,
			wantExpected: map[string][]string{
				"fixed": {"BTC-USDT", "USDC-USDT"},
			},
		},
		{
			name: "tag_pool",
			poolDSL: `name: tag_pool
universe:
  tags: [majors]
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
rules:
  - id: by_tag
    type: rank
    pool: {tags: [stablecoins]}
    score: "close"
    select: {top: 1}
    weight: {total: 1.0}
`,
			universeDSL: `name: tag_pool
universe:
  tags: [majors]
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
rules:
  - id: by_tag
    type: rank
    universe:
      tags: [stablecoins]
      include: []
      exclude_tags: []
      exclude: []
    score: "close"
    select: {top: 1}
    weight: {total: 1.0}
`,
			wantExpected: map[string][]string{
				"by_tag": {"USDC-USDT"},
			},
		},
		{
			name: "multi_rule_distinct_pools",
			poolDSL: `name: multi_rule_distinct_pools
universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
rules:
  - id: default_rule
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
  - id: fixed_rule
    type: rank
    pool: [BTC-USDT, ETH-USDT]
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
  - id: stable_rule
    type: rank
    pool: {tags: [stablecoins]}
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
  - id: alt_rule
    type: rank
    pool: {tags: [alts]}
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
`,
			universeDSL: `name: multi_rule_distinct_pools
universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
rules:
  - id: default_rule
    type: rank
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
  - id: fixed_rule
    type: rank
    universe:
      tags: []
      include: [BTC-USDT, ETH-USDT]
      exclude_tags: []
      exclude: []
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
  - id: stable_rule
    type: rank
    universe:
      tags: [stablecoins]
      include: []
      exclude_tags: []
      exclude: []
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
  - id: alt_rule
    type: rank
    universe:
      tags: [alts]
      include: []
      exclude_tags: []
      exclude: []
    score: "close"
    select: {top: 1}
    weight: {total: 0.25}
`,
			wantExpected: map[string][]string{
				"default_rule": {"ETH-USDT", "SOL-USDT"},
				"fixed_rule":   {"BTC-USDT", "ETH-USDT"},
				"stable_rule":  {"USDC-USDT"},
				"alt_rule":     {"ETH-USDT", "SOL-USDT"},
			},
		},
		{
			name: "pool_with_age_only_rule_universe",
			poolDSL: `name: pool_with_age
universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
  min_age_bars: 24
rules:
  - id: fixed_with_age
    type: rank
    allow_empty: true
    pool: [BTC-USDT, SOL-USDT]
    universe:
      min_age_bars: 48
      max_age_bars: 720
      age_markets: all
    score: "close"
    select: {top: 1}
    weight: {total: 0.4}
  - id: tag_with_age
    type: rank
    pool: {tags: [majors]}
    universe:
      min_age_bars: 12
    score: "close"
    select: {top: 1}
    weight: {total: 0.3}
  - id: age_only_rule
    type: rank
    universe:
      min_age_bars: 72
    score: "close"
    select: {top: 1}
    weight: {total: 0.3}
`,
			universeDSL: `name: pool_with_age
universe:
  exclude_tags: [stablecoins]
  exclude: [BTC-USDT]
  min_age_bars: 24
rules:
  - id: fixed_with_age
    type: rank
    allow_empty: true
    universe:
      tags: []
      include: [BTC-USDT, SOL-USDT]
      exclude_tags: []
      exclude: []
      min_age_bars: 48
      max_age_bars: 720
      age_markets: all
    score: "close"
    select: {top: 1}
    weight: {total: 0.4}
  - id: tag_with_age
    type: rank
    universe:
      tags: [majors]
      include: []
      exclude_tags: []
      exclude: []
      min_age_bars: 12
    score: "close"
    select: {top: 1}
    weight: {total: 0.3}
  - id: age_only_rule
    type: rank
    universe:
      min_age_bars: 72
    score: "close"
    select: {top: 1}
    weight: {total: 0.3}
`,
			wantExpected: map[string][]string{
				"fixed_with_age": {"BTC-USDT", "SOL-USDT"},
				"tag_with_age":   {"BTC-USDT", "ETH-USDT"},
				"age_only_rule":  {"ETH-USDT", "SOL-USDT"},
			},
		},
	}

	eventUniverse := []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "USDC-USDT", "OLD-USDT", "GHOST-USDT"}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := newFakeClient("spot")
			client.tags["alts"] = []string{"ETH-USDT", "SOL-USDT"}
			poolStrategy := parseStrategy(t, tc.poolDSL)
			universeStrategy := parseStrategy(t, tc.universeDSL)

			poolSets, err := BuildSets(context.Background(), membershipOf(client), poolStrategy, client.subjects["dataset_factor_1h"], eventUniverse)
			if err != nil {
				t.Fatalf("pool 写法 BuildSets 失败：%v", err)
			}
			universeSets, err := BuildSets(context.Background(), membershipOf(client), universeStrategy, client.subjects["dataset_factor_1h"], eventUniverse)
			if err != nil {
				t.Fatalf("universe 写法 BuildSets 失败：%v", err)
			}
			if !reflect.DeepEqual(poolSets, universeSets) {
				t.Fatalf("BuildSets 结果不一致：\npool:     %+v\nuniverse: %+v", poolSets, universeSets)
			}
			if !reflect.DeepEqual(poolSets.Expected, tc.wantExpected) {
				t.Fatalf("Expected 不符：got=%v want=%v", poolSets.Expected, tc.wantExpected)
			}
			if poolStrategy.HasRuleAges() != universeStrategy.HasRuleAges() || poolStrategy.LongestAgeBars() != universeStrategy.LongestAgeBars() {
				t.Fatalf("年龄汇总不一致：pool=(%v,%d) universe=(%v,%d)",
					poolStrategy.HasRuleAges(), poolStrategy.LongestAgeBars(), universeStrategy.HasRuleAges(), universeStrategy.LongestAgeBars())
			}
			normalizeUniverse := func(u dsl.Universe) dsl.Universe {
				if len(u.Tags) == 0 {
					u.Tags = nil
				}
				if len(u.ExcludeTags) == 0 {
					u.ExcludeTags = nil
				}
				if len(u.Include) == 0 {
					u.Include = nil
				}
				if len(u.Exclude) == 0 {
					u.Exclude = nil
				}
				return u
			}
			for i := range poolStrategy.Rules {
				gotU := normalizeUniverse(poolStrategy.RuleUniverse(poolStrategy.Rules[i]))
				wantU := normalizeUniverse(universeStrategy.RuleUniverse(universeStrategy.Rules[i]))
				if !reflect.DeepEqual(gotU, wantU) {
					t.Fatalf("规则 %s 合并后的 RuleUniverse 不一致：pool=%+v universe=%+v", poolStrategy.Rules[i].ID, gotU, wantU)
				}
			}
		})
	}
}

// S12：探针查询失败返回基础设施错误，不把标的记为年龄不足；成功时按有无行划分 G(r)。
func TestBuildSetsAgeProbe(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, strings.Replace(exampleDSL, "universe:\n", "universe:\n  min_age_bars: 24\n", 1))
	build := func() Sets {
		t.Helper()
		sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["dataset_factor_1h"], nil)
		if err != nil {
			t.Fatal(err)
		}
		return sets
	}
	failing := func(context.Context, []string) (map[string]struct{}, error) { return nil, errors.New("probe timeout") }
	failed := build()
	if err := applySingleAge(&failed, strategy.Universe.MinAgeBars, failing); err == nil || !strings.Contains(err.Error(), "probe timeout") {
		t.Fatalf("探针失败应返回错误：%v", err)
	}
	var probed []string
	probe := func(_ context.Context, instruments []string) (map[string]struct{}, error) {
		probed = instruments
		return map[string]struct{}{"BTC-USDT": {}, "ETH-USDT": {}}, nil
	}
	sets := build()
	if err := applySingleAge(&sets, strategy.Universe.MinAgeBars, probe); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(probed, []string{"BTC-USDT", "ETH-USDT", "SOL-USDT"}) {
		t.Fatalf("探针应覆盖 ∪E(r)：%v", probed)
	}
	if !reflect.DeepEqual(sets.AgedOut["long_momentum"], []string{"SOL-USDT"}) || len(sets.AgedOut["btc_trend"]) != 0 {
		t.Fatalf("年龄剔除不符：%v", sets.AgedOut)
	}
	if !reflect.DeepEqual(sets.Instruments(), []string{"BTC-USDT", "ETH-USDT"}) {
		t.Fatalf("年龄剔除后的读取范围不符：%v", sets.Instruments())
	}
	missingProbe := build()
	if err := applySingleAge(&missingProbe, strategy.Universe.MinAgeBars, nil); err == nil {
		t.Fatal("设置了 min_age_bars 却没有探针应报错")
	}
}

func TestNewAgeProbeQueriesToleranceWindow(t *testing.T) {
	client := newFakeClient("spot")
	client.rows = func(query upstream.Query) ([]upstream.Row, uint64, error) {
		return []upstream.Row{{SubjectID: "BTC-USDT", DataTime: query.Start, Values: map[string]float64{"close": 1}}}, 7, nil
	}
	resolved := Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]TaskBinding{"spot": {DatasetID: "dataset_factor_1h"}}, Bar: "1h", Calendar: period.DefaultCalendar, MinAgeBars: 24}
	subjects := map[string]upstream.Subject{"BTC-USDT": subject("BTC-USDT", true), "ETH-USDT": subject("ETH-USDT", true)}
	probe := NewAgeProbe(client, "space", resolved, resolved.MinAgeBars, client.views["dataset_factor_1h"], "spot", subjects, barStart, 0, nil)
	satisfied, err := probe(context.Background(), []string{"BTC-USDT", "ETH-USDT"})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := satisfied["BTC-USDT"]; !ok || len(satisfied) != 1 {
		t.Fatalf("有行的标的应满足年龄：%v", satisfied)
	}
	query := client.queries[0]
	target := barStart.Add(-23 * time.Hour)
	if !query.Start.Equal(target.Add(-2*time.Hour)) || !query.End.Equal(target.Add(time.Nanosecond)) || len(query.Columns) != 1 || query.Columns[0] != "close" || len(query.Subjects) != 2 {
		t.Fatalf("探针窗口不符：%+v", query)
	}
	// A 股日历按交易日回退：2026-10-12 往前三根依次是 10-09、10-08 和国庆节前的 09-30。
	stockStart := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	from, to, err := AgeWindow("cn_stock", "1d", stockStart, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !from.Equal(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)) || !to.Add(-time.Nanosecond).Equal(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("A 股探针窗口不符：%s ~ %s", from, to)
	}
}

// 探针窗口内候选全部没有行时，先确认整个数据集在窗口内是否有数据：有数据说明候选确实是新上市，照常剔除；
// 一行都没有说明是历史缺口，整期 history_insufficient，不能把全部标的当作新上市。
func TestAgeProbeDistinguishesNewListingsFromHistoryGaps(t *testing.T) {
	resolved := Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]TaskBinding{"spot": {DatasetID: "dataset_factor_1h"}}, Bar: "1h", Calendar: period.DefaultCalendar, MinAgeBars: 24}
	subjects := map[string]upstream.Subject{"BTC-USDT": subject("BTC-USDT", true), "NEW-USDT": subject("NEW-USDT", true)}
	client := newFakeClient("spot")
	client.rows = func(query upstream.Query) ([]upstream.Row, uint64, error) {
		for _, selected := range query.Subjects {
			if selected.SubjectID == "BTC-USDT" {
				return []upstream.Row{{SubjectID: "BTC-USDT", DataTime: query.Start, Values: map[string]float64{"close": 1}}}, 7, nil
			}
		}
		return nil, 7, nil
	}
	probe := NewAgeProbe(client, "space", resolved, resolved.MinAgeBars, client.views["dataset_factor_1h"], "spot", subjects, barStart, 7, nil)
	satisfied, err := probe(context.Background(), []string{"NEW-USDT"})
	if err != nil || len(satisfied) != 0 {
		t.Fatalf("数据集有数据时新标的应正常剔除：%v err=%v", satisfied, err)
	}
	if last := client.queries[len(client.queries)-1]; last.Limit != 1 || len(last.Subjects) != 2 || last.ExpectedRevision != 7 {
		t.Fatalf("确认查询应覆盖全部活跃标的、只读一行并固定修订号：%+v", last)
	}

	gap := newFakeClient("spot")
	gap.rows = func(upstream.Query) ([]upstream.Row, uint64, error) { return nil, 7, nil }
	probe = NewAgeProbe(gap, "space", resolved, resolved.MinAgeBars, gap.views["dataset_factor_1h"], "spot", subjects, barStart, 7, nil)
	_, err = probe(context.Background(), []string{"BTC-USDT", "NEW-USDT"})
	var skip *upstream.SkipError
	if !errors.As(err, &skip) || skip.Reason != upstream.SkipHistoryInsufficient || !strings.Contains(skip.Detail, "历史缺口") {
		t.Fatalf("窗口内整个数据集都没有数据应记 history_insufficient：%v", err)
	}
}
