package replay

import (
	"context"
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// dualClient 是现货与永续各一个结果 View 的客户端，按 View 与数据集转发给各自的模拟数据。
type dualClient struct{ spot, swap *fakeClient }

// swapPrice 是永续的收盘价：与现货错开时间、水平也不同，价格表混用时回放结果会对不上。
func swapPrice(id string, hour int) float64 { return price(id, hour+3) + 1 }

func newDualClient() *dualClient {
	return &dualClient{
		spot: &fakeClient{marketType: "spot"},
		swap: &fakeClient{marketType: "swap", viewName: "view_factor_b", datasetName: "ds_swap", priceFn: swapPrice},
	}
}

func (c *dualClient) byView(viewID string) *fakeClient {
	if viewID == c.swap.view() {
		return c.swap
	}
	return c.spot
}

func (c *dualClient) byDataset(datasetID string) *fakeClient {
	if datasetID == c.swap.dataset() {
		return c.swap
	}
	return c.spot
}

func (c *dualClient) GetView(ctx context.Context, spaceID, viewID string) (input.ViewInfo, error) {
	return c.byView(viewID).GetView(ctx, spaceID, viewID)
}

func (c *dualClient) ViewGeneration(ctx context.Context, spaceID, viewID string) (string, error) {
	return c.byView(viewID).ViewGeneration(ctx, spaceID, viewID)
}

func (c *dualClient) ViewCoverage(ctx context.Context, spaceID string, view input.ViewInfo, exact bool) (input.Coverage, error) {
	return c.byView(view.ViewID).ViewCoverage(ctx, spaceID, view, exact)
}

func (c *dualClient) GetDataset(ctx context.Context, spaceID, datasetID string) (input.DatasetInfo, error) {
	return c.byDataset(datasetID).GetDataset(ctx, spaceID, datasetID)
}

func (c *dualClient) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]input.Subject, error) {
	return c.byDataset(datasetID).ListDatasetSubjects(ctx, spaceID, datasetID)
}

func (c *dualClient) ListTagMembers(ctx context.Context, spaceID, tagID string) ([]string, error) {
	return c.spot.ListTagMembers(ctx, spaceID, tagID)
}

func (c *dualClient) GetTag(ctx context.Context, spaceID, tagID string) (input.TagInfo, error) {
	return c.spot.GetTag(ctx, spaceID, tagID)
}

func (c *dualClient) QueryRows(ctx context.Context, spaceID string, query input.Query) ([]input.Row, uint64, error) {
	return c.byView(query.ViewID).QueryRows(ctx, spaceID, query)
}

func (c *dualClient) GetFactor(ctx context.Context, factorID string) (input.FactorInfo, error) {
	return c.spot.GetFactor(ctx, factorID)
}

var (
	spotTask = store.ReplayTask{FactorTaskID: "ftask_a", ViewID: "view_factor_a", ViewGeneration: "idx"}
	swapTask = store.ReplayTask{FactorTaskID: "ftask_b", ViewID: "view_factor_b", ViewGeneration: "idx"}
)

// startMarketReplay 登记并认领一个按市场读取任务的回放：区间从第 2 根开始，共 hours 根。
func startMarketReplay(t *testing.T, repo *store.Store, client input.Client, id, dslYaml string, hours int, tasks map[string]store.ReplayTask, spotFee, swapFee float64) store.Replay {
	t.Helper()
	strategy, err := dsl.Parse([]byte(dslYaml))
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]string, len(tasks))
	for market, task := range tasks {
		ids[market] = task.FactorTaskID
	}
	resolved, _, err := input.Resolve(context.Background(), client, "crypto", input.TaskIDs(ids), strategy)
	if err != nil {
		t.Fatal(err)
	}
	job := store.Replay{ReplayID: id, DSLYaml: dslYaml, DSLHash: dsl.Hash([]byte(dslYaml)), Calendar: "crypto_24x7", SpaceID: "crypto", Tasks: tasks, StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(time.Duration(2+hours) * time.Hour), SpotFeeBps: spotFee, SwapFeeBps: swapFee, Factors: resolved.Factors, CreatedAt: origin}
	if err := repo.CreateReplay(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	if err != nil || !found || claimed.ReplayID != id {
		t.Fatalf("认领回放失败：%+v found=%v err=%v", claimed, found, err)
	}
	return claimed
}

