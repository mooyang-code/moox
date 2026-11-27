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
	return api.GetKlineWithIPs(ctx, req, nil)
}

// GetKlineWithIPs uses the collector's DNS snapshot when one is available.
func (api *SpotAPI) GetKlineWithIPs(ctx context.Context, req *KlineRequest, ips []string) ([]*Kline, error) {
	return api.GetKlineWithDomainIPs(ctx, req, api.client.SpotDomain(), ips)
}

// GetKlineWithDomainIPs requests one explicitly selected official Spot endpoint.
func (api *SpotAPI) GetKlineWithDomainIPs(ctx context.Context, req *KlineRequest, domain string, ips []string) ([]*Kline, error) {
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
			return api.client.GetDirectWithIPs(ctx, domain, ips, SpotKlineEndpoint, params, &rawKlines)
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
	return api.getExchangeInfo(ctx, nil)
}

func (api *SpotAPI) getExchangeInfo(ctx context.Context, query url.Values) ([]*SymbolInfo, error) {
	return api.getExchangeInfoWithIPs(ctx, query, nil)
}

func (api *SpotAPI) GetExchangeInfoWithIPs(ctx context.Context, ips []string) ([]*SymbolInfo, error) {
	return api.GetExchangeInfoWithDomainIPs(ctx, api.client.SpotDomain(), ips)
}

// GetExchangeInfoWithDomainIPs requests Spot exchange metadata from the explicitly selected official endpoint.
func (api *SpotAPI) GetExchangeInfoWithDomainIPs(ctx context.Context, domain string, ips []string) ([]*SymbolInfo, error) {
	return api.getExchangeInfoWithDomainIPs(ctx, domain, nil, ips)
}

func (api *SpotAPI) getExchangeInfoWithIPs(ctx context.Context, query url.Values, ips []string) ([]*SymbolInfo, error) {
	return api.getExchangeInfoWithDomainIPs(ctx, api.client.SpotDomain(), query, ips)
}

func (api *SpotAPI) getExchangeInfoWithDomainIPs(ctx context.Context, domain string, query url.Values, ips []string) ([]*SymbolInfo, error) {
	var symbols []*SymbolInfo
	var total int
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = api.client.SpotDomain()
	}

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirectStreamWithIPs(ctx, domain, ips, SpotExchangeInfoEndpoint, query, func(reader io.Reader) error {
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
