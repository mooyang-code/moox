package bootstrap

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// A 股实例的期望运行间隔按交易日计算：最近处理的一根之后第二个交易日收盘时下一根还没处理才告警，不因周末与长假误报
// run stale；crypto 仍是 bar 时长。
func TestStockRunIntervalFollowsTradingDays(t *testing.T) {
	observer, err := newInstanceObserver(openStore(t), prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	shanghai := time.FixedZone("CST", 8*3600)
	closeAt := func(month time.Month, day int) time.Time { return time.Date(2026, month, day, 15, 0, 0, 0, shanghai) }
	observer.mu.Lock()
	defer observer.mu.Unlock()
	for name, tc := range map[string]struct {
		last time.Time
		want time.Duration
	}{
		"周二处理完（下两根是周三、周四）":    {last: closeAt(10, 13), want: 24 * time.Hour},
		"周五处理完（隔着周末到下周二）":     {last: closeAt(10, 9), want: 48 * time.Hour},
		"国庆前处理完（10-08、10-09）": {last: closeAt(9, 30), want: 108 * time.Hour},
	} {
		observer.lastBarEnd["i1"] = tc.last
		if got := observer.runIntervalLocked("i1", "cn_stock", "1d", 24*time.Hour); got != tc.want {
			t.Fatalf("%s：期望间隔 %s，实际 %s", name, tc.want, got)
		}
	}
	// 还没处理过时以最近闭合一根的上一根为基准：周六时最近闭合的是周五，基准是周四，下两根到周一，间隔偏宽。
	delete(observer.lastBarEnd, "i1")
	observer.now = func() time.Time { return time.Date(2026, 10, 10, 10, 0, 0, 0, shanghai) }
	if got := observer.runIntervalLocked("i1", "cn_stock", "1d", 24*time.Hour); got != 48*time.Hour {
		t.Fatalf("没有处理记录时应按最近闭合一根的上一根计算：%s", got)
	}
	if got := observer.runIntervalLocked("i1", "crypto_24x7", "1h", time.Hour); got != time.Hour {
		t.Fatalf("crypto 的期望间隔是 bar 时长：%s", got)
	}
}

// 取消计数按原因预先登记（没发生过的原因也有值为 0 的序列），取消后又发出的目标另有计数器。
func TestCancelMetricsArePreRegistered(t *testing.T) {
	registry := prometheus.NewRegistry()
	if _, err := newInstanceObserver(openStore(t), registry, t.Logf); err != nil {
		t.Fatal(err)
	}
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	series := map[string]int{}
	for _, family := range families {
		series[family.GetName()] = len(family.GetMetric())
	}
	if series["moox_strategy_target_cancelled_total"] != 4 || series["moox_strategy_target_sent_after_cancel_total"] != 1 {
		t.Fatalf("取消计数应预先登记 4 个原因并有取消后发出的计数器：%v", series)
	}
}
