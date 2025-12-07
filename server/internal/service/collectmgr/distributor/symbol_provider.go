package distributor

import (
	"context"
	"sync"
	"time"
)

// DefaultSymbolProvider 默认标的提供者实现
// 目前返回空列表，标的由规则的 collect_params.objects 提供
// 后续可扩展：从交易所 API 获取、从数据库读取等
type DefaultSymbolProvider struct {
	// 缓存：数据源 -> 标的列表
	cache     map[string][]string
	cacheTime map[string]time.Time
	cacheTTL  time.Duration
	mu        sync.RWMutex
}

// NewDefaultSymbolProvider 创建默认标的提供者
func NewDefaultSymbolProvider() *DefaultSymbolProvider {
	return &DefaultSymbolProvider{
		cache:     make(map[string][]string),
		cacheTime: make(map[string]time.Time),
		cacheTTL:  5 * time.Minute, // 缓存 5 分钟
	}
}

// GetSymbols 获取指定数据源的所有标的
// 目前返回空列表，标的由规则参数中的 objects 提供
func (p *DefaultSymbolProvider) GetSymbols(ctx context.Context, dataSource string) ([]string, error) {
	// 检查缓存
	p.mu.RLock()
	if symbols, ok := p.cache[dataSource]; ok {
		if time.Since(p.cacheTime[dataSource]) < p.cacheTTL {
			p.mu.RUnlock()
			return symbols, nil
		}
	}
	p.mu.RUnlock()

	// 获取新数据
	symbols, err := p.fetchSymbols(ctx, dataSource)
	if err != nil {
		return nil, err
	}

	// 更新缓存
	p.mu.Lock()
	p.cache[dataSource] = symbols
	p.cacheTime[dataSource] = time.Now()
	p.mu.Unlock()

	return symbols, nil
}

// fetchSymbols 从数据源获取标的列表
// 目前从硬编码的标的列表中获取，后续可扩展
func (p *DefaultSymbolProvider) fetchSymbols(ctx context.Context, dataSource string) ([]string, error) {
	// 从硬编码的静态标的列表中获取
	staticSymbols := getStaticSymbols()

	if symbols, exists := staticSymbols[dataSource]; exists {
		return symbols, nil
	}

	// 如果没有配置该数据源，返回空列表
	// 后续可扩展实现：
	// 1. 从交易所 API 动态获取
	// 2. 从数据库读取
	// 3. 从配置文件读取
	return []string{}, nil
}

// getStaticSymbols 获取静态标的列表（硬编码）
func getStaticSymbols() map[string][]string {
	return map[string][]string{
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
}

// SetCacheTTL 设置缓存过期时间
func (p *DefaultSymbolProvider) SetCacheTTL(ttl time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cacheTTL = ttl
}

// ClearCache 清空缓存
func (p *DefaultSymbolProvider) ClearCache() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache = make(map[string][]string)
	p.cacheTime = make(map[string]time.Time)
}

// ClearCacheForDataSource 清空指定数据源的缓存
func (p *DefaultSymbolProvider) ClearCacheForDataSource(dataSource string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.cache, dataSource)
	delete(p.cacheTime, dataSource)
}
