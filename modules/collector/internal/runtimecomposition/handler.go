// Package runtimecomposition wires concrete data sources into common collection
// pipelines. Neither the scheduler nor marketfetch imports provider adapters.
package runtimecomposition

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
)

func NewHandler() *marketfetch.Handler {
	h := marketfetch.NewHandler()
	h.NewInstrumentPipeline = NewMarketInstrumentPipeline
	h.NewMarketKlinePipeline = NewMarketKlinePipeline
	h.NewStockKlinePipeline = NewStockKlinePipeline
	h.ResolveSourceID = DefaultSourceID
	h.ResolveSymbol = ResolveSymbol
	return h
}

func ResolveSymbol(provider, marketID, marketType, subjectID, configured string) (string, error) {
	if strings.EqualFold(strings.TrimSpace(provider), "binance") && strings.EqualFold(strings.TrimSpace(marketID), "crypto") &&
		(strings.EqualFold(strings.TrimSpace(marketType), "spot") || strings.EqualFold(strings.TrimSpace(marketType), "swap")) {
		return binance.ProviderSymbol(subjectID, configured)
	}
	return marketfetch.DefaultProviderSymbol(marketID, marketType, subjectID, configured)
}

// CompactSymbol deliberately limits omission to the codec already deployed in
// crypto Timer runtimes. Other markets retain their explicit symbol mapping.
func CompactSymbol(provider, marketID, marketType, subjectID, configured string) (string, error) {
	if !strings.EqualFold(strings.TrimSpace(provider), "binance") || !strings.EqualFold(strings.TrimSpace(marketID), "crypto") ||
		(!strings.EqualFold(strings.TrimSpace(marketType), "spot") && !strings.EqualFold(strings.TrimSpace(marketType), "swap")) {
		return "", fmt.Errorf("source requires explicit symbol mapping")
	}
	return binance.ProviderSymbol(subjectID, configured)
}

func DefaultSourceID(provider, instrumentType string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "binance":
		switch strings.ToLower(strings.TrimSpace(instrumentType)) {
		case "spot":
			return "spot_http"
		case "swap":
			return "swap_http"
		}
	case "sina":
		return "stockcn_minute_http"
	case "eastmoney", "tencent", "baidu":
		return "stockcn_http"
	}
	return ""
}
