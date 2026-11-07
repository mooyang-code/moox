package input

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
)

const dualDSL = `name: dual
market: {select: both, prefer: swap}
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 1}
    weight: 1
portfolio: {max_missing: 1}
`

// newDualClient 在现货 View 之外再加一个永续 View（任务 ftask_swap1h），成员与列和现货一致。
func newDualClient() *fakeClient {
	client := newFakeClient("spot")
	swap := client.views["view_factor_1h"]
	swap.ViewID, swap.DatasetID = "view_factor_swap1h", "ds_factor_swap"
	client.views["view_factor_swap1h"] = swap
	client.datasets["ds_factor_swap"] = DatasetInfo{DatasetID: "ds_factor_swap", Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{"source_dataset_id": "ds_kline_swap"}}
	client.datasets["ds_kline_swap"] = DatasetInfo{DatasetID: "ds_kline_swap", Status: "active", Frequency: "1h", Retention: "720h", Attributes: map[string]string{}, SubjectTags: []string{"binance_swap"}}
	client.subjects["ds_factor_swap"] = []Subject{subject("BTC-USDT", true), subject("ETH-USDT", true), subject("DOGE-USDT", true)}
	return client
}

func TestResolveBindsTwoTasks(t *testing.T) {
	client := newDualClient()
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, dualDSL))
	if err != nil {
		t.Fatalf("解析失败：%v", err)
	}
	if resolved.Market.Select != "both" || resolved.Market.Prefer != "swap" || len(resolved.Tasks) != 2 || program == nil {
		t.Fatalf("解析结果不符：%+v", resolved)
	}
	if resolved.Tasks["spot"].ViewID != "view_factor_1h" || resolved.Tasks["swap"].ViewID != "view_factor_swap1h" || resolved.Tasks["swap"].SourceDatasetID != "ds_kline_swap" {
		t.Fatalf("任务绑定不符：%+v", resolved.Tasks)
	}
	if got := strings.Join(resolved.ViewIDs(), ","); got != "view_factor_1h,view_factor_swap1h" {
		t.Fatalf("View 顺序现货在前：%s", got)
	}
	if err := CheckResolvedMarkets(resolved, map[string]string{"spot": "ftask_1h", "swap": "ftask_swap1h"}); err != nil {
		t.Fatal(err)
	}
	if err := CheckResolvedMarkets(resolved, map[string]string{"spot": "ftask_swap1h", "swap": "ftask_1h"}); err == nil || !strings.Contains(err.Error(), "源数据集是") {
		t.Fatalf("登记的市场与真实市场不符应被拒绝：%v", err)
	}
	raw, _ := json.Marshal(resolved)
	parsed, err := ParseResolved(raw)
	if err != nil || len(parsed.Tasks) != 2 {
		t.Fatalf("resolved_json 往返失败：%+v err=%v", parsed, err)
	}
}

func TestResolveRejectsInconsistentTwoTaskBindings(t *testing.T) {
	cases := map[string]struct {
		prepare func(*fakeClient)
		dsl     string
		tasks   []string
		want    string
	}{
		"两个任务不写 market": {func(*fakeClient) {}, strings.Replace(dualDSL, "market: {select: both, prefer: swap}\n", "", 1), []string{"ftask_1h", "ftask_swap1h"}, "必须写 market"},
		"只绑一个任务却要两个市场":  {func(*fakeClient) {}, dualDSL, []string{"ftask_1h"}, "需要绑定 spot、swap"},
		"两个任务同一市场":      {func(*fakeClient) {}, dualDSL, []string{"ftask_1h", "ftask_1h"}, "都是 spot"},
		"周期不一致": {func(c *fakeClient) {
			v := c.views["view_factor_swap1h"]
			v.Frequency = "4h"
			c.views["view_factor_swap1h"] = v
		}, dualDSL, []string{"ftask_1h", "ftask_swap1h"}, "周期不一致"},
		"缺列": {func(c *fakeClient) {
			v := c.views["view_factor_swap1h"]
			v.Columns = []ViewColumn{plainColumn("close")}
			c.views["view_factor_swap1h"] = v
		}, dualDSL, []string{"ftask_1h", "ftask_swap1h"}, "不存在于 View view_factor_swap1h"},
		"成员定义不一致": {func(c *fakeClient) {
			v := c.views["view_factor_swap1h"]
			cols := append([]ViewColumn(nil), v.Columns...)
			for i := range cols {
				if cols[i].Name == "ma_20" {
					cols[i] = memberColumn("ma_20", "ma", "ma_other", "fixed")
				}
			}
			v.Columns = cols
			c.views["view_factor_swap1h"] = v
			c.factors["ma_other"] = FactorInfo{FactorID: "ma_other", DefinitionHash: "sha256:other"}
		}, dualDSL, []string{"ftask_1h", "ftask_swap1h"}, "因子定义不一致"},
		"纯现货做空": {func(*fakeClient) {}, "name: s\nrules:\n  - {id: r, type: rank, side: short, score: ma_20, select: {bottom: 1}, weight: 1}\n", []string{"ftask_1h"}, "不允许规则 r 做空"},
	}
	for name, tc := range cases {
		client := newDualClient()
		tc.prepare(client)
		if _, _, err := Resolve(context.Background(), client, "space", tc.tasks, parseStrategy(t, tc.dsl)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s：期望错误含 %q，实际 %v", name, tc.want, err)
		}
	}
}

