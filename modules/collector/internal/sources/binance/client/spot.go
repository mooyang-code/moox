package binance

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-go/log"
)

// SpotAPI 现货 API
type SpotAPI struct {
	client *Client
}

// NewSpotAPI 创建现货 API
func NewSpotAPI(client *Client) *SpotAPI {
	return &SpotAPI{client: client}
}

// GetKline 获取现货K线数据
// API: GET https://api.binance.com/api/v3/klines
func (api *SpotAPI) GetKline(ctx context.Context, req *KlineRequest) ([]*Kline, error) {
	return api.GetKlineWithDomain(ctx, req, api.client.SpotDomain())
}

// GetKlineWithDomain 按显式指定的官方现货端点获取 K 线。
func (api *SpotAPI) GetKlineWithDomain(ctx context.Context, req *KlineRequest, domain string) ([]*Kline, error) {
	if api == nil || api.client == nil || req == nil {
		return nil, fmt.Errorf("现货 K 线请求无效")
	}
	params := url.Values{}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = api.client.SpotDomain()
	}

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

	var rawKlines []CandleStick
	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirect(ctx, domain, SpotKlineEndpoint, params, &rawKlines)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取现货K线失败: %w", err)
	}

	klines := make([]*Kline, 0, len(rawKlines))
	for _, raw := range rawKlines {
		kline, err := raw.ToKline()
		if err != nil {
			return nil, fmt.Errorf("转换K线数据失败: %w", err)
		}
		klines = append(klines, kline)
	}
	return klines, nil
}

// GetExchangeInfo 获取现货交易所信息（交易规则和交易对）
// API: GET https://api.binance.com/api/v3/exchangeInfo
func (api *SpotAPI) GetExchangeInfo(ctx context.Context) ([]*SymbolInfo, error) {
	return api.getExchangeInfoWithDomain(ctx, api.client.SpotDomain(), nil)
}

// GetExchangeInfoWithDomain 按显式指定的官方现货端点获取交易对元数据。
func (api *SpotAPI) GetExchangeInfoWithDomain(ctx context.Context, domain string) ([]*SymbolInfo, error) {
	return api.getExchangeInfoWithDomain(ctx, domain, nil)
}

func (api *SpotAPI) getExchangeInfoWithDomain(ctx context.Context, domain string, query url.Values) ([]*SymbolInfo, error) {
	var symbols []*SymbolInfo
	var total int
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = api.client.SpotDomain()
	}

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirectStream(ctx, domain, SpotExchangeInfoEndpoint, query, func(reader io.Reader) error {
				var decodeErr error
				total, symbols, decodeErr = decodeExchangeInfo(reader, func(*exchangeInfoSymbolRaw) bool { return true })
				return decodeErr
			})
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取现货交易所信息失败: %w", err)
	}

	log.InfoContextf(ctx, "[SpotAPI] 获取ExchangeInfo成功，总计%d个交易对，正常交易%d个",
		total, countTrading(symbols))
	return symbols, nil
}
