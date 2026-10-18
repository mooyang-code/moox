package replay

import "math"

// 回放输出的精度规范（只作用于写入记录的输出，不改变账本模拟的内部状态）：
// 金额、占比保留 4 位小数，持仓数量保留 8 位（加密货币的最小交易单位），价格是输入数据，原样保留。
const (
	outputPlaces   = 4
	quantityPlaces = 8
)

func roundPlaces(value float64, places int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	factor := math.Pow(10, float64(places))
	scaled := value * factor
	// 放大后超过 2^52 时浮点的分辨率已经粗于所需的小数精度（取整不改变值），而且很大的值放大会溢出成 Inf：原样返回。
	if math.IsInf(scaled, 0) || math.Abs(scaled) >= 1<<52 {
		return value
	}
	rounded := math.Round(scaled) / factor
	if rounded == 0 {
		return 0 // 避免 -0
	}
	return rounded
}

func round4(value float64) float64 { return roundPlaces(value, outputPlaces) }

func round4Ptr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	rounded := round4(*value)
	return &rounded
}

// rounded 返回按输出精度取整的账本变化副本。
func (o Outcome) rounded() Outcome {
	o.EquityBefore, o.EquityAfter = round4(o.EquityBefore), round4(o.EquityAfter)
	o.Traded, o.Fee, o.Turnover = round4(o.Traded), round4(o.Fee), round4(o.Turnover)
	o.BuyScale = round4Ptr(o.BuyScale)
	return o
}

// rounded 返回按输出精度取整的绩效副本。
func (m Metrics) rounded() Metrics {
	m.InitialEquity, m.FinalEquity = round4(m.InitialEquity), round4(m.FinalEquity)
	m.TotalReturn, m.MaxDrawdown = round4(m.TotalReturn), round4(m.MaxDrawdown)
	m.AverageTurnover, m.AverageHoldings, m.TotalFee = round4(m.AverageTurnover), round4(m.AverageHoldings), round4(m.TotalFee)
	m.AnnualizedReturn = round4Ptr(m.AnnualizedReturn)
	return m
}