type barTarget struct {
	InstrumentID string `json:"instrument_id"`
	Market       string `json:"market"`
	TargetWeight string `json:"target_weight"`
}

func decodeTargets(t *testing.T, bar store.ReplayBar) map[string]float64 {
	t.Helper()
	var targets []barTarget
	if err := json.Unmarshal(bar.TargetsJSON, &targets); err != nil {
		t.Fatal(err)
	}
	weights := make(map[string]float64, len(targets))
	for _, target := range targets {
		weight, err := strconv.ParseFloat(target.TargetWeight, 64)
		if err != nil {
			t.Fatal(err)
		}
		weights[target.InstrumentID+"|"+target.Market] = weight
	}
	return weights
}

type positionRow struct {
	InstrumentID string  `json:"instrument_id"`
	Market       string  `json:"market"`
	Quantity     float64 `json:"quantity"`
	Value        float64 `json:"value"`
}

func decodePositions(t *testing.T, bar store.ReplayBar) map[string]positionRow {
	t.Helper()
	var snapshot struct {
		Positions map[string]positionRow `json:"positions"`
	}
	if err := json.Unmarshal(bar.PositionsJSON, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot.Positions
}

// hourOf 返回一根 K 线（bar_end 是结束时刻）收盘价所在的小时序号。
func hourOf(bar store.ReplayBar) int { return int(bar.BarEndTime.Sub(origin)/time.Hour) - 1 }

// requireEquityFollowsTargets 不借助账本，只按每期的目标权重与各市场自己的价格推算权益（无手续费、每期调到目标）：
// 下一期的权益 = 本期权益 × (1 + Σ 权重 × 该（标的，市场）的价格涨跌)。
func requireEquityFollowsTargets(t *testing.T, bars []store.ReplayBar, initial float64, prices map[string]func(string, int) float64) {
	t.Helper()
	equity := initial
	for i, bar := range bars {
		if i > 0 {
			growth := 1.0
			for key, weight := range decodeTargets(t, bars[i-1]) {
				id, market, _ := strings.Cut(key, "|")
				fn := prices[market]
				growth += weight * (fn(id, hourOf(bar))/fn(id, hourOf(bars[i-1])) - 1)
			}
			equity *= growth
		}
		if math.Abs(bar.Equity-equity) > 1e-3 {
			t.Fatalf("第 %d 期（小时 %d）权益 %.4f，按目标与各市场价格推算应为 %.4f", i, hourOf(bar), bar.Equity, equity)
		}
	}
}

const swapOnlyDSL = `name: swap_only
market: {select: swap}
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// 纯永续策略的回放：持仓键带永续市场，权益按永续价格逐期结算；永续只收永续费率，现货费率不起作用。
func TestReplaySwapOnlyUsesSwapPricesAndFeeRate(t *testing.T) {
	prices := map[string]func(string, int) float64{"swap": swapPrice}
	run := func(spotFee, swapFee float64) (store.Replay, []store.ReplayBar) {
		repo := openStore(t)
		client := newDualClient()
		job := startMarketReplay(t, repo, client, "p1", swapOnlyDSL, 10, map[string]store.ReplayTask{"swap": swapTask}, spotFee, swapFee)
		(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 100}).Execute(context.Background(), job)
		done, err := repo.GetReplay(context.Background(), "p1")
		if err != nil || done.Status != store.ReplayDone {
			t.Fatalf("回放应完成：%+v err=%v", done, err)
		}
		return done, barsOf(t, repo, "p1")
	}
	done, bars := run(0, 0)
	if len(bars) != 10 {
		t.Fatalf("应有 10 根：%d", len(bars))
	}
	for _, bar := range bars {
		positions := decodePositions(t, bar)
		if len(positions) != 1 {
			t.Fatalf("每期只持有一个永续多头：%s", bar.PositionsJSON)
		}
		for key, position := range positions {
			if !strings.HasSuffix(key, "|swap") || position.Market != "swap" || position.Quantity <= 0 {
				t.Fatalf("持仓应是永续多头：%s", bar.PositionsJSON)
			}
		}
		if bar.SpotValue != 0 || math.Abs(bar.SwapValue-bar.Equity) > 1e-3 {
			t.Fatalf("永续满仓：永续名义价值应等于权益：spot=%v swap=%v equity=%v", bar.SpotValue, bar.SwapValue, bar.Equity)
		}
	}
	requireEquityFollowsTargets(t, bars, 100, prices)
	var metrics Metrics
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	if metrics.OKBars != 10 || metrics.TotalFee != 0 || !strings.Contains(strings.Join(metrics.Limitations, "；"), "未计资金费") {
		t.Fatalf("永续回放的绩效与局限不符：%+v", metrics)
	}
	_, withSpotFee := run(50, 0)
	for i := range bars {
		if bars[i].Equity != withSpotFee[i].Equity {
			t.Fatalf("纯永续回放不应受现货费率影响：第 %d 期 %v / %v", i, bars[i].Equity, withSpotFee[i].Equity)
		}
	}
	chargedDone, charged := run(0, 10)
	total := 0.0
	for _, bar := range charged {
		total += bar.Fee
	}
	if total <= 0 || bars[len(bars)-1].Equity <= charged[len(charged)-1].Equity {
		t.Fatalf("永续费率应扣手续费：总费用 %v，无费率终值 %v，有费率终值 %v", total, bars[len(bars)-1].Equity, charged[len(charged)-1].Equity)
	}
	var chargedMetrics Metrics
	if err := json.Unmarshal(chargedDone.MetricsJSON, &chargedMetrics); err != nil || math.Abs(chargedMetrics.TotalFee-total) > 1e-3 {
		t.Fatalf("指标的总费用应等于逐期费用之和：%+v / %v err=%v", chargedMetrics, total, err)
	}
}

const hedgeDSL = `name: hedge
market: {select: both, prefer: spot}
rules:
  - id: l
    type: rank
    score: "mom"
    select: {top: 1}
    weight: 0.5
  - id: s
    type: rank
    side: short
    score: "-mom"
    select: {top: 1}
    weight: 0.5
portfolio:
  leverage: 2
  max_missing: 1
`

// 混合市场：多头只在现货、空头只在永续，两个 View 各用各的价格；每期结果带现货与永续分别的持仓市值，手续费分市场计。
func TestReplayMixedMarketHedgeUsesPerMarketPrices(t *testing.T) {
	prices := map[string]func(string, int) float64{"spot": price, "swap": swapPrice}
	repo := openStore(t)
	client := newDualClient()
	job := startMarketReplay(t, repo, client, "p1", hedgeDSL, 10, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 0, 0)
	(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 100}).Execute(context.Background(), job)
	if done, _ := repo.GetReplay(context.Background(), "p1"); done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v", done)
	}
	bars := barsOf(t, repo, "p1")
	for _, bar := range bars {
		positions := decodePositions(t, bar)
		var spotLong, swapShort int
		for _, position := range positions {
			switch {
			case position.Market == "spot" && position.Quantity > 0:
				spotLong++
			case position.Market == "swap" && position.Quantity < 0:
				swapShort++
			}
		}
		if spotLong != 1 || swapShort != 1 || len(positions) != 2 {
			t.Fatalf("应是一个现货多头加一个永续空头：%s", bar.PositionsJSON)
		}
		if bar.SpotValue <= 0 || bar.SwapValue >= 0 {
			t.Fatalf("现货市值应为正、永续净名义价值应为负：spot=%v swap=%v", bar.SpotValue, bar.SwapValue)
		}
	}
	requireEquityFollowsTargets(t, bars, 100, prices)

	// 费率分市场：现货 10 bps、永续 20 bps。
	feeRepo := openStore(t)
	feeJob := startMarketReplay(t, feeRepo, client, "p2", hedgeDSL, 10, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 10, 20)
	(&Runner{Store: feeRepo, Client: client, ChunkBars: 4, InitialEquity: 100}).Execute(context.Background(), feeJob)
	charged := barsOf(t, feeRepo, "p2")
	first := charged[0]
	// 首期从空仓建仓：费用 = 现货成交额 × 0.1% + 永续成交额 × 0.2%，成交额就是各自的持仓市值。
	want := first.SpotValue*0.001 + (-first.SwapValue)*0.002
	if math.Abs(first.Fee-want) > 1e-3 || first.Fee <= 0 {
		t.Fatalf("首期手续费应分市场计：%v，期望 %v（现货 %v，永续 %v）", first.Fee, want, first.SpotValue, first.SwapValue)
	}
}

const agedPreferSwapDSL = `name: aged
market: {select: both, prefer: swap}
universe:
  min_age_bars: 3
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// 年龄判定按市场各自的历史：老币刚上线永续（永续没有行的小时记作新上市）时，不能借现货的历史让永续的行通过年龄检查，
// 这几根只能用现货的行；永续有满够年龄的历史后才改用永续。
func TestReplayAgeIsJudgedPerMarket(t *testing.T) {
	repo := openStore(t)
	client := newDualClient()
	client.swap.missing = map[string]map[int]bool{"B": {0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 6: true, 7: true}}
	// B 的现货在第 8 小时之前一路下跌（动量为负、不会被选中，也就不会被持仓粘住），第 8 小时起恢复原样并领先。
	client.spot.priceFn = func(id string, hour int) float64 {
		if id == "B" && hour < 8 {
			return 40 - 3*float64(hour)
		}
		return price(id, hour)
	}
	job := startMarketReplay(t, repo, client, "p1", agedPreferSwapDSL, 14, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 0, 0)
	(&Runner{Store: repo, Client: client, ChunkBars: 5}).Execute(context.Background(), job)
	if done, _ := repo.GetReplay(context.Background(), "p1"); done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v", done)
	}
	// B 的永续行从第 8 小时起有，年龄目标是 T−2（向前容忍两根）：第 10 小时起永续才满足年龄。第 8 小时 B 已领先，
	// 优先永续却只能取现货的行；第 11 小时（第 10 小时 B 动量转负被卖出）重新入选时才取永续。
	var targetAt8, targetAt11 string
	for _, bar := range barsOf(t, repo, "p1") {
		for key := range decodeTargets(t, bar) {
			if key == "B|swap" && hourOf(bar) < 10 {
				t.Fatalf("第 %d 小时 B 的永续不满足年龄，不能入选：%s", hourOf(bar), bar.TargetsJSON)
			}
			switch hourOf(bar) {
			case 8:
				targetAt8 = key
			case 11:
				targetAt11 = key
			}
		}
	}
	if targetAt8 != "B|spot" || targetAt11 != "B|swap" {
		t.Fatalf("第 8 小时应持有 B 的现货、第 11 小时改为 B 的永续：%q / %q", targetAt8, targetAt11)
	}
}

