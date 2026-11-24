package telemetry

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// 每个声明的种类都要上报：Collect 的条数等于声明数，恢复后 Monitor 才能看到值回到 0。
func TestAttentionReportsEveryDeclaredKind(t *testing.T) {
	Attention.Set("test", map[string][]string{AttentionAutoPaused: {"组合账户暂停"}})
	defer Attention.Set("test", nil)
	series := make(chan prometheus.Metric, len(attentionKinds))
	Attention.Collect(series)
	close(series)
	count := 0
	for range series {
		count++
	}
	if count != len(attentionKinds) {
		t.Fatalf("上报 %d 项，应为 %d 项", count, len(attentionKinds))
	}
	if Attention.Active()[AttentionAutoPaused] != "组合账户暂停" {
		t.Fatalf("auto_paused 应在报: %v", Attention.Active())
	}
}
