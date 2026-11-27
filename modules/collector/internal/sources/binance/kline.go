package binance

import (
	"context"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
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
	fetchKlinePage func(context.Context, *sources.CollectParams, *binanceapi.KlineRequest) ([]*binanceapi.Kline, error)
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
func (c *KlineCollector) fetchKlines(ctx context.Context, params *sources.CollectParams, req *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
	if c.fetchKlinePage != nil {
		return c.fetchKlinePage(ctx, params, req)
	}
	return c.fetchExchangeKlines(ctx, params, req)
}

// fetchKlinesOnce deliberately performs one physical provider request.
func (c *KlineCollector) fetchKlinesOnce(ctx context.Context, params *sources.CollectParams, req *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
	return c.fetchKlines(binanceapi.SingleAttempt(ctx), params, req)
}

func (c *KlineCollector) fetchExchangeKlines(ctx context.Context, params *sources.CollectParams, req *binanceapi.KlineRequest) ([]*binanceapi.Kline, error) {
	switch params.InstType {
	case InstTypeSPOT:
		return tryEndpoints(ctx, c.client.SpotDomains(), func(attemptCtx context.Context, domain string) ([]*binanceapi.Kline, error) {
			return c.spotAPI.GetKlineWithDomainIPs(attemptCtx, req, domain, nil)
		})
	case InstTypeSWAP:
		return tryEndpoints(ctx, c.client.SwapDomains(), func(attemptCtx context.Context, domain string) ([]*binanceapi.Kline, error) {
			return c.swapAPI.GetKlineWithDomainIPs(attemptCtx, req, domain, nil)
		})
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

// tryEndpoints 按端点顺序请求，前一个失败才换下一个。
func tryEndpoints(ctx context.Context, domains []string, call func(context.Context, string) ([]*binanceapi.Kline, error)) ([]*binanceapi.Kline, error) {
	var lastErr error
	for index := range domains {
		if err := defaultWeightGovernor.Admit(ctx, domains[index]); err != nil {
			return nil, err
		}
		attemptCtx, cancel := endpointAttemptContext(ctx, len(domains)-index)
		klines, err := call(attemptCtx, domains[index])
		cancel()
		if err == nil {
			return klines, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	return nil, lastErr
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
	slice := remaining / time.Duration(remainingEndpoints)
	if slice < minEndpointAttempt {
		slice = min(remaining, minEndpointAttempt)
	}
	return context.WithTimeout(parent, slice)
}

const minEndpointAttempt = 2 * time.Second

func filterClosedKlines(klines []*binanceapi.Kline, now time.Time) ([]*binanceapi.Kline, int) {
	closed := make([]*binanceapi.Kline, 0, len(klines))
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

func isKlineClosed(kline *binanceapi.Kline, now time.Time) bool {
	if kline == nil || kline.CloseTime.IsZero() {
		return false
	}
	return now.After(kline.CloseTime)
}