// 某个市场的活动索引在回放中换代：只对那个市场复查剩余区间并切换，另一个市场不受影响，回放继续完成。
func TestReplayFollowsIndexSwitchOfOneMarket(t *testing.T) {
	repo := openStore(t)
	client := newDualClient()
	client.swap.switchAtQuery, client.swap.switchedFrom = 2, origin
	job := startMarketReplay(t, repo, client, "p1", hedgeDSL, 12, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 0, 0)
	(&Runner{Store: repo, Client: client, ChunkBars: 4}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v", done)
	}
	var metrics Metrics
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil || !strings.Contains(strings.Join(metrics.Limitations, "；"), "重建了 1 次索引") {
		t.Fatalf("应记录一次换代：%+v err=%v", metrics, err)
	}
	if client.spot.activeIndex != "" || client.swap.activeIndex != "idx2" {
		t.Fatalf("只有永续换了索引：spot=%q swap=%q", client.spot.activeIndex, client.swap.activeIndex)
	}
}

// referenceEquity 是独立于账本的权益推算，只用每期的目标权重、各（标的，市场）的价格、费率与是否有价：
//  1. 有价的持仓名义价值随价格涨跌，权益随之变化（永续逐期结算进现金，与现货市值对权益的作用相同）；无价的持仓冻结不动；
//  2. 持仓期间权益不超过 0 即爆仓：此后目标视为空；
//  3. 可成交的（有价的持仓与目标）按 A = 权益 − Σ 费率 × |目标权重 × A − 现有名义价值| 迭代求 A，名义价值调到 目标权重 × A；
//  4. 跳过的期不成交。
//
// 它不模拟现货的现金约束，所以只用在现货权重不大的场景。
func referenceEquity(t *testing.T, bars []store.ReplayBar, initial float64, rates map[string]float64, price func(market, id string, hour int) (float64, bool)) []float64 {
	t.Helper()
	type held struct{ notional, last float64 }
	positions := map[string]*held{}
	split := func(key string) (string, string) {
		id, market, _ := strings.Cut(key, "|")
		return id, market
	}
	equity, bankrupt := initial, false
	var series []float64
	for _, bar := range bars {
		hour := hourOf(bar)
		before := equity
		keys := make([]string, 0, len(positions))
		for key := range positions {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			id, market := split(key)
			if px, ok := price(market, id, hour); ok {
				position := positions[key]
				moved := position.notional * px / position.last
				before += moved - position.notional
				position.notional, position.last = moved, px
			}
		}
		if !bankrupt && before <= 0 && len(positions) > 0 {
			bankrupt = true
		}
		if bar.Status != "ok" {
			equity = before
			series = append(series, equity)
			continue
		}
		targets := decodeTargets(t, bar)
		if bankrupt {
			targets = nil
		}
		tradable := map[string]float64{}
		for key := range positions {
			id, market := split(key)
			if px, ok := price(market, id, hour); ok {
				tradable[key] = px
			}
		}
		for key, weight := range targets {
			id, market := split(key)
			if px, ok := price(market, id, hour); ok && weight != 0 {
				tradable[key] = px
			}
		}
		current := func(key string) float64 {
			if position := positions[key]; position != nil {
				return position.notional
			}
			return 0
		}
		adjusted := before
		for i := 0; i < 200; i++ {
			cost := 0.0
			for key := range tradable {
				_, market := split(key)
				cost += rates[market] * math.Abs(targets[key]*adjusted-current(key))
			}
			adjusted = before - cost
		}
		fee := 0.0
		for key, px := range tradable {
			_, market := split(key)
			target := targets[key] * adjusted
			fee += rates[market] * math.Abs(target-current(key))
			if math.Abs(target) < 1e-9 {
				delete(positions, key)
			} else {
				positions[key] = &held{notional: target, last: px}
			}
		}
		equity = before - fee
		series = append(series, equity)
	}
	return series
}

