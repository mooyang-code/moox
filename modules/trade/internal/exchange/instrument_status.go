package exchange

// StatusTradable 报告标的状态是否可交易：币安、模拟盘是 TRADING，OKX 是 live。
// 各处判断标的能不能下单、能不能补买都用它，不再各写一份。
func StatusTradable(status string) bool {
	return status == "TRADING" || status == "live"
}
