package relay

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
)

// 有积压且最早一条等待超过 2 分钟才报；没有积压时不报；detail 只写最早一条的时刻。
func TestOutboxAttention(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	oldest := time.Now().UTC().Add(-time.Minute)
	metrics.SetOutboxSnapshotAt(5, oldest)
	compute := OutboxAttention(metrics)
	if got := compute(oldest.Add(119 * time.Second)); got != nil {
		t.Fatalf("未满 2 分钟不应报: %v", got)
	}
	first := compute(oldest.Add(121 * time.Second))[observability.AttentionOutboxBacklog]
	if len(first) != 1 {
		t.Fatalf("超过 2 分钟应报: %v", first)
	}
	metrics.SetOutboxSnapshotAt(9, oldest)
	if later := compute(oldest.Add(10 * time.Minute))[observability.AttentionOutboxBacklog]; later[0] != first[0] {
		t.Fatalf("积压条数变化 detail 不应变: %q vs %q", first[0], later[0])
	}
	metrics.SetOutboxSnapshotAt(0, time.Time{})
	if got := compute(oldest.Add(10 * time.Minute)); got != nil {
		t.Fatalf("没有积压不应报: %v", got)
	}
}
