package exec

import (
	"math"
	"math/rand"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// stockDailySpecs 从内置 A 股日线因子库里按因子 ID 取定义（与管理端导入的是同一份文件）。
func stockDailySpecs(t *testing.T, ids ...string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile("../../../modules/factor/factors/stockcn_daily.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		FactorID string `yaml:"factor_id"`
		Spec     string `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &entries); err != nil {
		t.Fatal(err)
	}
	specs := map[string]string{}
	for _, entry := range entries {
		for _, id := range ids {
			if entry.FactorID == id {
				specs[id] = entry.Spec
			}
		}
	}
	if len(specs) != len(ids) {
		t.Fatalf("因子库里找到 %d 个因子，应为 %d 个：%v", len(specs), len(ids), ids)
	}
	return specs
}

// 内置筹码因子在执行器上的数值检查：同一只股票给两份日线，一份中途 10 送 10（不复权价减半、后复权因子翻倍），一份没有除权。
// 两份的后复权价相同，所以获利比例、集中度应当相同，平均成本只差当天的后复权因子；换手率缺一天时仍然出值。
func TestChipLibraryFactorsAcrossExRights(t *testing.T) {
	const length, window, exRights, missing = 560, 210, 530, 520
	rng := rand.New(rand.NewSource(5))
	plain := map[string][]float64{}
	split := map[string][]float64{}
	for _, name := range []string{"open", "high", "low", "close", "adj_factor", "turnover_rate"} {
		plain[name], split[name] = make([]float64, length), make([]float64, length)
	}
	price := 40.0
	for i := 0; i < length; i++ {
		open := price * (1 + (rng.Float64()-0.5)*0.03)
		closes := open * (1 + (rng.Float64()-0.5)*0.06)
		high := math.Max(open, closes) * (1 + rng.Float64()*0.015)
		low := math.Min(open, closes) * (1 - rng.Float64()*0.015)
		price = closes
		turnover := 0.2 + rng.Float64()*4
		if i == missing {
			turnover = math.NaN()
		}
		factor := 1.0
		if i >= exRights {
			factor = 2 // 除以 2 再乘 2 没有舍入，两份数据的后复权价逐位相同
		}
		for name, value := range map[string]float64{"open": open, "high": high, "low": low, "close": closes} {
			plain[name][i], split[name][i] = value, value/factor
		}
		plain["adj_factor"][i], split["adj_factor"][i] = 1, factor
		plain["turnover_rate"][i], split["turnover_rate"][i] = turnover, turnover
	}

	specs := stockDailySpecs(t, "chip_profit_ratio", "chip_avg_cost", "chip_concentration")
	results := map[string][2][]float64{}
	var start int
	for id, spec := range specs {
		a, first := evalSpec(t, spec, plain, length)
		b, _ := evalSpec(t, spec, split, length)
		results[id], start = [2][]float64{a, b}, first
	}
	if start != 499 {
		t.Fatalf("起算位置 = %d，应为 499（回看按 window 上限 500 推导）", start)
	}
	for i := start; i < length; i++ {
		profit, cost, concentration := results["chip_profit_ratio"], results["chip_avg_cost"], results["chip_concentration"]
		if math.IsNaN(profit[0][i]) || math.IsNaN(cost[0][i]) || math.IsNaN(concentration[0][i]) {
			t.Fatalf("第 %d 根没有出值（换手率只缺第 %d 根，应按当天没有换手处理）", i, missing)
		}
		if profit[0][i] != profit[1][i] || concentration[0][i] != concentration[1][i] {
			t.Fatalf("第 %d 根：除权改变了获利比例或集中度：%v/%v、%v/%v", i, profit[0][i], profit[1][i], concentration[0][i], concentration[1][i])
		}
		if want := cost[0][i] / split["adj_factor"][i]; cost[1][i] != want {
			t.Fatalf("第 %d 根：除权后的平均成本 = %v，应为不除权时的成本 ÷ 后复权因子 = %v", i, cost[1][i], want)
		}
		lowest, highest := math.Inf(1), math.Inf(-1)
		for k := i - window + 1; k <= i; k++ {
			lowest, highest = math.Min(lowest, plain["low"][k]), math.Max(highest, plain["high"][k])
		}
		if cost[0][i] < lowest || cost[0][i] > highest {
			t.Fatalf("第 %d 根：平均成本 %v 不在窗口的最低价 %v 与最高价 %v 之间", i, cost[0][i], lowest, highest)
		}
		if !(profit[0][i] >= 0 && profit[0][i] <= 1) || !(concentration[0][i] >= 0 && concentration[0][i] < 1) {
			t.Fatalf("第 %d 根：获利比例 %v 或集中度 %v 超出范围", i, profit[0][i], concentration[0][i])
		}
	}
	// 除权日当天：平均成本与不复权收盘价同比例减半，收盘价 ÷ 平均成本 不受除权影响。
	before := split["close"][exRights] / results["chip_avg_cost"][1][exRights]
	after := plain["close"][exRights] / results["chip_avg_cost"][0][exRights]
	if before != after {
		t.Fatalf("除权日 收盘价÷平均成本：除权数据 %v、不除权数据 %v，应相同", before, after)
	}
}

// 内置估值因子在执行器上的数值检查：市值 = 收盘价 × 股本，估值再除以财务字段；
// 股本缺一天时沿用前值，连续 20 根没有才为空；分母为 0 或为空时结果为空。
func TestValuationLibraryFactors(t *testing.T) {
	const length = 60
	nan := math.NaN()
	data := map[string][]float64{}
	for _, name := range []string{"close", "total_shares", "float_shares", "dividend_per_share", "fin_net_profit_parent_ttm", "fin_equity_parent", "fin_revenue_ttm"} {
		data[name] = make([]float64, length)
	}
	for i := 0; i < length; i++ {
		data["close"][i] = 10 + float64(i%7)
		data["total_shares"][i], data["float_shares"][i], data["dividend_per_share"][i] = 2e9, 1.5e9, 0.5
		data["fin_net_profit_parent_ttm"][i], data["fin_equity_parent"][i], data["fin_revenue_ttm"][i] = 4e9, 1e10, 2e10
	}
	// 第 30 根：股本和每股股利没采到。第 31 根起送转，股本翻倍。
	data["total_shares"][30], data["float_shares"][30], data["dividend_per_share"][30] = nan, nan, nan
	for i := 31; i < length; i++ {
		data["total_shares"][i], data["float_shares"][i] = 4e9, 3e9
	}
	// 第 40 根：亏损（净利润为负）；第 41 根：净利润为 0；第 42 根：财务字段为空。
	data["fin_net_profit_parent_ttm"][40], data["fin_net_profit_parent_ttm"][41], data["fin_net_profit_parent_ttm"][42] = -1e9, 0, nan
	// 第 5～29 根的流通股本全部缺失：到第 23 根为止最近 20 根里还有值（第 4 根），之后为空。
	for i := 5; i < 30; i++ {
		data["float_shares"][i] = nan
	}

	specs := stockDailySpecs(t, "total_mv", "float_mv", "pe_ttm", "pb", "ps_ttm", "dv_ratio")
	out := map[string][]float64{}
	for id, spec := range specs {
		values, start := evalSpec(t, spec, data, length)
		if start != 19 {
			t.Fatalf("%s 的起算位置 = %d，应为 19（回看 20）", id, start)
		}
		out[id] = values
	}
	check := func(id string, index int, want float64) {
		t.Helper()
		got := out[id][index]
		if math.IsNaN(want) != math.IsNaN(got) || (!math.IsNaN(want) && math.Abs(got-want) > 1e-9*math.Abs(want)) {
			t.Fatalf("%s 第 %d 根 = %v，应为 %v", id, index, got, want)
		}
	}
	closeAt := func(i int) float64 { return data["close"][i] }
	// 普通的一根。
	check("total_mv", 20, closeAt(20)*2e9)
	check("pe_ttm", 20, closeAt(20)*2e9/4e9)
	check("pb", 20, closeAt(20)*2e9/1e10)
	check("ps_ttm", 20, closeAt(20)*2e9/2e10)
	check("dv_ratio", 20, 0.5/closeAt(20)*100)
	// 股本缺一天：沿用前一天的值；第二天用新的股本。
	check("total_mv", 30, closeAt(30)*2e9)
	check("dv_ratio", 30, 0.5/closeAt(30)*100)
	check("total_mv", 31, closeAt(31)*4e9)
	check("float_mv", 31, closeAt(31)*3e9)
	// 流通股本从第 5 根起缺失：第 24 根的窗口（第 5～24 根）里已经没有值。
	check("float_mv", 23, closeAt(23)*1.5e9)
	check("float_mv", 24, nan)
	check("float_mv", 29, nan)
	// 亏损是负数；分母为 0 或为空时为空，其他因子不受影响。
	check("pe_ttm", 40, closeAt(40)*4e9/-1e9)
	check("pe_ttm", 41, nan)
	check("pe_ttm", 42, nan)
	check("pb", 41, closeAt(41)*4e9/1e10)
}