func TestLoadBarReadsEveryMarketAndMarksUnavailable(t *testing.T) {
	client := newDualClient()
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, dualDSL))
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		if query.ViewID == "view_factor_1h" {
			return rowsAt(barStart, map[string]map[string]float64{"BTC-USDT": {"ma_20": 1}, "ETH-USDT": {"ma_20": 2}}), 1, nil
		}
		return rowsAt(barStart, map[string]map[string]float64{"ETH-USDT": {"ma_20": 3}, "DOGE-USDT": {"ma_20": 4}}), 1, nil
	}
	universes := map[string][]string{"spot": {"BTC-USDT", "ETH-USDT"}, "swap": {"ETH-USDT", "DOGE-USDT"}}
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, EventUniverse: universes, Readiness: map[string]readiness.Result{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Frame.Markets["spot"].Rows) != 2 || len(loaded.Frame.Markets["swap"].Rows) != 2 {
		t.Fatalf("两个市场各读一份行：%+v", loaded.Frame.Markets)
	}
	if got := strings.Join(loaded.Frame.Universe, ","); got != "BTC-USDT,DOGE-USDT,ETH-USDT" {
		t.Fatalf("基础集合是各市场事件名单的并集：%s", got)
	}
	// 目标：ETH 两边都有，prefer 永续，永续那一行 ma_20=3；DOGE 只在永续，ma_20=4 最大。
	decision, err := engine.Evaluate(program, loaded.Frame, engine.State{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Status != engine.StatusOK || len(decision.Targets) != 1 || decision.Targets[0].InstrumentID != "DOGE-USDT" || decision.Targets[0].Market != "swap" {
		t.Fatalf("目标不符：%+v %v", decision.Targets, decision.Summary.Notes)
	}

	// 永续本期整体不可用：不读永续的行，永续的标的记“市场不可用”，现货照常。
	client.queries = nil
	loaded, err = Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart, EventUniverse: universes, Unavailable: map[string]string{"swap": "因子被跳过"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.queries) != 1 || client.queries[0].ViewID != "view_factor_1h" {
		t.Fatalf("不可用的市场不读行：%+v", client.queries)
	}
	swap := loaded.Frame.Markets["swap"]
	if len(swap.Rows) != 0 || swap.Failed["ma_20"]["DOGE-USDT"] != engine.FailedMarket || swap.Failed["ma_20"]["ETH-USDT"] != engine.FailedMarket {
		t.Fatalf("不可用的市场应记市场不可用：%+v", swap)
	}
}

func TestLoadBarSelectSpotKeepsSwapOnlyForAvailability(t *testing.T) {
	client := newDualClient()
	dsl := strings.Replace(dualDSL, "{select: both, prefer: swap}", "{select: spot, prefer: swap}", 1)
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h", "ftask_swap1h"}, parseStrategy(t, dsl))
	if err != nil {
		t.Fatal(err)
	}
	client.rows = func(query Query) ([]Row, uint64, error) {
		return rowsAt(barStart, map[string]map[string]float64{"ETH-USDT": {"ma_20": 2}, "DOGE-USDT": {"ma_20": 9}}), 1, nil
	}
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: barStart})
	if err != nil {
		t.Fatal(err)
	}
	// 永续独有的 DOGE 不进入基础集合；ETH 现货选中后在永续下单。
	if strings.Join(loaded.Frame.Universe, ",") != "BTC-USDT,ETH-USDT,SOL-USDT,USDC-USDT" {
		t.Fatalf("基础集合只由现货构成：%v", loaded.Frame.Universe)
	}
}

// 启用时先核对“市场 → 任务”再关联：登记错了市场的任务不能被关联占住。
func TestResolveInstanceChecksMarketsBeforeBinding(t *testing.T) {
	client := newDualClient()
	tasks := &tasksFake{}
	service := Service{Client: client, Sets: tasks}
	// ftask_1h 是现货任务，却登记成永续。
	_, _, err := service.ResolveInstance(context.Background(), "i1", "space", map[string]string{"swap": "ftask_1h"}, parseStrategy(t, strings.Replace(dualDSL, "market: {select: both, prefer: swap}\n", "market: {select: swap}\n", 1)))
	if err == nil || !strings.Contains(err.Error(), "登记为 swap 市场，但它的源数据集是 spot 市场") {
		t.Fatalf("登记错市场应被拒绝：%v", err)
	}
	if len(tasks.binds) != 0 {
		t.Fatalf("核对失败时不能关联任务：%v", tasks.binds)
	}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", map[string]string{"spot": "ftask_1h", "swap": "ftask_swap1h"}, parseStrategy(t, dualDSL)); err != nil {
		t.Fatalf("市场对得上应通过：%v", err)
	}
	if len(tasks.binds) != 2 {
		t.Fatalf("两个任务都应关联：%v", tasks.binds)
	}
	if market, err := service.TaskMarket(context.Background(), "space", "ftask_swap1h"); err != nil || market != "swap" {
		t.Fatalf("任务市场不符：%s %v", market, err)
	}
}

