package replay

import (
	"testing"
	"time"
)

// A 股日内的年化按 244 个交易日 × 每日根数（5m 为 48），年化门槛是一周 5 个交易日的根数；日线口径不变。
func TestStockIntradayAnnualization(t *testing.T) {
	if got := periodsPerYear("cn_stock", "5m", 5*time.Minute); got != 244*48 {
		t.Fatalf("A 股 5m 一年应是 244×48 根：%v", got)
	}
	if got := minAnnualizedBars("cn_stock", "5m", 5*time.Minute); got != 240 {
		t.Fatalf("A 股 5m 一周应是 240 根：%d", got)
	}
	if got := periodsPerYear("cn_stock", "1d", 24*time.Hour); got != 244 {
		t.Fatalf("A 股日线一年仍是 244 根：%v", got)
	}
	if got := periodsPerYear("crypto_24x7", "5m", 5*time.Minute); got != 365*24*12 {
		t.Fatalf("crypto 按自然时间：%v", got)
	}
}
