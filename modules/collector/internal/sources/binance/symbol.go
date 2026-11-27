package binance

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
)

// SymbolCollector 标的同步采集器
type SymbolCollector struct {
	client          *binanceapi.Client
	spotAPI         *binanceapi.SpotAPI
	swapAPI         *binanceapi.SwapAPI
	fetchSymbolPage func(context.Context, *sources.CollectParams) ([]*binanceapi.SymbolInfo, error)
}

// NewSymbolCollector returns a configured Binance symbol protocol client.
func NewSymbolCollector(clients ...*binanceapi.Client) *SymbolCollector {
	client := (*binanceapi.Client)(nil)
	if len(clients) > 0 {
		client = clients[0]
	}
	if client == nil {
		client = newConfiguredClient()
	}
	return &SymbolCollector{client: client, spotAPI: binanceapi.NewSpotAPI(client), swapAPI: binanceapi.NewSwapAPI(client)}
}

// fetchSymbols 获取标的列表
func (c *SymbolCollector) fetchSymbols(ctx context.Context, params *sources.CollectParams) ([]*binanceapi.SymbolInfo, error) {
	if c.fetchSymbolPage != nil {
		return c.fetchSymbolPage(ctx, params)
	}
	switch params.InstType {
	case InstTypeSPOT:
		var lastErr error
		domains := c.client.SpotDomains()
		for index, domain := range domains {
			attemptCtx, cancel := endpointAttemptContext(binanceapi.SingleAttempt(ctx), len(domains)-index)
			symbols, err := c.spotAPI.GetExchangeInfoWithDomainIPs(attemptCtx, domain, nil)
			cancel()
			if err == nil {
				return symbols, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	case InstTypeSWAP:
		var lastErr error
		domains := c.client.SwapDomains()
		for index, domain := range domains {
			attemptCtx, cancel := endpointAttemptContext(binanceapi.SingleAttempt(ctx), len(domains)-index)
			symbols, err := c.swapAPI.GetExchangeInfoWithDomainIPs(attemptCtx, domain, nil)
			cancel()
			if err == nil {
				return symbols, nil
			}
			lastErr = err
			if ctx.Err() != nil {
				break
			}
		}
		return nil, lastErr
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

// filterSymbols 只保留 USDT 计价的交易对，交易状态不限（带在 TradingStatus 里）：标签同步只新增 TRADING 的，
// 已是成员、临时 BREAK 的留着交给交易状态检测，见 subjectsync.TagRunner。
func (c *SymbolCollector) filterSymbols(symbols []*binanceapi.SymbolInfo) []*binanceapi.SymbolInfo {
	filtered := make([]*binanceapi.SymbolInfo, 0, len(symbols))

	for _, symbol := range symbols {
		if symbol != nil && symbol.QuoteAsset == "USDT" {
			filtered = append(filtered, symbol)
		}
	}

	return filtered
}

func externalSymbol(symbol *binanceapi.SymbolInfo) string {
	if symbol == nil {
		return ""
	}
	value := strings.ToUpper(strings.TrimSpace(symbol.Symbol))
	parts := strings.Split(value, "-")
	if len(parts) >= 2 {
		return strings.Join(parts[:2], "")
	}
	return strings.ReplaceAll(value, "-", "")
}
