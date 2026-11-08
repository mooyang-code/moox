package binance

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

const (
	// fundingRatePath 是 U 本位合约的历史资金费率接口：公共接口，不带 symbol 时返回时间窗内全部合约的结算记录。
	fundingRatePath = "/fapi/v1/fundingRate"
	// fundingRatePageLimit 是单次最多返回的条数；时间窗内超过这个数时只返回从 startTime 起的一部分。
	fundingRatePageLimit = 1000
	// fundingRateChunk 是每次请求覆盖的最长时间：一个 8 小时结算点约 600 多个合约，4 小时内通常装得下；满页时再对半拆开重查。
	fundingRateChunk = 4 * time.Hour
)

type fundingRatePayload struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime int64  `json:"fundingTime"`
	MarkPrice   string `json:"markPrice"`
}

// SupportsFundingRates：只有 U 本位永续有资金费。
func (a *Adapter) SupportsFundingRates() bool {
	return a.config.Market() == exchange.MarketTypeSwap
}

// ListFundingRates 返回 [start, end] 内全部合约的资金费结算记录。按 4 小时分段请求，
// 某段满 1000 条说明可能被截断，对半拆开重查，不依赖返回顺序；同一毫秒就超过 1000 条时报错，不返回不完整的结果。
func (a *Adapter) ListFundingRates(ctx context.Context, start, end time.Time) ([]exchange.FundingRate, error) {
	if !a.SupportsFundingRates() {
		return nil, typedRejected("该市场没有资金费", nil)
	}
	if end.Before(start) {
		return nil, typedRejected("资金费率查询的结束时间早于开始时间", nil)
	}
	from, to := start.UnixMilli(), end.UnixMilli()
	chunk := fundingRateChunk.Milliseconds()
	var result []exchange.FundingRate
	for chunkStart := from; chunkStart <= to; chunkStart += chunk {
		rows, err := a.fundingRateRange(ctx, chunkStart, min(chunkStart+chunk-1, to))
		if err != nil {
			return nil, err
		}
		result = append(result, rows...)
	}
	return result, nil
}

func (a *Adapter) fundingRateRange(ctx context.Context, from, to int64) ([]exchange.FundingRate, error) {
	rows, err := a.fundingRatePage(ctx, from, to)
	if err != nil {
		return nil, err
	}
	if len(rows) < fundingRatePageLimit {
		return fundingRatesFromPayload(rows)
	}
	if to <= from {
		return nil, typedRejected("同一毫秒的资金费结算记录超过单页上限", errors.New(strconv.FormatInt(from, 10)))
	}
	middle := from + (to-from)/2
	left, err := a.fundingRateRange(ctx, from, middle)
	if err != nil {
		return nil, err
	}
	right, err := a.fundingRateRange(ctx, middle+1, to)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

func (a *Adapter) fundingRatePage(ctx context.Context, from, to int64) ([]fundingRatePayload, error) {
	raw, err := a.swap.Do(ctx, &httpclient.Request{
		Method: http.MethodGet,
		Path:   fundingRatePath,
		Query: url.Values{
			"startTime": []string{strconv.FormatInt(from, 10)},
			"endTime":   []string{strconv.FormatInt(to, 10)},
			"limit":     []string{strconv.Itoa(fundingRatePageLimit)},
		},
	})
	if err != nil {
		return nil, classifyAPIError(err, raw)
	}
	var rows []fundingRatePayload
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, typedRejected("解析资金费率", err)
	}
	return rows, nil
}

func fundingRatesFromPayload(rows []fundingRatePayload) ([]exchange.FundingRate, error) {
	result := make([]exchange.FundingRate, 0, len(rows))
	for _, row := range rows {
		if row.Symbol == "" || row.FundingTime <= 0 {
			return nil, typedRejected("资金费率记录缺少合约或结算时刻", errors.New(row.Symbol))
		}
		rate, err := shared.ParseDecimal(row.FundingRate)
		if err != nil {
			return nil, typedRejected("资金费率不是数字", err)
		}
		// 老记录的 markPrice 是空串，按零返回，由调用方决定跳过。
		mark, err := decimalOrZero(row.MarkPrice)
		if err != nil {
			return nil, err
		}
		result = append(result, exchange.FundingRate{
			ExchangeSymbol: row.Symbol, FundingTime: millis(row.FundingTime), Rate: rate, MarkPrice: mark,
		})
	}
	return result, nil
}
