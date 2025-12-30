package planner

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
// instType 参数被忽略（静态列表不区分产品类型）
func (p *DefaultSymbolProvider) GetSymbols(ctx context.Context, dataSource string, instType ...string) ([]string, error) {
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

// DatabaseSymbolProvider 数据库标的提供者实现
// 优先从数据库读取，如果数据库无数据则 fallback 到静态列表
type DatabaseSymbolProvider struct {
	dao            ExchangeSymbolDAO
	fallbackStatic bool // 是否启用 fallback 到静态列表
}

// ExchangeSymbolDAO 定义数据访问接口（避免循环依赖）
type ExchangeSymbolDAO interface {
	ListActiveSymbols(ctx context.Context, exchange, instType string) ([]string, error)
}

// NewDatabaseSymbolProvider 创建数据库标的提供者
func NewDatabaseSymbolProvider(dao ExchangeSymbolDAO, fallbackStatic bool) *DatabaseSymbolProvider {
	return &DatabaseSymbolProvider{
		dao:            dao,
		fallbackStatic: fallbackStatic,
	}
}

// GetSymbols 获取指定数据源和产品类型的所有标的
// 优先从数据库读取，如果数据库无数据则 fallback 到静态列表
func (p *DatabaseSymbolProvider) GetSymbols(ctx context.Context, dataSource string, instType ...string) ([]string, error) {
	// 确定产品类型（默认为 SPOT）
	var targetInstType string
	if len(instType) > 0 && instType[0] != "" {
		targetInstType = instType[0]
	} else {
		targetInstType = "SPOT"
	}

	// 1. 尝试从数据库获取
	symbols, err := p.dao.ListActiveSymbols(ctx, dataSource, targetInstType)
	if err == nil && len(symbols) > 0 {
		return symbols, nil
	}

	// 2. 如果启用了 fallback，返回静态列表
	if p.fallbackStatic {
		ds := strings.ToLower(dataSource)
		if staticList, exists := staticSymbols[ds]; exists {
			return staticList, nil
		}
		return defaultSymbols, nil
	}

	// 3. 不启用 fallback 时，返回空列表
	if err != nil {
		return []string{}, err
	}
	return symbols, nil
}

