package binance

import (
	"context"
	"encoding/json"
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

// GetKline 获取现货K线数据
// API: GET https://api.binance.com/api/v3/klines
func (api *SpotAPI) GetKline(ctx context.Context, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	params := url.Values{}
	domain := api.client.SpotDomain()

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

	// Retry the domain request. Each SCF invocation uses normal DNS directly.
	var rawKlines []CandleStick

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirect(ctx, domain, SpotKlineEndpoint, params, &rawKlines)
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
	return api.getExchangeInfo(ctx, nil, nil)
}

// GetExchangeInfoForSymbols asks Binance for only the configured symbols. The
// exchangeInfo endpoint accepts a JSON-encoded symbols array; using it keeps
// manual SymbolTask invocations small enough for the 64MB SCF limit.
func (api *SpotAPI) GetExchangeInfoForSymbols(ctx context.Context, symbols []string) ([]*exchange.SymbolInfo, error) {
	normalized := normalizeExchangeInfoSymbols(symbols)
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, fmt.Errorf("编码ExchangeInfo symbols失败: %w", err)
	}
	return api.getExchangeInfo(ctx, url.Values{"symbols": {string(encoded)}}, normalized)
}

func (api *SpotAPI) getExchangeInfo(ctx context.Context, query url.Values, allowlist []string) ([]*exchange.SymbolInfo, error) {
	allowed := make(map[string]struct{}, len(allowlist))
	for _, symbol := range allowlist {
		allowed[symbol] = struct{}{}
	}
	var symbols []*exchange.SymbolInfo
	var total int
	domain := api.client.SpotDomain()

	err := retryBinance(ctx,
		func() error {
			return api.client.GetDirectStream(ctx, domain, SpotExchangeInfoEndpoint, query, func(reader io.Reader) error {
				var decodeErr error
				total, symbols, decodeErr = decodeExchangeInfo(reader, func(raw *exchangeInfoSymbolRaw) bool {
					if raw.Status != "TRADING" {
						return false
					}
					if len(allowed) == 0 {
						return true
					}
					_, ok := allowed[strings.ToUpper(raw.Symbol)]
					return ok
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
