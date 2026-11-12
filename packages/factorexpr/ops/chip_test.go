package ops

import (
	"math"
	"math/rand"
	"testing"
)

// chipBars 是测试用的一段 K 线：每根是 {开, 高, 低, 收, 换手率%}。
type chipBars [][5]float64

func (b chipBars) columns() (open, high, low, closes, turnover []float64) {
	for _, bar := range b {
		open, high, low = append(open, bar[0]), append(high, bar[1]), append(low, bar[2])
		closes, turnover = append(closes, bar[3]), append(turnover, bar[4])
	}
	return
}

func (b chipBars) value(result chipResult) float64 {
	open, high, low, closes, turnover := b.columns()
	return chipValue(open, high, low, closes, turnover, make([]float64, chipBuckets), result)
}

func flat(price, turnover float64) [5]float64 {
	return [5]float64{price, price, price, price, turnover}
}

func near(got, want, tolerance float64) bool { return math.Abs(got-want) <= tolerance }

// 一字板的手算例子：第 1 根在 10 换手 50%，第 2 根在 20 换手 50%。
// 第 2 根之后 10 上剩 0.25、20 上有 0.5（单位：1/档宽），合计 0.75。
func TestChipLimitBarsByHand(t *testing.T) {
	bars := chipBars{flat(10, 50), flat(20, 50)}
	if got := bars.value(chipProfit); got != 1 {
		t.Fatalf("收盘在最高档时获利比例 = %v，应为 1", got)
	}
	if got := bars.value(chipCost(0.3)); got != 10 {
		t.Fatalf("分位 0.3 的成本 = %v，应为 10（最低档占 1/3）", got)
	}
	if got := bars.value(chipCost(0.5)); got != 20 {
		t.Fatalf("分位 0.5 的成本 = %v，应为 20", got)
	}
	// 再加一根没有换手的 K 线，收盘回到 10：分布不变，获利比例是最低档的占比 1/3。
	bars = append(bars, flat(10, 0))
	if got := bars.value(chipProfit); !near(got, 1.0/3, 1e-12) {
		t.Fatalf("获利比例 = %v，应为 1/3", got)
	}
	if got := bars.value(chipCost(0.5)); got != 20 {
		t.Fatalf("没有换手的 K 线改变了分布：分位 0.5 的成本 = %v", got)
	}
}

// 换手率超过 100% 按 100% 算：之前的筹码全部换掉。
func TestChipTurnoverIsCapped(t *testing.T) {
	bars := chipBars{flat(10, 50), flat(20, 250)}
	if got := bars.value(chipCost(0.01)); got != 20 {
		t.Fatalf("分位 0.01 的成本 = %v，应为 20（旧筹码应全部换掉）", got)
	}
}

// 普通 K 线按三角形分布：对称的一根，平均成本在中点，一半筹码获利；各档之和约等于 换手率/档宽，与一字板同一个单位。
func TestChipTriangleBar(t *testing.T) {
	bars := chipBars{{15, 20, 10, 15, 40}}
	width := 10.0 / (chipBuckets - 1)
	if got := bars.value(chipCost(0.5)); !near(got, 15, width) {
		t.Fatalf("平均成本 = %v，应在 15 附近", got)
	}
	if got := bars.value(chipProfit); !near(got, 0.5, 0.01) {
		t.Fatalf("获利比例 = %v，应在 0.5 附近", got)
	}
	total := bars.value(func(_ []float64, total, _, _, _ float64) float64 { return total })
	if !near(total*width, 0.4, 0.004) {
		t.Fatalf("各档之和 × 档宽 = %v，应约等于换手率 0.4", total*width)
	}
	limit := chipBars{{15, 20, 10, 15, 0}, flat(15, 40)}.value(func(_ []float64, total, _, _, _ float64) float64 { return total })
	if !near(limit*width, 0.4, 1e-12) {
		t.Fatalf("一字板加入的量 × 档宽 = %v，应等于换手率 0.4", limit*width)
	}
}

// 当根的最高最低价都落在两档之间时，筹码放进均价所在的档，不丢。
func TestChipBarBetweenGridPoints(t *testing.T) {
	// 档宽 1：价格档是 10、11、…、159。第 2 根在 20.2～20.8 之间，没有覆盖任何一档。
	bars := chipBars{{10, 159, 10, 100, 0}, {20.5, 20.8, 20.2, 20.5, 30}}
	var index int
	total := bars.value(func(grid []float64, total, _, _, _ float64) float64 {
		for j, chips := range grid {
			if chips > 0 {
				index = j
			}
		}
		return total
	})
	if !near(total, 0.3, 1e-12) || index != 10 {
		t.Fatalf("筹码合计 = %v、所在档 = %d，应为 0.3、第 10 档（价格 20）", total, index)
	}
}

