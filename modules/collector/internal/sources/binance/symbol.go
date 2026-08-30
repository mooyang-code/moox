package binance

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	binanceapi "github.com/mooyang-code/moox/modules/collector/internal/sources/binance/client"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/exchange"
)

// SymbolCollector 标的同步采集器
type SymbolCollector struct {
	client          *binanceapi.Client
	spotAPI         *binanceapi.SpotAPI
	swapAPI         *binanceapi.SwapAPI
	fetchSymbolPage func(context.Context, *sources.CollectParams) ([]*exchange.SymbolInfo, error)
}

// NewSymbolCollector returns a configured Binance symbol protocol client.
func NewSymbolCollector() *SymbolCollector {
	client := newConfiguredClient()
	return &SymbolCollector{client: client, spotAPI: binanceapi.NewSpotAPI(client), swapAPI: binanceapi.NewSwapAPI(client)}
}

// fetchSymbols 获取标的列表
func (c *SymbolCollector) fetchSymbols(ctx context.Context, params *sources.CollectParams) ([]*exchange.SymbolInfo, error) {
	if c.fetchSymbolPage != nil {
		return c.fetchSymbolPage(ctx, params)
	}
	switch params.InstType {
	case InstTypeSPOT:
		return c.spotAPI.GetExchangeInfoWithIPs(binanceapi.SingleAttempt(ctx), params.DNSIPs(c.client.SpotDomain()))
	case InstTypeSWAP:
		return c.swapAPI.GetExchangeInfoWithIPs(binanceapi.SingleAttempt(ctx), params.DNSIPs(c.client.SwapDomain()))
	default:
		return nil, fmt.Errorf("不支持的产品类型: %s", params.InstType)
	}
}

// filterSymbols 过滤标的列表，仅保留 QuoteAsset 为 USDT 且 Status 为 active 的数据
func (c *SymbolCollector) filterSymbols(symbols []*exchange.SymbolInfo) []*exchange.SymbolInfo {
	filtered := make([]*exchange.SymbolInfo, 0, len(symbols))

	for _, symbol := range symbols {
		// 仅保留 QuoteAsset 为 USDT 且 Status 为 active 的标的
		if symbol.QuoteAsset == "USDT" && symbol.Status == "active" {
			filtered = append(filtered, symbol)
		}
	}

	return filtered
}

func normalizedSubjectID(symbol *exchange.SymbolInfo, market ...string) string {
	if symbol == nil {
		return ""
	}
	base, quote := strings.ToUpper(strings.TrimSpace(symbol.BaseAsset)), strings.ToUpper(strings.TrimSpace(symbol.QuoteAsset))
	if base == "" || quote == "" {
		value := strings.ToUpper(strings.TrimSpace(symbol.Symbol))
		parts := strings.Split(value, "-")
		if len(parts) >= 2 {
			base, quote = parts[0], parts[1]
		}
		if base == "" || quote == "" {
			parsed := binanceapi.ParseSymbol(value, quote)
			parts = strings.Split(parsed, "-")
			if len(parts) >= 2 {
				base, quote = parts[0], parts[1]
			}
		}
	}
	suffix := ""
	if len(market) > 0 {
		switch strings.ToLower(strings.TrimSpace(market[0])) {
		case "spot":
			suffix = "SPOT"
		case "swap", "perpetual", "future", "futures":
			suffix = "SWAP"
		}
	}
	if suffix == "" {
		parts := strings.Split(strings.ToUpper(strings.TrimSpace(symbol.Symbol)), "-")
		if len(parts) >= 3 && (parts[2] == "SPOT" || parts[2] == "SWAP") {
			suffix = parts[2]
		}
	}
	if base == "" || quote == "" {
		return strings.ToUpper(strings.TrimSpace(symbol.Symbol))
	}
	if suffix == "" {
		return base + "-" + quote
	}
	return base + "-" + quote + "-" + suffix
}

func externalSymbol(symbol *exchange.SymbolInfo) string {
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
