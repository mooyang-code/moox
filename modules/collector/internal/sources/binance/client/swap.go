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
	return api.getExchangeInfoWithIPs(ctx, nil, ips)
}

func (api *SwapAPI) getExchangeInfoWithIPs(ctx context.Context, query url.Values, ips []string) ([]*exchange.SymbolInfo, error) {
	var symbols []*exchange.SymbolInfo
	var total int
	domain := api.client.SwapDomain()

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
