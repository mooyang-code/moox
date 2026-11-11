package marketfetch

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// FundingFieldSpecs 是有资金费率能力的行情源作为附加字段来源提供的字段：每根写结算时刻落在 [开始, 收盘) 内的费率之和。
// 只支持 1h、1d：1m～30m 收盘时结算记录可能还没公布；1w、1M 推送不支持，一次要取的数据也太多。
// fill=zero：没有结算的根写的是 0，因子引擎读列时没有这个属性就沿用上一根，会重复计费。
func FundingFieldSpecs() []marketdata.FieldSpec {
	return []marketdata.FieldSpec{{ID: domain.FieldFundingRate, Name: "资金费率", Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1h", "1d"}}}
}

// FetchFields 返回 [Start, End) 内每根 K 线的资金费率之和（本根没有结算为 0）。
// 实时批次读进程缓存，没就绪的小时槽按需补取，等不到就报错；补采、追赶、缺口修复按合约直接查区间。
// 区间里放不下完整一根（收盘晚于 End）时报错：不能把查不全的根当成“没有值”写成空列。
func (c *FundingCache) FetchFields(ctx context.Context, req marketdata.FieldRequest) (map[time.Time]map[string]marketdata.FieldValue, error) {
	frequency, err := marketdata.ParseFrequency(req.Frequency)
	if err != nil {
		return nil, err
	}
	step := frequency.Duration()
	start, end := req.Start.UTC(), req.End.UTC()
	if step <= 0 || !end.After(start) {
		return nil, fmt.Errorf("资金费率查询区间为空")
	}
	symbol := strings.ToUpper(strings.TrimSpace(req.ProviderSymbol))
	span := fmt.Sprintf("[%s, %s)", start.Format(time.RFC3339), end.Format(time.RFC3339))
	var sum func(barStart, barEnd time.Time) (float64, bool)
	if req.Realtime {
		if err := c.Await(ctx, start, end); err != nil {
			return nil, fmt.Errorf("等待资金费率小时槽 %s 就绪：%w", span, err)
		}
		sum = func(barStart, barEnd time.Time) (float64, bool) { return c.Sum(symbol, barStart, barEnd) }
	} else {
		rates, err := c.Query(ctx, symbol, start, end)
		if err != nil {
			return nil, fmt.Errorf("查询 %s 资金费率 %s：%w", symbol, span, err)
		}
		sum = func(barStart, barEnd time.Time) (float64, bool) {
			return marketdata.SumFundingRates(rates, barStart, barEnd)[symbol], true
		}
	}
	values := make(map[time.Time]map[string]marketdata.FieldValue)
	first := start.Truncate(step)
	if first.Before(start) {
		first = first.Add(step)
	}
	for barStart := first; barStart.Before(end); barStart = barStart.Add(step) {
		barEnd := barStart.Add(step)
		value, ok := sum(barStart, barEnd)
		if barEnd.After(end) || !ok {
			return nil, fmt.Errorf("K 线 [%s, %s) 超出资金费率查询区间 %s", barStart.Format(time.RFC3339), barEnd.Format(time.RFC3339), span)
		}
		values[barStart] = map[string]marketdata.FieldValue{domain.FieldFundingRate: marketdata.DoubleField(value)}
	}
	return values, nil
}
