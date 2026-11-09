package marketfetch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

// errFundingUnsupported 表示写入目标选了 funding_rate，但这个行情源没有资金费率能力（任务校验本应拦下，属于配置错误）。
var errFundingUnsupported = errors.New("行情源不具备资金费率能力")

// fundingLookup 返回 [start, end) 内的资金费率之和；取不到时返回 false。
type fundingLookup func(start, end time.Time) (float64, bool)

// startFunding 异步取一个合约在 [start, end) 内的资金费：实时批次读进程缓存（没就绪的小时槽按需补取），
// 其余（补采、追赶、手动）按合约直接查区间。返回的函数等待结果。
func (p *KlinePipeline) startFunding(ctx context.Context, req Request, symbol string, start, end time.Time) func() (fundingLookup, error) {
	type outcome struct {
		lookup fundingLookup
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		lookup, err := p.loadFunding(ctx, req, strings.ToUpper(strings.TrimSpace(symbol)), start, end)
		done <- outcome{lookup: lookup, err: err}
	}()
	return func() (fundingLookup, error) {
		result := <-done
		return result.lookup, result.err
	}
}

func (p *KlinePipeline) loadFunding(ctx context.Context, req Request, symbol string, start, end time.Time) (fundingLookup, error) {
	if p.Funding == nil {
		return nil, errFundingUnsupported
	}
	if !end.After(start) {
		return nil, fmt.Errorf("资金费率查询区间为空")
	}
	if req.BatchKind == domain.BatchKindRealtime {
		if err := p.Funding.Await(ctx, start, end); err != nil {
			return nil, fmt.Errorf("等待资金费率小时槽 [%s, %s) 就绪：%w", start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), err)
		}
		return func(barStart, barEnd time.Time) (float64, bool) { return p.Funding.Sum(symbol, barStart, barEnd) }, nil
	}
	rates, err := p.Funding.Query(ctx, symbol, start, end)
	if err != nil {
		return nil, fmt.Errorf("查询 %s 资金费率 [%s, %s)：%w", symbol, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), err)
	}
	return func(barStart, barEnd time.Time) (float64, bool) {
		if barStart.Before(start) || barEnd.After(end) {
			return 0, false
		}
		return marketdata.SumFundingRates(rates, barStart, barEnd)[symbol], true
	}, nil
}

// fillFundingRates 等资金费结果并填进每一根；取不到的根留空，由 expandTargetRows 按目标判定失败。
func fillFundingRates(bars []marketdata.NormalizedKline, result func() (fundingLookup, error)) error {
	lookup, err := result()
	if err != nil {
		return err
	}
	for index := range bars {
		if value, ok := lookup(bars[index].BarStart, bars[index].BarEnd); ok {
			bars[index].FundingRate = &value
		}
	}
	return nil
}

// klineSpan 返回一组 K 线覆盖的 [最早开盘, 最晚收盘)。
func klineSpan(bars []marketdata.NormalizedKline) (time.Time, time.Time) {
	var start, end time.Time
	for _, bar := range bars {
		if start.IsZero() || bar.BarStart.Before(start) {
			start = bar.BarStart
		}
		if bar.BarEnd.After(end) {
			end = bar.BarEnd
		}
	}
	return start, end
}

// fundingFailure 是资金费取不到时这一项的结果：可重试的 provider_error（行情源不具备能力时是不重试的配置错误）。
// 不能把缺的几根丢掉照常报成功，否则补采复查会把它们登记成缺失、之后不再补。
func fundingFailure(item domain.CollectionItem, cause error) domain.ItemResult {
	if errors.Is(cause, errFundingUnsupported) {
		return failureResult(item, domain.ItemOutcomeInvalid, "funding_rate", cause)
	}
	if cause == nil {
		cause = errors.New("返回的 K 线超出资金费率查询区间")
	}
	return failureResult(item, domain.ItemOutcomeProviderError, "funding_rate", fmt.Errorf("资金费率缺失：%w", cause))
}
