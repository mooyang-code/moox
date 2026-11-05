package exchange

import (
	"fmt"
	"slices"
	"strings"
)

// marketOrder 是市场集合的固定排列顺序，库里存的逗号分隔文本也按这个顺序。
var marketOrder = []MarketType{MarketTypeSpot, MarketTypeSwap, MarketTypeStock}

// NormalizeMarkets 校验并规范化账户已开通的市场集合：去重、按固定顺序排列。
// 加密货币账户取 SPOT、SWAP 的子集，股票账户只有 STOCK，两类不能混合。
func NormalizeMarkets(markets []MarketType) ([]MarketType, error) {
	if len(markets) == 0 {
		return nil, fmt.Errorf("至少要开通一个市场")
	}
	result := make([]MarketType, 0, len(markets))
	for _, market := range marketOrder {
		if slices.Contains(markets, market) {
			result = append(result, market)
		}
	}
	for _, market := range markets {
		if !slices.Contains(marketOrder, market) {
			return nil, fmt.Errorf("不支持的市场 %q", market)
		}
	}
	if slices.Contains(result, MarketTypeStock) && len(result) > 1 {
		return nil, fmt.Errorf("股票市场不能和现货、合约混合开通")
	}
	return result, nil
}

// JoinMarkets 把市场集合写成库里存的逗号分隔文本，例如 SPOT,SWAP。
func JoinMarkets(markets []MarketType) string {
	parts := make([]string, len(markets))
	for i, market := range markets {
		parts[i] = string(market)
	}
	return strings.Join(parts, ",")
}

// ParseMarkets 解析库里存的逗号分隔文本，要求已经是规范写法。
func ParseMarkets(raw string) ([]MarketType, error) {
	parts := strings.Split(raw, ",")
	markets := make([]MarketType, len(parts))
	for i, part := range parts {
		markets[i] = MarketType(part)
	}
	normalized, err := NormalizeMarkets(markets)
	if err != nil {
		return nil, fmt.Errorf("市场集合 %q：%w", raw, err)
	}
	if JoinMarkets(normalized) != raw {
		return nil, fmt.Errorf("市场集合 %q 不是规范写法", raw)
	}
	return normalized, nil
}

// ResolveInstrumentMarket 返回内部标的 ID 所属的市场：账户只开通一个市场时就是它；
// 开通多个市场时标的 ID 必须自带 -SPOT、-SWAP 后缀。
func ResolveInstrumentMarket(instrumentID string, markets []MarketType) MarketType {
	if len(markets) == 1 {
		return markets[0]
	}
	return MarketOfInstrumentID(instrumentID)
}

// UnionMarkets 返回若干市场集合的并集，按固定顺序排列。
func UnionMarkets(sets ...[]MarketType) []MarketType {
	var result []MarketType
	for _, market := range marketOrder {
		for _, set := range sets {
			if slices.Contains(set, market) {
				result = append(result, market)
				break
			}
		}
	}
	return result
}
