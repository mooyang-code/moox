package telemetry

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

func TestPublishAttentionKeepsDetailWithinMonitorLimit(t *testing.T) {
	PublishAttention(map[string][]string{AttentionAutoPaused: {strings.Repeat("组合账户暂停", 200)}})
	// 缓冲按 kind 个数开：Collect 是同步写入，缓冲不够会一直阻塞。
	series := make(chan prometheus.Metric, len(attentionKinds))
	Attention.Collect(series)
	close(series)
	count := 0
	for metric := range series {
		count++
		var value dto.Metric
		if err := metric.Write(&value); err != nil {
			t.Fatal(err)
		}
		for _, label := range value.GetLabel() {
			if len(label.GetValue()) > 512 {
				t.Fatalf("标签长度 %d 超过 Monitor 上限", len(label.GetValue()))
			}
		}
	}
	if count != len(attentionKinds) {
		t.Fatalf("上报 %d 项，应为 %d 项", count, len(attentionKinds))
	}
}
