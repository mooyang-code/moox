package bootstrap

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// 周五 ok、周一跳过：运行口径只剩 24 小时（周一到周三的一半），但上次成功在周五，按 3 个间隔算的 success stale 会在
// 周一立即触发；期望间隔取成功口径（周五到下周三的三分之一，40 小时），周三收盘前不告警。
func TestStockIntervalCoversSuccessAfterSkippedMonday(t *testing.T) {
	observer, err := newInstanceObserver(openStore(t), prometheus.NewRegistry(), t.Logf)
	if err != nil {
		t.Fatal(err)
	}
	shanghai := time.FixedZone("CST", 8*3600)
	friday := time.Date(2026, 10, 9, 15, 0, 0, 0, shanghai)
	monday := time.Date(2026, 10, 12, 15, 0, 0, 0, shanghai)
	instance := store.Instance{InstanceID: "i1", SpaceID: "stock", ResolvedJSON: []byte(`{"view_id":"v","bar":"1d","calendar":"cn_stock","columns":{},"view_columns":["close"]}`)}
	observer.ObservePeriod(instance, "1d", friday, store.StatusOK, "")
	observer.ObservePeriod(instance, "1d", monday, store.StatusSkipped, "factor_missing")
	observer.mu.Lock()
	defer observer.mu.Unlock()
	interval := observer.runIntervalLocked("i1", "cn_stock", "1d", 24*time.Hour)
	if interval != 40*time.Hour {
		t.Fatalf("期望间隔应按成功口径取 40 小时：%s", interval)
	}
	lastSuccess := friday.Add(30 * time.Minute)
	if deadline := lastSuccess.Add(successMissedIntervals*interval + 30*time.Second); !deadline.After(monday.Add(35 * time.Minute)) {
		t.Fatalf("周一跳过后不应立即 success stale：截止 %s", deadline)
	}
}
