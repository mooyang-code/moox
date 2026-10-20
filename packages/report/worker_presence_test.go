package report

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestEvaluatePresence(t *testing.T) {
	now := time.Date(2026, 10, 18, 6, 0, 0, 0, time.UTC)
	cases := []struct {
		required bool
		last     time.Time
		want     int
	}{
		{false, time.Time{}, PresenceNotRequired},
		{true, now.Add(-90 * time.Second), PresenceOnline},
		{true, now.Add(-121 * time.Second), PresenceOffline},
		{true, time.Time{}, PresenceOffline},
	}
	for _, c := range cases {
		if got := EvaluatePresence(c.required, c.last, now, 0); got != c.want {
			t.Fatalf("EvaluatePresence(%v, %v) = %d, want %d", c.required, c.last, got, c.want)
		}
	}
}

// 换了一台机器后只保留当前这台的标签；两个指标都被 Monitor 采集。
func TestWorkerPresenceKeepsOnlyCurrentWorker(t *testing.T) {
	registry := prometheus.NewRegistry()
	presence, err := NewWorkerPresence(registry, "factor")
	if err != nil {
		t.Fatal(err)
	}
	last := time.Unix(1_800_000_000, 0)
	presence.Set(PresenceOnline, "factor-engine@old", last)
	presence.Set(PresenceOffline, "factor-engine@new", last)
	if n := testutil.CollectAndCount(registry, "moox_factor_worker_presence_state"); n != 1 {
		t.Fatalf("presence series = %d", n)
	}
	if v := testutil.ToFloat64(presence.state.WithLabelValues("factor-engine@new")); v != PresenceOffline {
		t.Fatalf("state = %v", v)
	}
	for _, name := range []string{"moox_factor_worker_presence_state", "moox_archive_worker_last_heartbeat_timestamp_seconds"} {
		if !IsMonitoredMetric(name) {
			t.Fatalf("%s must be reported to Monitor", name)
		}
	}
}
