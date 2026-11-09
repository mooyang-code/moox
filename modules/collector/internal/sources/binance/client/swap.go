package binance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
	"trpc.group/trpc-go/trpc-go/log"
)

// SwapAPI 永续合约 API
type SwapAPI struct {
	client *Client
}

// NewSwapAPI 创建永续合约 API
func NewSwapAPI(client *Client) *SwapAPI {
	return &SwapAPI{client: client}
}

// GetRecentTrades 获取永续合约最近成交。
func (api *SwapAPI) GetRecentTrades(ctx context.Context, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
	return getRecentTrades(ctx, api.client, api.client.SwapDomain(), SwapTradesEndpoint, "SwapAPI", true, req)
}

// GetRecentTradesWithIPs uses the Collector DNS snapshot when available and
// falls back to the hostname resolver when every snapshot address fails.
func (api *SwapAPI) GetRecentTradesWithIPs(ctx context.Context, req *exchange.TradeRequest, ips []string) ([]*exchange.Trade, error) {
	return getRecentTradesWithIPs(ctx, api.client, api.client.SwapDomain(), ips, SwapTradesEndpoint, "SwapAPI", true, req)
}

// GetKline 获取永续合约K线数据
// API: GET https://fapi.binance.com/fapi/v1/klines
func (api *SwapAPI) GetKline(ctx context.Context, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	return api.GetKlineWithIPs(ctx, req, nil)
}

func (api *SwapAPI) GetKlineWithIPs(ctx context.Context, req *exchange.KlineRequest, ips []string) ([]*exchange.Kline, error) {
	return api.GetKlineWithDomainIPs(ctx, req, api.client.SwapDomain(), ips)
}

func (api *SwapAPI) GetKlineWithDomainIPs(ctx context.Context, req *exchange.KlineRequest, domain string, ips []string) ([]*exchange.Kline, error) {
	if api == nil || api.client == nil || req == nil {
		return nil, fmt.Errorf("永续合约 K 线请求无效")
	}
	params := url.Values{}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = api.client.SwapDomain()
	}

	// 转换交易对格式
	symbol := FormatSymbol(req.Symbol)
	params.Set("symbol", symbol)
	params.Set("interval", req.Interval)

	if req.Limit > 0 {
		params.Set("limit", strconv.Itoa(req.Limit))
	}

	if !req.StartTime.IsZero() {
		params.Set("startTime", strconv.FormatInt(req.StartTime.UnixMilli(), 10))
	}

	if !req.EndTime.IsZero() {
		params.Set("endTime", strconv.FormatInt(req.EndTime.UnixMilli(), 10))
	}

	// GetDirectWithIPs falls back to hostname DNS after the supplied addresses.
	var rawKlines []CandleStick

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirectWithIPs(ctx, domain, ips, SwapKlineEndpoint, params, &rawKlines)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取永续合约K线失败: %w", err)
	}

	// 转换为通用格式
	klines := make([]*exchange.Kline, 0, len(rawKlines))
	for _, raw := range rawKlines {
		kline, err := raw.ToKline()
		if err != nil {
			return nil, fmt.Errorf("转换K线数据失败: %w", err)
		}
		klines = append(klines, kline)
	}

	return klines, nil
}

// GetExchangeInfo 获取永续合约交易所信息（交易规则和交易对）
// API: GET https://fapi.binance.com/fapi/v1/exchangeInfo
func (api *SwapAPI) GetExchangeInfo(ctx context.Context) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfo(ctx, nil)
}

func (api *SwapAPI) getExchangeInfo(ctx context.Context, query url.Values) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfoWithIPs(ctx, query, nil)
}

func (api *SwapAPI) GetExchangeInfoWithIPs(ctx context.Context, ips []string) ([]*exchange.SymbolInfo, error) {
	return api.GetExchangeInfoWithDomainIPs(ctx, api.client.SwapDomain(), ips)
}

// GetExchangeInfoWithDomainIPs requests swap exchange metadata from the
// explicitly selected official endpoint. The caller owns endpoint fallback so
// each configured domain can use its own DNS snapshot.
func (api *SwapAPI) GetExchangeInfoWithDomainIPs(ctx context.Context, domain string, ips []string) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfoWithDomainIPs(ctx, domain, nil, ips)
}

func (api *SwapAPI) getExchangeInfoWithIPs(ctx context.Context, query url.Values, ips []string) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfoWithDomainIPs(ctx, api.client.SwapDomain(), query, ips)
}

func (api *SwapAPI) getExchangeInfoWithDomainIPs(ctx context.Context, domain string, query url.Values, ips []string) ([]*exchange.SymbolInfo, error) {
	var symbols []*exchange.SymbolInfo
	var total int
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = api.client.SwapDomain()
	}

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirectStreamWithIPs(ctx, domain, ips, SwapExchangeInfoEndpoint, query, func(reader io.Reader) error {
				var decodeErr error
				total, symbols, decodeErr = decodeExchangeInfo(reader, func(raw *exchangeInfoSymbolRaw) bool {
					if raw.Status != "TRADING" || raw.ContractType != "PERPETUAL" {
						return false
					}
					return true
				})
				return decodeErr
			})
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取永续合约交易所信息失败: %w", err)
	}

	log.InfoContextf(ctx, "[SwapAPI] 获取ExchangeInfo成功，总计%d个交易对，活跃永续合约%d个",
		total, len(symbols))
	return symbols, nil
}