func requireMatchesReference(t *testing.T, bars []store.ReplayBar, initial float64, rates map[string]float64, price func(market, id string, hour int) (float64, bool)) {
	t.Helper()
	want := referenceEquity(t, bars, initial, rates, price)
	for i, bar := range bars {
		if math.Abs(bar.Equity-want[i]) > 1e-3+1e-6*math.Abs(want[i]) {
			t.Fatalf("第 %d 期（小时 %d）权益 %.4f，独立推算应为 %.4f", i, hourOf(bar), bar.Equity, want[i])
		}
	}
}

// pricesOf 把各市场的价格函数与“某小时没有行”的清单合成独立推算要用的取价函数。
func pricesOf(client *dualClient) func(market, id string, hour int) (float64, bool) {
	return func(market, id string, hour int) (float64, bool) {
		fake := client.spot
		if market == "swap" {
			fake = client.swap
		}
		if fake.missing[id][hour] {
			return 0, false
		}
		return fake.priceAt(id, hour), true
	}
}

// 带手续费的纯永续与混合对冲：权益与独立推算逐期一致（费用迭代、分市场费率、重新平衡）。
func TestReplayEquityMatchesReferenceWithFees(t *testing.T) {
	cases := []struct {
		name         string
		dsl          string
		tasks        map[string]store.ReplayTask
		spot, swap   float64
		spotR, swapR float64
	}{
		{"纯永续", swapOnlyDSL, map[string]store.ReplayTask{"swap": swapTask}, 0, 10, 0, 0.001},
		{"混合对冲", hedgeDSL, map[string]store.ReplayTask{"spot": spotTask, "swap": swapTask}, 10, 20, 0.001, 0.002},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo := openStore(t)
			client := newDualClient()
			job := startMarketReplay(t, repo, client, "p1", c.dsl, 12, c.tasks, c.spot, c.swap)
			(&Runner{Store: repo, Client: client, ChunkBars: 5, InitialEquity: 100}).Execute(context.Background(), job)
			bars := barsOf(t, repo, "p1")
			if len(bars) != 12 {
				t.Fatalf("应有 12 根：%d", len(bars))
			}
			requireMatchesReference(t, bars, 100, map[string]float64{"spot": c.spotR, "swap": c.swapR}, pricesOf(client))
		})
	}
}

