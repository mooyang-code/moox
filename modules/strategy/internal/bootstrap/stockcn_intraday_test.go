package bootstrap

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// A 股 5m 的期望运行间隔跨午休按交易时段计算：最近处理的一根 bar_end 是 11:30 时，往后 2 根到 13:10（运行口径
// 100 分钟 / 2 = 50 分钟），往后 3 根到 13:15（成功口径 105 分钟 / 3 = 35 分钟），取较大者 50 分钟；盘中仍是 5 分钟。
func TestStockIntradayRunIntervalCrossesLunchBreak(t *testing.T) {
	observer, err := newInstanceObserver(openStore(t), prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	shanghai := time.FixedZone("CST", 8*3600)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.lastBarEnd["i1"] = time.Date(2026, 10, 8, 11, 30, 0, 0, shanghai)
	if got := observer.runIntervalLocked("i1", "cn_stock", "5m", 5*time.Minute); got != 50*time.Minute {
		t.Fatalf("午休前的期望间隔应是 50 分钟：%s", got)
	}
	observer.lastBarEnd["i1"] = time.Date(2026, 10, 8, 10, 0, 0, 0, shanghai)
	if got := observer.runIntervalLocked("i1", "cn_stock", "5m", 5*time.Minute); got != 5*time.Minute {
		t.Fatalf("盘中的期望间隔应是 5 分钟：%s", got)
	}
}

// A 股新实例在 10:50 进入积累期：ma__10 要攒到午休后，按交易日历往后数 20 根到 14:00 之前都算积累，不按墙钟 100 分钟截断。
func TestStockIntradayWarmupLimitCrossesLunchBreak(t *testing.T) {
	observer, err := newInstanceObserver(openStore(t), prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	shanghai := time.FixedZone("CST", 8*3600)
	instance := store.Instance{InstanceID: "i1", ResolvedJSON: json.RawMessage(`{"view_id":"v","factor_task_id":"f","bar":"5m","calendar":"cn_stock"}`)}
	start := time.Date(2026, 10, 8, 10, 50, 0, 0, shanghai)
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if !observer.accumulatingLocked(instance, "5m", start, true) {
		t.Fatal("进入积累期")
	}
	if !observer.accumulatingLocked(instance, "5m", time.Date(2026, 10, 8, 13, 50, 0, 0, shanghai), true) {
		t.Fatal("跨午休后 13:50 还在 20 根之内，应算积累")
	}
	if observer.accumulatingLocked(instance, "5m", time.Date(2026, 10, 8, 14, 30, 0, 0, shanghai), true) {
		t.Fatal("超过 20 根交易 K 线还缺数，应告警")
	}
}