const (
	// FundingRatePageLimit 是资金费接口单次最多返回的条数。
	FundingRatePageLimit = 1000
	// fundingRateChunk 是不带 symbol 查询时每次请求覆盖的最长时间：一个 8 小时结算点约 600 多个合约，4 小时内通常装得下。
	fundingRateChunk = 4 * time.Hour
)

// FundingRateRecord 是一条资金费结算记录（接口原文）。
type FundingRateRecord struct {
	Symbol      string `json:"symbol"`
	FundingRate string `json:"fundingRate"`
	FundingTime int64  `json:"fundingTime"`
}

// FundingPacer 在每次资金费请求前等待配额，并在请求结束后拿到结果（用于 429/418 退避）。
type FundingPacer interface {
	Wait(ctx context.Context) error
	Done(err error)
}

// ErrFundingPageOverflow 表示同一毫秒内的结算记录就超过了单页上限，无法拆分。
var ErrFundingPageOverflow = errors.New("同一毫秒的资金费结算记录超过单页上限")

// FundingRates 返回结算时刻落在 [start, end) 内的资金费记录（接口原文，不保证顺序）。
//   - 不带 symbol：按 4 小时分段查询全部合约；某段满页说明可能被截断，对半拆开重查，不依赖返回顺序；
//   - 带 symbol：按时间往后翻页，直到返回不足一页（满页时总要再翻一页确认）。
//
// 每次请求前经 pacer 等待配额；请求只发一次，不在这里重试，失败交给调用方。
func (api *SwapAPI) FundingRates(ctx context.Context, pacer FundingPacer, symbol string, start, end time.Time) ([]FundingRateRecord, error) {
	if api == nil || api.client == nil {
		return nil, fmt.Errorf("永续合约资金费请求无效")
	}
	if !end.After(start) {
		return nil, nil
	}
	symbol = FormatSymbol(strings.TrimSpace(symbol))
	from, to := start.UnixMilli(), end.UnixMilli()
	var records []FundingRateRecord
	var err error
	if symbol != "" {
		records, err = api.fundingRatesOfSymbol(ctx, pacer, symbol, from, to)
	} else {
		chunk := fundingRateChunk.Milliseconds()
		for chunkStart := from; chunkStart < to && err == nil; chunkStart += chunk {
			var rows []FundingRateRecord
			rows, err = api.fundingRateRange(ctx, pacer, chunkStart, min(chunkStart+chunk, to))
			records = append(records, rows...)
		}
	}
	if err != nil {
		return nil, err
	}
	// 接口的 endTime 含端点，这里再按左闭右开过滤一次。
	kept := records[:0]
	for _, record := range records {
		if record.FundingTime >= from && record.FundingTime < to {
			kept = append(kept, record)
		}
	}
	return kept, nil
}

// fundingRateRange 查 [from, to) 毫秒内的全部合约；满页时拆成 [from, mid) 与 [mid, to) 重查。
func (api *SwapAPI) fundingRateRange(ctx context.Context, pacer FundingPacer, from, to int64) ([]FundingRateRecord, error) {
	rows, err := api.fundingRatePage(ctx, pacer, "", from, to)
	if err != nil {
		return nil, err
	}
	if len(rows) < FundingRatePageLimit {
		return rows, nil
	}
	if to-from <= 1 {
		return nil, fmt.Errorf("%w：%d", ErrFundingPageOverflow, from)
	}
	middle := from + (to-from)/2
	left, err := api.fundingRateRange(ctx, pacer, from, middle)
	if err != nil {
		return nil, err
	}
	right, err := api.fundingRateRange(ctx, pacer, middle, to)
	if err != nil {
		return nil, err
	}
	return append(left, right...), nil
}

// fundingRatesOfSymbol 按时间往后翻页查一个合约在 [from, to) 毫秒内的记录。
func (api *SwapAPI) fundingRatesOfSymbol(ctx context.Context, pacer FundingPacer, symbol string, from, to int64) ([]FundingRateRecord, error) {
	var records []FundingRateRecord
	for cursor := from; cursor < to; {
		rows, err := api.fundingRatePage(ctx, pacer, symbol, cursor, to)
		if err != nil {
			return nil, err
		}
		records = append(records, rows...)
		if len(rows) < FundingRatePageLimit {
			return records, nil
		}
		latest := cursor
		for _, row := range rows {
			latest = max(latest, row.FundingTime)
		}
		cursor = latest + 1
	}
	return records, nil
}

// fundingRatePage 请求一页：[from, to) 换成接口含端点的 [startTime, endTime]。
func (api *SwapAPI) fundingRatePage(ctx context.Context, pacer FundingPacer, symbol string, from, to int64) ([]FundingRateRecord, error) {
	if pacer != nil {
		if err := pacer.Wait(ctx); err != nil {
			return nil, err
		}
	}
	params := url.Values{}
	if symbol != "" {
		params.Set("symbol", symbol)
	}
	params.Set("startTime", strconv.FormatInt(from, 10))
	params.Set("endTime", strconv.FormatInt(to-1, 10))
	params.Set("limit", strconv.Itoa(FundingRatePageLimit))
	var rows []FundingRateRecord
	err := api.client.GetDirectWithIPs(ctx, api.client.SwapDomain(), nil, SwapFundingRateEndpoint, params, &rows)
	if pacer != nil {
		pacer.Done(err)
	}
	if err != nil {
		return nil, fmt.Errorf("获取永续合约资金费率失败（symbol=%s 区间=[%d,%d)）: %w", symbol, from, to, err)
	}
	return rows, nil
}
