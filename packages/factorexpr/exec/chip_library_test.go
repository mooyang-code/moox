package exec

import (
	"math"
	"math/rand"
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

// chipLibrarySpecs 读内置 A 股日线因子库里的三个筹码因子（与管理端导入的是同一份文件）。
func chipLibrarySpecs(t *testing.T) map[string]string {
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
		if entry.FactorID == "chip_profit_ratio" || entry.FactorID == "chip_avg_cost" || entry.FactorID == "chip_concentration" {
			specs[entry.FactorID] = entry.Spec
		}
	}
	if len(specs) != 3 {
		t.Fatalf("因子库里的筹码因子有 %d 个，应为 3 个", len(specs))
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

	specs := chipLibrarySpecs(t)
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
