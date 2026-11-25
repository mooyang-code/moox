package bootstrap

import (
	"strings"
	"testing"
	"time"
)

// 连续水位阻塞超过 30 分钟才上报，detail 写任务与阻塞起点，同一次阻塞内不变。
func TestWatermarkAttention(t *testing.T) {
	since := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	o := &factorDatasetObserver{blocked: map[string]blockedWatermark{"task-a": {spaceID: "crypto", since: since}}}
	if got := o.watermarkAttention(since.Add(29 * time.Minute)); got != nil {
		t.Fatalf("阻塞 29 分钟不应上报: %v", got)
	}
	first := o.watermarkAttention(since.Add(31 * time.Minute))[attentionWatermarkStalled]
	if len(first) != 1 || !strings.Contains(first[0], "crypto/task-a 自 11-24 03:00 UTC 起") {
		t.Fatalf("阻塞 31 分钟应上报: %v", first)
	}
	if later := o.watermarkAttention(since.Add(3 * time.Hour))[attentionWatermarkStalled]; later[0] != first[0] {
		t.Fatalf("同一次阻塞 detail 应不变: %q vs %q", first[0], later[0])
	}
	o.blocked = map[string]blockedWatermark{}
	if got := o.watermarkAttention(since.Add(3 * time.Hour)); got != nil {
		t.Fatalf("阻塞解除后不应上报: %v", got)
	}
}
