package distributor

import (
	"context"
	"strings"
)

// DefaultSymbolProvider 默认标的提供者实现
// 直接返回硬编码的标的列表
type DefaultSymbolProvider struct{}

// NewDefaultSymbolProvider 创建默认标的提供者
func NewDefaultSymbolProvider() *DefaultSymbolProvider {
	return &DefaultSymbolProvider{}
}

// GetSymbols 获取指定数据源的所有标的
// 直接从硬编码的静态标的列表中获取
func (p *DefaultSymbolProvider) GetSymbols(ctx context.Context, dataSource string) ([]string, error) {
	// 统一转小写进行匹配
	ds := strings.ToLower(dataSource)

	if symbols, exists := staticSymbols[ds]; exists {
		return symbols, nil
	}

	// 如果没有配置该数据源，返回默认标的列表
	return defaultSymbols, nil
}

// 默认标的列表（当数据源未配置时使用）
var defaultSymbols = []string{
	"BTC-USDT", "ETH-USDT", "BNB-USDT", "SOL-USDT", "XRP-USDT",
	"ADA-USDT", "DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
	"LINK-USDT", "UNI-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT",
}

// staticSymbols 静态标的列表（硬编码）
var staticSymbols = map[string][]string{
	// 币安 (Binance)
	"binance": {
		"BTC-USDT", "ETH-USDT", "BNB-USDT", "SOL-USDT", "XRP-USDT",
		"ADA-USDT", "DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
		"LINK-USDT", "UNI-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT",
		"ETC-USDT", "FIL-USDT", "NEAR-USDT", "APT-USDT", "ARB-USDT",
	},
	// OKX
	"okx": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "ADA-USDT",
		"DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT", "LINK-USDT",
		"UNI-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT", "ETC-USDT",
		"FIL-USDT", "NEAR-USDT", "APT-USDT", "ARB-USDT", "OP-USDT",
	},
	// 火币 (Huobi)
	"huobi": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "ADA-USDT",
		"DOGE-USDT", "DOT-USDT", "MATIC-USDT", "LINK-USDT", "ATOM-USDT",
		"LTC-USDT", "BCH-USDT", "ETC-USDT", "FIL-USDT", "TRX-USDT",
		"XLM-USDT", "VET-USDT", "ICP-USDT", "THETA-USDT", "FTM-USDT",
	},
	// Bybit
	"bybit": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "BNB-USDT",
		"ADA-USDT", "DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
		"LINK-USDT", "UNI-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT",
		"NEAR-USDT", "APT-USDT", "ARB-USDT", "OP-USDT", "SUI-USDT",
	},
	// Bitget
	"bitget": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "BNB-USDT",
		"ADA-USDT", "DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
		"LINK-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT", "FIL-USDT",
		"APT-USDT", "ARB-USDT", "OP-USDT",
	},
	// KuCoin
	"kucoin": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "BNB-USDT",
		"ADA-USDT", "DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
		"LINK-USDT", "UNI-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT",
		"ETC-USDT", "FIL-USDT", "NEAR-USDT",
	},
	// Gate.io
	"gate": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "BNB-USDT",
		"ADA-USDT", "DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT",
		"LINK-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT", "FIL-USDT",
	},
	// MEXC
	"mexc": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "BNB-USDT",
		"ADA-USDT", "DOGE-USDT", "DOT-USDT", "MATIC-USDT", "LINK-USDT",
		"ATOM-USDT", "LTC-USDT", "BCH-USDT",
	},
	// Bitfinex
	"bitfinex": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "ADA-USDT",
		"DOT-USDT", "MATIC-USDT", "LINK-USDT", "ATOM-USDT", "LTC-USDT",
		"BCH-USDT", "ETC-USDT",
	},
	// Coinbase
	"coinbase": {
		"BTC-USDT", "ETH-USDT", "SOL-USDT", "XRP-USDT", "ADA-USDT",
		"DOGE-USDT", "AVAX-USDT", "DOT-USDT", "MATIC-USDT", "LINK-USDT",
		"UNI-USDT", "ATOM-USDT", "LTC-USDT", "BCH-USDT",
	},
}
