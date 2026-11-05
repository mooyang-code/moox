package exchange

import (
	"fmt"
	"strings"
)

// SymbolKey 在交易所内唯一标识一个标的：币安现货和合约都叫 BTCUSDT，订单号、成交号也是两套独立序列，
// 所以任何以交易所代码做键的 map、集合、缓存和查询参数都必须带上市场。
type SymbolKey struct {
	Market MarketType
	Symbol string
}

// NewSymbolKey 用市场和交易所代码构造键，接受字符串形式的市场（数据库记录里的写法）。
func NewSymbolKey[T ~string](market T, symbol string) SymbolKey {
	return SymbolKey{Market: MarketType(market), Symbol: symbol}
}

// String 返回“市场:交易所代码”，用于 JSON 的键和日志。
func (k SymbolKey) String() string {
	return string(k.Market) + ":" + k.Symbol
}

// IsZero 报告键是否缺少市场或代码。
func (k SymbolKey) IsZero() bool {
	return k.Market == MarketTypeUnspecified || k.Symbol == ""
}

// ParseSymbolKey 解析 String 的输出。
func ParseSymbolKey(raw string) (SymbolKey, error) {
	market, symbol, found := strings.Cut(raw, ":")
	key := SymbolKey{Market: MarketType(market), Symbol: symbol}
	if !found || !key.Market.Valid() || key.Symbol == "" {
		return SymbolKey{}, fmt.Errorf("标的键 %q 不是“市场:交易所代码”", raw)
	}
	return key, nil
}
