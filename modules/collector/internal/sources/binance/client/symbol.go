package binance

import "strings"

// FormatSymbol 转换交易对格式
// 输入: BTC-USDT, ETH-USDT
// 输出: BTCUSDT, ETHUSDT
func FormatSymbol(symbol string) string {
	// 移除分隔符
	return strings.ReplaceAll(symbol, "-", "")
}

// ParseSymbol 解析币安交易对格式
// 输入: BTCUSDT
// 输出: BTC-USDT（假设 quote 为 USDT）
func ParseSymbol(symbol string, quote string) string {
	if quote == "" {
		quote = "USDT"
	}

	// 检查是否以 quote 结尾
	if strings.HasSuffix(symbol, quote) {
		base := strings.TrimSuffix(symbol, quote)
		return base + "-" + quote
	}

	return symbol
}
