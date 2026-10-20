package binance

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
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
func NewKlineCollector(clients ...*binanceapi.Client) *KlineCollector {
	client := newConfiguredClient()
	if len(clients) > 0 && clients[0] != nil {
		client = clients[0]
	}
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
		return tryEndpoints(ctx, c.client.SpotDomains(), func(attemptCtx context.Context, domain string) ([]*exchange.Kline, error) {
			return c.spotAPI.GetKlineWithDomainIPs(attemptCtx, req, domain, params.DNSIPs(domain))
		})
	case InstTypeSWAP:
		return tryEndpoints(ctx, c.client.SwapDomains(), func(attemptCtx context.Context, domain string) ([]*exchange.Kline, error) {
			return c.swapAPI.GetKlineWithDomainIPs(attemptCtx, req, domain, params.DNSIPs(domain))
		})
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

// tryEndpoints 按端点顺序请求，前一个失败才换下一个。上下文带对冲延迟时（httpclient.WithHedgeDelay），
// 某个端点的请求超过延迟还没有结果，就并发再发一个（有下一个端点用下一个，只有一个端点就再发一次同一个），先成功的为准；
// 前一个端点在延迟之前就失败时仍是普通的顺序换端点，不对冲。
func tryEndpoints(ctx context.Context, domains []string, call func(context.Context, string) ([]*exchange.Kline, error)) ([]*exchange.Kline, error) {
	delay := httpclient.HedgeDelayFrom(ctx)
	var lastErr error
	for index := 0; index < len(domains); index++ {
		if err := defaultWeightGovernor.Admit(ctx, domains[index]); err != nil {
			return nil, err
		}
		klines, consumed, err := attemptEndpoint(ctx, domains, index, delay, call)
		if err == nil {
			return klines, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
		index += consumed
	}
	return nil, lastErr
}

type endpointResult struct {
	klines []*exchange.Kline
	err    error
	hedge  bool
}

// attemptEndpoint 请求 domains[index]；consumed 是对冲占用的下一个端点个数（0 或 1），调用方据此跳过它。
func attemptEndpoint(ctx context.Context, domains []string, index int, delay time.Duration, call func(context.Context, string) ([]*exchange.Kline, error)) (klines []*exchange.Kline, consumed int, err error) {
	primaryCtx, cancelPrimary := endpointAttemptContext(ctx, len(domains)-index)
	defer cancelPrimary()
	if delay <= 0 {
		klines, err = call(primaryCtx, domains[index])
		return klines, 0, err
	}
	results := make(chan endpointResult, 2)
	go func() {
		klines, err := call(primaryCtx, domains[index])
		results <- endpointResult{klines: klines, err: err}
	}()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	trace := httpclient.TraceFromContext(ctx)
	pending, hedged := 1, false
	var lastErr error
	for pending > 0 {
		select {
		case result := <-results:
			pending--
			if result.err == nil {
				if result.hedge {
					trace.NoteHedge(false, true)
				}
				return result.klines, consumed, nil
			}
			lastErr = result.err
			if !hedged {
				// 第一次请求在对冲延迟之前就失败了：交给普通的顺序换端点。
				return nil, 0, lastErr
			}
		case <-timer.C:
			if hedged {
				continue
			}
			hedged = true
			hedgeIndex := index
			if index+1 < len(domains) {
				hedgeIndex = index + 1
			}
			if err := defaultWeightGovernor.Admit(ctx, domains[hedgeIndex]); err != nil {
				// 对冲请求不值得顶着频控发出：按没有对冲继续等第一次请求。
				continue
			}
			consumed = hedgeIndex - index
			hedgeCtx, cancelHedge := endpointAttemptContext(ctx, len(domains)-hedgeIndex)
			defer cancelHedge()
			trace.NoteHedge(true, false)
			pending++
			go func() {
				klines, err := call(hedgeCtx, domains[hedgeIndex])
				results <- endpointResult{klines: klines, err: err, hedge: true}
			}()
		case <-ctx.Done():
			return nil, consumed, ctx.Err()
		}
	}
	return nil, consumed, lastErr
}

func endpointAttemptContext(parent context.Context, remainingEndpoints int) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if remainingEndpoints <= 1 {
		return context.WithCancel(parent)
	}
	deadline, ok := parent.Deadline()
	if !ok {
		return context.WithCancel(parent)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithDeadline(parent, deadline)
	}
	// 每个端点至少有一段能容纳行情源延迟尖峰的时间：把剩余预算平均切给全部端点，
	// 前几个端点只能拿到几百毫秒，慢一点的正常响应会被全部掐断，最后一个端点才有机会拿到较长时间。
	slice := remaining / time.Duration(remainingEndpoints)
	if slice < minEndpointAttempt {
		slice = min(remaining, minEndpointAttempt)
	}
	return context.WithTimeout(parent, slice)
}

// minEndpointAttempt 是单个端点一次尝试的最短时间。
const minEndpointAttempt = 1200 * time.Millisecond

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
