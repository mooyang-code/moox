package input

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func shanghai(t *testing.T) *time.Location {
	t.Helper()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	return location
}

func membershipOf(client *fakeClient) Membership {
	return func(ctx context.Context, tagID string) ([]string, error) {
		return client.ListTagMembers(ctx, "space", tagID)
	}
}

// S1（装配部分）：U = 事件名单 ∩ 活跃标的；universe 排除的 BTC 仍出现在显式 pool 规则的 E(r) 中；
// 黑名单与标签排除不计入任何集合。
func TestBuildSetsAppliesUniverseAndExplicitPools(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, exampleDSL)
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["ds_factor"], []string{"BTC-USDT", "ETH-USDT", "SOL-USDT", "USDC-USDT", "OLD-USDT", "GHOST-USDT"})
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
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["ds_factor"], nil)
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
	if _, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["ds_factor"], nil); err == nil || !strings.Contains(err.Error(), "storage down") {
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
	sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["ds_factor"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sets.Expected["r"], []string{"SOL-USDT"}) {
		t.Fatalf("include 单独使用应只保留白名单再减黑名单：%v", sets.Expected["r"])
	}
}

// S12：探针查询失败返回基础设施错误，不把标的记为年龄不足；成功时按有无行划分 G(r)。
func TestBuildSetsAgeProbe(t *testing.T) {
	client := newFakeClient("spot")
	strategy := parseStrategy(t, strings.Replace(exampleDSL, "universe:\n", "universe:\n  min_age_bars: 24\n", 1))
	build := func() Sets {
		t.Helper()
		sets, err := BuildSets(context.Background(), membershipOf(client), strategy, client.subjects["ds_factor"], nil)
		if err != nil {
			t.Fatal(err)
		}
		return sets
	}
	failing := func(context.Context, []string) (map[string]struct{}, error) { return nil, errors.New("probe timeout") }
	failed := build()
	if err := failed.ApplyAge(context.Background(), strategy.Universe.MinAgeBars, failing); err == nil || !strings.Contains(err.Error(), "probe timeout") {
		t.Fatalf("探针失败应返回错误：%v", err)
	}
	var probed []string
	probe := func(_ context.Context, instruments []string) (map[string]struct{}, error) {
		probed = instruments
		return map[string]struct{}{"BTC-USDT": {}, "ETH-USDT": {}}, nil
	}
	sets := build()
	if err := sets.ApplyAge(context.Background(), strategy.Universe.MinAgeBars, probe); err != nil {
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
	if err := missingProbe.ApplyAge(context.Background(), strategy.Universe.MinAgeBars, nil); err == nil {
		t.Fatal("设置了 min_age_bars 却没有探针应报错")
	}
}

func TestNewAgeProbeQueriesToleranceWindow(t *testing.T) {
	client := newFakeClient("spot")
	client.rows = func(query Query) ([]Row, uint64, error) {
		return []Row{{SubjectID: "BTC-USDT", DataTime: query.Start, Values: map[string]float64{"close": 1}}}, 7, nil
	}
	resolved := Resolved{ViewID: "view_factor_1h", Bar: "1h", Calendar: DefaultCalendar, MinAgeBars: 24}
	subjects := map[string]Subject{"BTC-USDT": subject("BTC-USDT", true), "ETH-USDT": subject("ETH-USDT", true)}
	probe := NewAgeProbe(client, "space", resolved, client.views["view_factor_1h"], subjects, barStart, 0)
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
	stockStart := time.Date(2026, 10, 11, 16, 0, 0, 0, time.UTC)
	from, to, err := AgeWindow("cn_stock", "1d", stockStart, 2)
	if err != nil {
		t.Fatal(err)
	}
	if from.In(shanghai(t)).Format("2006-01-02") != "2026-09-30" || to.Add(-time.Nanosecond).In(shanghai(t)).Format("2006-01-02") != "2026-10-09" {
		t.Fatalf("A 股探针窗口不符：%s ~ %s", from.In(shanghai(t)), to.In(shanghai(t)))
	}
}
