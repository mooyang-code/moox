package bootstrap

import "testing"

func TestDatasetCheckNames(t *testing.T) {
	for _, tc := range []struct {
		producer, subject, want string
	}{
		{"strategy", "动量轮动（26d51f7e）", "动量轮动（26d51f7e）· 策略结果"},
		{"strategy", datasetSubject("", "strategy_b", "1m"), "strategy_b（1分钟）· 策略结果"},
		{"factor", "现货分钟K线因子", "现货分钟K线因子 · 因子计算"},
		{"collector", datasetSubject("", "market_kline", "1m"), "market_kline（1分钟）· 采集"},
		{"custom", "数据集", "数据集 · custom"},
	} {
		if got := datasetCheckName(tc.producer, tc.subject); got != tc.want {
			t.Fatalf("datasetCheckName(%q, %q) = %q，应为 %q", tc.producer, tc.subject, got, tc.want)
		}
	}
	if got := klineCheckName("现货分钟K线"); got != "现货分钟K线 · K线结果" {
		t.Fatalf("K 线检查名称 %q", got)
	}
}