// 持仓的标的所在 View 缺了几根：账本冻结它（不结算、不成交），价格回来后从冻结前的价格结算；权益与独立推算一致。
func TestReplayEquityMatchesReferenceWithFrozenSwap(t *testing.T) {
	repo := openStore(t)
	client := newDualClient()
	gap := map[int]bool{6: true, 7: true}
	client.swap.missing = map[string]map[int]bool{"A": gap, "B": gap}
	job := startMarketReplay(t, repo, client, "p1", swapOnlyDSL, 12, map[string]store.ReplayTask{"swap": swapTask}, 0, 10)
	(&Runner{Store: repo, Client: client, ChunkBars: 5, InitialEquity: 100, MissingPriceLiquidateBars: 5}).Execute(context.Background(), job)
	bars := barsOf(t, repo, "p1")
	frozen := 0
	for _, bar := range bars {
		frozen += bar.Frozen
	}
	if frozen == 0 {
		t.Fatal("这个场景应让持仓冻结至少一期")
	}
	requireMatchesReference(t, bars, 100, map[string]float64{"swap": 0.001}, pricesOf(client))
}

const leveragedSwapDSL = `name: leveraged
market: {select: swap}
rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 5}
portfolio:
  leverage: 5
  max_missing: 1
`

// crashPrice 在第 6 小时暴跌 30%、之后继续跌到 50，再反弹。
func crashPrice(_ string, hour int) float64 {
	return map[int]float64{6: 70, 7: 60, 8: 50, 9: 80, 10: 100, 11: 120}[hour] + 100*boolFloat(hour <= 5 || hour > 11)
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
}

