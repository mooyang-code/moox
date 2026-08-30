package binance

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/market"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

// 产品类型常量
const (
	InstTypeSPOT = "SPOT" // 现货
	InstTypeSWAP = "SWAP" // 永续合约
)

// KlineCollector owns Binance protocol access for the typed MarketProvider.
// Storage writes are performed by the common KlinePipeline, never here.
type KlineCollector struct {
	client         *binanceapi.Client
	spotAPI        *binanceapi.SpotAPI
	swapAPI        *binanceapi.SwapAPI
	fetchKlinePage func(context.Context, *sources.CollectParams, *exchange.KlineRequest) ([]*exchange.Kline, error)
}

// NewKlineCollector returns a configured Binance K-line collector for bounded
// short-lived invocations. It intentionally does not attach a Storage writer.
func NewKlineCollector() *KlineCollector {
	client := newConfiguredClient()
	return &KlineCollector{client: client, spotAPI: binanceapi.NewSpotAPI(client), swapAPI: binanceapi.NewSwapAPI(client)}
}

// fetchKlines 从币安 API 获取一页 K 线数据。
func (c *KlineCollector) fetchKlines(ctx context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	if c.fetchKlinePage != nil {
		return c.fetchKlinePage(ctx, params, req)
	}
	return c.fetchExchangeKlines(ctx, params, req)
}

// fetchKlinesOnce deliberately performs one physical provider request. The
// common RouterSession owns the two-provider fallback budget and FeedGuard
// owns the per-IP token/concurrency policy; retrying here would bypass both.
func (c *KlineCollector) fetchKlinesOnce(ctx context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	return c.fetchKlines(binanceapi.SingleAttempt(ctx), params, req)
}

func (c *KlineCollector) fetchExchangeKlines(ctx context.Context, params *sources.CollectParams, req *exchange.KlineRequest) ([]*exchange.Kline, error) {
	switch params.InstType {
	case InstTypeSPOT:
		return c.spotAPI.GetKlineWithIPs(ctx, req, params.DNSIPs(c.client.SpotDomain()))
	case InstTypeSWAP:
		return c.swapAPI.GetKlineWithIPs(ctx, req, params.DNSIPs(c.client.SwapDomain()))
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

func convertExchangeKlines(exchangeKlines []*exchange.Kline, symbol string, interval string) []*market.Kline {
	klines := make([]*market.Kline, 0, len(exchangeKlines))
	for _, ek := range exchangeKlines {
		kline := market.NewKline("binance", symbol, interval)
		kline.OpenTime = ek.OpenTime
		kline.CloseTime = ek.CloseTime
		kline.Open = ek.Open
		kline.High = ek.High
		kline.Low = ek.Low
		kline.Close = ek.Close
		kline.Volume = ek.Volume
		kline.QuoteVolume = ek.QuoteVolume
		kline.TradeCount = ek.TradeCount
		klines = append(klines, kline)
	}
	return klines
}

func filterClosedKlines(klines []*market.Kline, now time.Time) ([]*market.Kline, int) {
	closed := make([]*market.Kline, 0, len(klines))
	skipped := 0
	for _, kline := range klines {
		if isKlineClosed(kline, now) {
			closed = append(closed, kline)
			continue
		}
		skipped++
	}
	return closed, skipped
}

func isKlineClosed(kline *market.Kline, now time.Time) bool {
	if kline == nil || kline.CloseTime.IsZero() {
		return false
	}
	return now.After(kline.CloseTime)
}
