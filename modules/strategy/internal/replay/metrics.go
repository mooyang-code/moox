package replay

import (
	"math"
	"strings"
	"time"
)

// Metrics 是回放的绩效与局限性说明，写入 metrics_json。
type Metrics struct {
	Bars             int               `json:"bars"`
	OKBars           int               `json:"ok_bars"`
	SkippedBars      int               `json:"skipped_bars"`
	SkipReasons      map[string]int    `json:"skip_reasons,omitempty"`
	InitialEquity    float64           `json:"initial_equity"`
	FinalEquity      float64           `json:"final_equity"`
	TotalReturn      float64           `json:"total_return"`
	AnnualizedReturn *float64          `json:"annualized_return,omitempty"`
	MaxDrawdown      float64           `json:"max_drawdown"`
	AverageTurnover  float64           `json:"average_turnover"`
	AverageHoldings  float64           `json:"average_holdings"`
	TotalFee         float64           `json:"total_fee"`
	UnfilledBars     int               `json:"unfilled_bars"`
	Liquidations     int               `json:"liquidations"`
	FirstBarEnd      time.Time         `json:"first_bar_end,omitzero"`
	LastBarEnd       time.Time         `json:"last_bar_end,omitzero"`
	Factors          map[string]string `json:"factors,omitempty"`
	Limitations      []string          `json:"limitations"`
}

// accumulator 逐期累计绩效。
type accumulator struct {
	metrics      Metrics
	peak         float64
	turnoverSum  float64
	holdingsSum  float64
	periodsPerYr float64
	barDuration  time.Duration
}

func newAccumulator(initialEquity, periodsPerYear float64, barDuration time.Duration) *accumulator {
	return &accumulator{metrics: Metrics{InitialEquity: initialEquity, FinalEquity: initialEquity, SkipReasons: map[string]int{}}, peak: initialEquity, periodsPerYr: periodsPerYear, barDuration: barDuration}
}

// minAnnualizedSpan 是给出年化收益所需的最短区间：根数 × bar 时长不短于一周。
const minAnnualizedSpan = 7 * 24 * time.Hour

func (a *accumulator) add(barEnd time.Time, ok bool, reason string, outcome Outcome, holdings int) {
	m := &a.metrics
	if m.Bars == 0 {
		m.FirstBarEnd = barEnd
	}
	m.LastBarEnd = barEnd
	m.Bars++
	if ok {
		m.OKBars++
		a.turnoverSum += outcome.Turnover
	} else {
		m.SkippedBars++
		m.SkipReasons[reason]++
	}
	if len(outcome.Unfilled) > 0 {
		m.UnfilledBars++
	}
	m.Liquidations += len(outcome.Liquidated)
	m.TotalFee += outcome.Fee
	a.holdingsSum += float64(holdings)
	m.FinalEquity = outcome.EquityAfter
	if outcome.EquityAfter > a.peak {
		a.peak = outcome.EquityAfter
	}
	if a.peak > 0 {
		if drawdown := 1 - outcome.EquityAfter/a.peak; drawdown > m.MaxDrawdown {
			m.MaxDrawdown = drawdown
		}
	}
}

func (a *accumulator) finish() Metrics {
	m := a.metrics
	if m.InitialEquity > 0 {
		m.TotalReturn = m.FinalEquity/m.InitialEquity - 1
	}
	// 年化只在区间不短于一周时给出：更短的区间外推一年没有意义，还可能溢出为 +Inf 导致指标无法编码。
	if note := a.annualize(&m); note != "" {
		m.Limitations = append(append([]string(nil), m.Limitations...), note)
	}
	if m.OKBars > 0 {
		m.AverageTurnover = a.turnoverSum / float64(m.OKBars)
	}
	if m.Bars > 0 {
		m.AverageHoldings = a.holdingsSum / float64(m.Bars)
	}
	if len(m.SkipReasons) == 0 {
		m.SkipReasons = nil
	}
	return m
}

// annualize 计算年化收益；不给出时返回说明原因的局限性条目。
func (a *accumulator) annualize(m *Metrics) string {
	if m.Bars == 0 || a.periodsPerYr <= 0 {
		return ""
	}
	if time.Duration(m.Bars)*a.barDuration < minAnnualizedSpan {
		return "区间不足一周，不给出年化收益"
	}
	if 1+m.TotalReturn <= 0 {
		return "期末权益不为正，不给出年化收益"
	}
	annualized := math.Pow(1+m.TotalReturn, a.periodsPerYr/float64(m.Bars)) - 1
	if math.IsNaN(annualized) || math.IsInf(annualized, 0) {
		return "年化结果溢出，不给出年化收益"
	}
	m.AnnualizedReturn = &annualized
	return ""
}

// periodsPerYear 返回一年的 bar 数：A 股日线按 244 个交易日，其余按自然时间。
func periodsPerYear(calendar string, duration time.Duration) float64 {
	if strings.EqualFold(calendar, "cn_stock") {
		return 244
	}
	if duration <= 0 {
		return 0
	}
	return float64(365*24*time.Hour) / float64(duration)
}

// limitations 是每份回放报告都要标注的局限。
func limitations(liquidateAfter int, usesTags bool) []string {
	notes := []string{
		"研究回放：按当期 close 成交的理想化假设，与实盘在 bar_end 之后成交不同",
		"区间内历史行版本未验证：因子结果行不携带版本，只核对了回放开始时的因子定义指纹",
		"基础集合取每期有行的标的，与实时事件名单可能不同；数据集的全部标的绑定（含已停用）都参与读取",
		"手续费只按实际成交额计；不含滑点与盘口冲击",
		"持有标的连续缺价 " + itoa(liquidateAfter) + " 期后在 ok 期按最后价清算（研究假设）",
	}
	if usesTags {
		notes = append(notes, "标签只有当前成员关系：按标签选池存在幸存者偏差")
	}
	return notes
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}
