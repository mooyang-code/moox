package report

import (
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEvaluateObjectStore(t *testing.T) {
	now := time.Date(2026, 11, 7, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		since time.Time
		want  int
	}{
		{time.Time{}, ObjectStoreAvailable},
		{now.Add(-4 * time.Minute), ObjectStoreUnavailable},
		{now.Add(-5 * time.Minute), ObjectStoreDown},
	}
	for _, c := range cases {
		if got := EvaluateObjectStore(c.since, now); got != c.want {
			t.Fatalf("EvaluateObjectStore(%v) = %d, want %d", c.since, got, c.want)
		}
	}
}

// 每次设置只保留当前一条序列，probe_error 按字节截短且不切断字符；Clear 后不报；指标被 Monitor 采集。
func TestArchiveObjectStoreKeepsOnlyCurrentSeries(t *testing.T) {
	registry := prometheus.NewRegistry()
	metric, err := NewArchiveObjectStore(registry)
	if err != nil {
		t.Fatal(err)
	}
	metric.Set("archive-worker@m1", ObjectStoreUnavailable, "连接被拒绝")
	long := strings.Repeat("错", 300)
	metric.Set("archive-worker@m1", ObjectStoreDown, long)
	if n := testutil.CollectAndCount(registry, ArchiveObjectStoreStateMetric); n != 1 {
		t.Fatalf("series = %d", n)
	}
	if v := testutil.ToFloat64(metric.gauge.WithLabelValues("archive-worker@m1", strings.Repeat("错", maxProbeErrorLabel/3))); v != ObjectStoreDown {
		t.Fatalf("state = %v", v)
	}
	metric.Clear()
	if n := testutil.CollectAndCount(registry, ArchiveObjectStoreStateMetric); n != 0 {
		t.Fatalf("after clear series = %d", n)
	}
	if !IsMonitoredMetric(ArchiveObjectStoreStateMetric) {
		t.Fatal("对象存储状态必须上报给 Monitor")
	}
}
