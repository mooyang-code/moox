package binance

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
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

// GetRecentTrades 获取现货最近成交。
func (api *SpotAPI) GetRecentTrades(ctx context.Context, req *exchange.TradeRequest) ([]*exchange.Trade, error) {
	return getRecentTrades(ctx, api.client, api.client.SpotDomain(), SpotTradesEndpoint, "SpotAPI", true, req)
}

// GetRecentTradesWithIPs uses the Collector DNS snapshot when available and
// falls back to the hostname resolver when every snapshot address fails.
func (api *SpotAPI) GetRecentTradesWithIPs(ctx context.Context, req *exchange.TradeRequest, ips []string) ([]*exchange.Trade, error) {
	return getRecentTradesWithIPs(ctx, api.client, api.client.SpotDomain(), ips, SpotTradesEndpoint, "SpotAPI", true, req)
}

// GetKline 获取现货K线数据
// API: GET https://api.binance.com/api/v3/klines
func (api *SpotAPI) GetKline(ctx context.Context, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	return api.GetKlineWithIPs(ctx, req, nil)
}

// GetKlineWithIPs uses the collector's DNS snapshot when one is available.
func (api *SpotAPI) GetKlineWithIPs(ctx context.Context, req *exchange.KlineRequest, ips []string) ([]*exchange.Kline, error) {
	return api.GetKlineWithDomainIPs(ctx, req, api.client.SpotDomain(), ips)
}

// GetKlineWithDomainIPs requests one explicitly selected official Spot
// endpoint. The collector uses this narrow method to try the configured
// endpoint order while keeping each endpoint's DNS snapshot separate.
func (api *SpotAPI) GetKlineWithDomainIPs(ctx context.Context, req *exchange.KlineRequest, domain string, ips []string) ([]*exchange.Kline, error) {
	if api == nil || api.client == nil || req == nil {
		return nil, fmt.Errorf("现货 K 线请求无效")
	}
	params := url.Values{}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		domain = api.client.SpotDomain()
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
			return api.client.GetDirectWithIPs(ctx, domain, ips, SpotKlineEndpoint, params, &rawKlines)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取现货K线失败: %w", err)
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

// GetExchangeInfo 获取现货交易所信息（交易规则和交易对）
// API: GET https://api.binance.com/api/v3/exchangeInfo
func (api *SpotAPI) GetExchangeInfo(ctx context.Context) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfo(ctx, nil)
}

func (api *SpotAPI) getExchangeInfo(ctx context.Context, query url.Values) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfoWithIPs(ctx, query, nil)
}

func (api *SpotAPI) GetExchangeInfoWithIPs(ctx context.Context, ips []string) ([]*exchange.SymbolInfo, error) {
	return api.getExchangeInfoWithIPs(ctx, nil, ips)
}

func (api *SpotAPI) getExchangeInfoWithIPs(ctx context.Context, query url.Values, ips []string) ([]*exchange.SymbolInfo, error) {
	var symbols []*exchange.SymbolInfo
	var total int
	domain := api.client.SpotDomain()

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirectStreamWithIPs(ctx, domain, ips, SpotExchangeInfoEndpoint, query, func(reader io.Reader) error {
				var decodeErr error
				total, symbols, decodeErr = decodeExchangeInfo(reader, func(raw *exchangeInfoSymbolRaw) bool {
					if raw.Status != "TRADING" {
						return false
					}
					return true
				})
				return decodeErr
			})
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取现货交易所信息失败: %w", err)
	}

	log.InfoContextf(ctx, "[SpotAPI] 获取ExchangeInfo成功，总计%d个交易对，活跃%d个",
		total, len(symbols))
	return symbols, nil
}