func TestChipInvalidWindows(t *testing.T) {
	cases := map[string]chipBars{
		"价格没有波动":   {flat(10, 5), flat(10, 5)},
		"全程没有换手":   {flat(10, 0), flat(20, 0)},
		"价格不是正数":   {flat(10, 5), {0, 20, 10, 15, 5}},
		"最高价低于最低价": {flat(10, 5), {15, 12, 18, 15, 5}},
	}
	for name, bars := range cases {
		if got := bars.value(chipProfit); !math.IsNaN(got) {
			t.Errorf("%s：获利比例 = %v，应为 NaN", name, got)
		}
	}
}

func randomChipBars(rng *rand.Rand, n int) chipBars {
	bars := make(chipBars, n)
	price := 20 + rng.Float64()*80
	for i := range bars {
		open := price * (1 + (rng.Float64()-0.5)*0.04)
		closes := open * (1 + (rng.Float64()-0.5)*0.08)
		high := math.Max(open, closes) * (1 + rng.Float64()*0.02)
		low := math.Min(open, closes) * (1 - rng.Float64()*0.02)
		bars[i] = [5]float64{open, high, low, closes, rng.Float64() * 6}
		price = closes
	}
	return bars
}

// 分位成本随 q 单调不减，并落在窗口的最低价与最高价之间；获利比例在 0～1 之间。
func TestChipCostIsMonotonic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 50; round++ {
		bars := randomChipBars(rng, 120)
		_, high, low, _, _ := bars.columns()
		previous := windowMin(low)
		for _, q := range []float64{0.01, 0.05, 0.25, 0.5, 0.75, 0.95, 0.99} {
			cost := bars.value(chipCost(q))
			if cost < previous || cost > windowMax(high)+1e-9 {
				t.Fatalf("第 %d 组：分位 %v 的成本 %v 不在 [%v, %v] 内", round, q, cost, previous, windowMax(high))
			}
			previous = cost
		}
		if profit := bars.value(chipProfit); !(profit >= 0 && profit <= 1) {
			t.Fatalf("第 %d 组：获利比例 %v 不在 0～1 之间", round, profit)
		}
	}
}

// 所有价格乘同一个常数（换一种复权口径）：获利比例不变，成本价乘同一个常数。
func TestChipIsScaleInvariant(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	const factor = 8.0 // 2 的幂，乘法没有舍入，换算档下标时与原价逐位一致
	for round := 0; round < 20; round++ {
		bars := randomChipBars(rng, 210)
		scaled := make(chipBars, len(bars))
		for i, bar := range bars {
			scaled[i] = [5]float64{bar[0] * factor, bar[1] * factor, bar[2] * factor, bar[3] * factor, bar[4]}
		}
		if a, b := bars.value(chipProfit), scaled.value(chipProfit); !near(a, b, 1e-12) {
			t.Fatalf("第 %d 组：获利比例 %v 与缩放后的 %v 不同", round, a, b)
		}
		if a, b := bars.value(chipCost(0.5)), scaled.value(chipCost(0.5)); !near(a*factor, b, 1e-9*b) {
			t.Fatalf("第 %d 组：平均成本 %v × %v 与缩放后的 %v 不同", round, a, factor, b)
		}
	}
}

// 内核逐点取窗口：每个输出点等于单独算这个窗口的结果；窗口里任一输入有 NaN 时输出 NaN。
func TestChipKernelSlidesWindow(t *testing.T) {
	const n, length = 30, 80
	bars := randomChipBars(rand.New(rand.NewSource(3)), length)
	open, high, low, closes, turnover := bars.columns()
	turnover[50] = math.NaN()
	in := []Series{{Data: open}, {Data: high}, {Data: low}, {Data: closes}, {Data: turnover}}
	out := make([]float64, length-n+1)
	rolling["chip_cost"]([]int{n}, []float64{0.5})(out, n-1, in)
	for i, got := range out {
		t0 := i + n - 1
		if t0 >= 50 && t0 < 50+n {
			if !math.IsNaN(got) {
				t.Fatalf("第 %d 根的窗口含 NaN，输出 %v", t0, got)
			}
			continue
		}
		want := bars[i : i+n].value(chipCost(0.5))
		if got != want {
			t.Fatalf("第 %d 根：内核输出 %v，单独计算 %v", t0, got, want)
		}
	}
}