// lateView 模拟任务刚建、结果 View 还没建出：前几次读取返回 View 不存在。
type lateView struct {
	*fakeClient
	missing string
	reads   int
	after   int
}

func (c *lateView) GetView(ctx context.Context, space, viewID string) (ViewInfo, error) {
	if viewID == c.missing {
		c.reads++
		if c.reads <= c.after {
			return ViewInfo{}, ErrViewNotFound
		}
	}
	return c.fakeClient.GetView(ctx, space, viewID)
}

// 启用前的市场核对在 ReadyWait 内等结果 View 建出；等不到就报错，且始终没有关联任务；其他错误（市场对不上）不重试。
func TestResolveInstanceWaitsForViewBeforeCheckingMarkets(t *testing.T) {
	tasks := map[string]string{"spot": "ftask_1h", "swap": "ftask_swap1h"}
	client := &lateView{fakeClient: newDualClient(), missing: "view_factor_swap1h", after: 3}
	sets := &tasksFake{}
	service := Service{Client: client, Sets: sets, ReadyWait: time.Second, ReadyStep: time.Millisecond}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", tasks, parseStrategy(t, dualDSL)); err != nil {
		t.Fatalf("View 稍后建出，应等到并通过：%v", err)
	}
	if len(sets.binds) != 2 || client.reads <= 3 {
		t.Fatalf("核对通过之后才关联两个任务：binds=%v reads=%d", sets.binds, client.reads)
	}

	never := &lateView{fakeClient: newDualClient(), missing: "view_factor_swap1h", after: 1 << 30}
	sets = &tasksFake{}
	service = Service{Client: never, Sets: sets, ReadyWait: 20 * time.Millisecond, ReadyStep: time.Millisecond}
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", tasks, parseStrategy(t, dualDSL)); err == nil || !strings.Contains(err.Error(), "无法确定因子任务 ftask_swap1h 的市场") {
		t.Fatalf("等不到 View 应报错：%v", err)
	}
	if len(sets.binds) != 0 {
		t.Fatalf("核对没通过不能关联任务：%v", sets.binds)
	}

	wrong := &lateView{fakeClient: newDualClient()}
	sets = &tasksFake{}
	service = Service{Client: wrong, Sets: sets, ReadyWait: time.Second, ReadyStep: time.Millisecond}
	started := time.Now()
	if _, _, err := service.ResolveInstance(context.Background(), "i1", "space", map[string]string{"swap": "ftask_1h"}, parseStrategy(t, strings.Replace(dualDSL, "market: {select: both, prefer: swap}\n", "market: {select: swap}\n", 1))); err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("市场对不上应立即报错而不是等待：%v", err)
	}
}

// 回放区间逐个 View 校验：起点要在每个 View 的可用范围内，终点取各 View 中最早的，并返回各市场的索引代次。
func TestServiceReplayWindowChecksEveryView(t *testing.T) {
	client := newDualClient()
	spot, swap := client.views["view_factor_1h"], client.views["view_factor_swap1h"]
	spot.Generation, spot.IndexedFrom, spot.IndexedTo = "idx_a@s", barStart.Add(-500*time.Hour), barStart
	swap.Generation, swap.IndexedFrom, swap.IndexedTo = "idx_b@w", barStart.Add(-50*time.Hour), barStart.Add(-10*time.Hour)
	client.views["view_factor_1h"], client.views["view_factor_swap1h"] = spot, swap
	resolved := Resolved{Market: dsl.Market{Select: dsl.MarketBoth, Prefer: dsl.MarketSpot}, Calendar: DefaultCalendar, Bar: "1h", Tasks: map[string]TaskBinding{
		"spot": {ViewID: "view_factor_1h"}, "swap": {ViewID: "view_factor_swap1h"}}}
	service := Service{Client: client}

	end, generations, err := service.ReplayWindow(context.Background(), "space", resolved, nil, barStart.Add(-40*time.Hour), barStart.Add(10*time.Hour), testNow)
	if err != nil || !end.Equal(barStart.Add(-10*time.Hour)) || generations["spot"] != "idx_a@s" || generations["swap"] != "idx_b@w" || len(generations) != 2 {
		t.Fatalf("终点应取各 View 中最早的、并返回各市场的代次：end=%s generations=%v err=%v", end, generations, err)
	}
	// 起点在现货的范围内，却早于永续的覆盖起点：被永续的 View 拒绝。
	if _, _, err := service.ReplayWindow(context.Background(), "space", resolved, nil, barStart.Add(-200*time.Hour), barStart, testNow); err == nil || !strings.Contains(err.Error(), "view_factor_swap1h") {
		t.Fatalf("起点早于永续 View 的覆盖起点应被拒绝并指明 View：%v", err)
	}
}
