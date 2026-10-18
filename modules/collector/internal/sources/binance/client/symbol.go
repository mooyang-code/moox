package binance

import "strings"

// FormatSymbol 转换交易对格式
// 输入: BTC-USDT, ETH-USDT
// 输出: BTCUSDT, ETHUSDT
func FormatSymbol(symbol string) string {
	// 移除分隔符
	return strings.ReplaceAll(symbol, "-", "")
}
