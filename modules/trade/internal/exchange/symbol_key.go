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

// IsZero 报告键是否完全没填（市场和代码都为空），用作“不按标的过滤”。半空的键（有代码没市场）不是零值，会被 Validate 拒绝。
func (k SymbolKey) IsZero() bool {
	return k.Market == MarketTypeUnspecified && k.Symbol == ""
}

// Valid 报告键的市场和代码是否都已填写。
func (k SymbolKey) Valid() bool {
	return k.Market != MarketTypeUnspecified && k.Symbol != ""
}

// Validate 要求键要么完全没填，要么市场和代码都填了：给了代码就必须给市场，不猜。
func (k SymbolKey) Validate() error {
	if k.IsZero() || k.Valid() {
		return nil
	}
	return fmt.Errorf("标的键 %q 缺少市场或交易所代码：给了代码就必须同时给市场", k.String())
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