// 纯永续 5 倍杠杆，价格暴跌 30% 使权益变成 −50：模拟爆仓，持仓平掉，之后价格再怎么变都空仓、权益不变，
// 不会被“目标权重 × 负权益”翻成反向持仓而“复活”。
func TestReplayLeveragedSwapBlowsUpAndStaysFlat(t *testing.T) {
	repo := openStore(t)
	client := newDualClient()
	client.swap.priceFn = crashPrice
	job := startMarketReplay(t, repo, client, "p1", leveragedSwapDSL, 10, map[string]store.ReplayTask{"swap": swapTask}, 0, 0)
	(&Runner{Store: repo, Client: client, ChunkBars: 4, InitialEquity: 100}).Execute(context.Background(), job)
	done, _ := repo.GetReplay(context.Background(), "p1")
	if done.Status != store.ReplayDone {
		t.Fatalf("回放应完成：%+v", done)
	}
	bars := barsOf(t, repo, "p1")
	var metrics Metrics
	if err := json.Unmarshal(done.MetricsJSON, &metrics); err != nil {
		t.Fatal(err)
	}
	crashAt := origin.Add(7 * time.Hour) // 第 6 小时这根的 bar_end
	if !metrics.BankruptAt.Equal(crashAt) || !strings.Contains(strings.Join(metrics.Limitations, "；"), "模拟爆仓") {
		t.Fatalf("应记录爆仓期与说明：%+v", metrics)
	}
	for _, bar := range bars {
		switch {
		case hourOf(bar) < 6:
			if len(decodePositions(t, bar)) != 1 {
				t.Fatalf("爆仓前应满仓 5 倍多头：%s", bar.PositionsJSON)
			}
		case hourOf(bar) == 6:
			if math.Abs(bar.Equity+50) > 1e-3 || len(decodePositions(t, bar)) != 0 {
				t.Fatalf("第 6 小时权益应为 −50 并已平仓：%v %s", bar.Equity, bar.PositionsJSON)
			}
		default:
			if math.Abs(bar.Equity+50) > 1e-3 || len(decodePositions(t, bar)) != 0 || bar.SwapValue != 0 || bar.Turnover != 0 {
				t.Fatalf("爆仓后应一直空仓、权益不变：小时 %d 权益 %v %s", hourOf(bar), bar.Equity, bar.PositionsJSON)
			}
		}
	}
	requireMatchesReference(t, bars, 100, map[string]float64{"swap": 0}, pricesOf(client))
}
