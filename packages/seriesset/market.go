package seriesset

import "strings"

// 序列所属的市场（小写），与标签 market_type、策略 DSL 的 market 取值一致。
const (
	MarketSpot = "spot"
	MarketSwap = "swap"
	// MarketMulti 是统一数据集的 market_type：同一个数据集里同时放现货与永续两条序列。
	MarketMulti = "multi"
)

const marketSegmentPrefix = "market:"

// SeriesTagMarket 从 series_tag 解析序列所属的市场：按 `|` 分段，取 `market:` 段的值（小写）；
// 没有这一段（例如 `venue:binance`）的视为现货。
// 采集写行时生成的 tag 见 collector 的 PlanSeriesTag，现货 `venue:binance|market:spot|source:spot_http`，
// 永续 `venue:binance|market:swap|source:swap_http`。
func SeriesTagMarket(tag string) string {
	for _, segment := range strings.Split(tag, "|") {
		segment = strings.TrimSpace(segment)
		if len(segment) >= len(marketSegmentPrefix) && strings.EqualFold(segment[:len(marketSegmentPrefix)], marketSegmentPrefix) {
			if value := strings.ToLower(strings.TrimSpace(segment[len(marketSegmentPrefix):])); value != "" {
				return value
			}
		}
	}
	return MarketSpot
}
