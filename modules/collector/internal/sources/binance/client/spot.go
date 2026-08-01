package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
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

	// 发送请求（带重试，失败时切换IP）
	var rawKlines []CandleStick
	var triedIPs []string // 记录已尝试失败的IP列表

	err := retryBinance(ctx,
		func() error {
			// 获取下一个可用的IP（排除已失败的IP）
			currentIP := httpclient.GetNextAvailableIP(domain, triedIPs)

			// DNS proxy 记录可能尚未同步，允许降级为标准域名访问。
			if currentIP == "" {
				log.WarnContextf(ctx, "[SpotAPI] 无可用DNS优选IP，降级为域名直连, symbol=%s, interval=%s, 已尝试IP: %v",
					symbol, req.Interval, triedIPs)
			}

			// 使用指定IP发送请求
			err := api.client.GetWithIP(ctx, domain, SpotKlineEndpoint, params, &rawKlines, currentIP)
			if err != nil {
				if currentIP != "" {
					// 请求失败，记录这个IP
					triedIPs = append(triedIPs, currentIP)
					log.WarnContextf(ctx, "[SpotAPI] IP %s 请求失败，加入排除列表", currentIP)
				}
				return err
			}
			return nil
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
	var triedIPs []string
	domain := api.client.SpotDomain()

	err := retryBinance(ctx,
		func() error {
			currentIP := httpclient.GetNextAvailableIP(domain, triedIPs)
			if currentIP == "" {
				log.WarnContextf(ctx, "[SpotAPI] 无可用DNS优选IP获取ExchangeInfo，降级为域名直连, 已尝试IP: %v", triedIPs)
			}

			err := api.client.GetWithIPStream(ctx, domain, SpotExchangeInfoEndpoint, query, currentIP, func(reader io.Reader) error {
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
			if err != nil {
				if currentIP != "" {
					triedIPs = append(triedIPs, currentIP)
					log.WarnContextf(ctx, "[SpotAPI] IP %s 获取ExchangeInfo失败，加入排除列表", currentIP)
				}
				return err
			}
			return nil
		},
	)
	if err != nil {
		return nil, fmt.Errorf("获取现货交易所信息失败: %w", err)
	}

	log.InfoContextf(ctx, "[SpotAPI] 获取ExchangeInfo成功，总计%d个交易对，活跃%d个",
		total, len(symbols))
	return symbols, nil
}
