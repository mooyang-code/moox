package binance

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/dnsproxy"
	"github.com/mooyang-code/moox/modules/collector/internal/exchange"
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

	err := retry.Do(
		func() error {
			// 获取下一个可用的IP（排除已失败的IP）
			currentIP := dnsproxy.GetNextAvailableIP(domain, triedIPs)

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
		retry.Attempts(3),
		retry.Delay(1*time.Second),
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "[SpotAPI] 获取K线重试 #%d, symbol=%s, interval=%s, err=%v",
				n+1, symbol, req.Interval, err)
		}),
		retry.Context(ctx),
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
	var result ExchangeInfoResponse
	var triedIPs []string
	domain := api.client.SpotDomain()

	err := retry.Do(
		func() error {
			currentIP := dnsproxy.GetNextAvailableIP(domain, triedIPs)
			if currentIP == "" {
				log.WarnContextf(ctx, "[SpotAPI] 无可用DNS优选IP获取ExchangeInfo，降级为域名直连, 已尝试IP: %v", triedIPs)
			}

			err := api.client.GetWithIP(ctx, domain, SpotExchangeInfoEndpoint, nil, &result, currentIP)
			if err != nil {
				if currentIP != "" {
					triedIPs = append(triedIPs, currentIP)
					log.WarnContextf(ctx, "[SpotAPI] IP %s 获取ExchangeInfo失败，加入排除列表", currentIP)
				}
				return err
			}
			return nil
		},
		retry.Attempts(3),
		retry.Delay(1*time.Second),
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "[SpotAPI] 获取ExchangeInfo重试 #%d, err=%v", n+1, err)
		}),
		retry.Context(ctx),
	)
	if err != nil {
		return nil, fmt.Errorf("获取现货交易所信息失败: %w", err)
	}

	// 转换为通用格式
	symbols := make([]*exchange.SymbolInfo, 0, len(result.Symbols))
	for _, raw := range result.Symbols {
		// 只包含状态为 TRADING 的交易对
		if raw.Status == "TRADING" {
			symbols = append(symbols, raw.ToSymbolInfo())
		}
	}

	log.InfoContextf(ctx, "[SpotAPI] 获取ExchangeInfo成功，总计%d个交易对，活跃%d个",
		len(result.Symbols), len(symbols))
	return symbols, nil
}
