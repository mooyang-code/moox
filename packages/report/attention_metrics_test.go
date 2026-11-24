package report

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// 关注项指标由 Monitor 生成检查并发告警：不在白名单里，上报时就被丢掉，告警永远不会触发。
func TestAttentionMetricIsMonitored(t *testing.T) {
	if !IsMonitoredMetric(AttentionMetric) {
		t.Fatalf("%s 必须在上报白名单里", AttentionMetric)
	}
	for _, name := range []string{"moox_trade_attention_active", "moox_collector_attention_active"} {
		if IsMonitoredMetric(name) {
			t.Errorf("旧关注项指标 %s 已合成 %s，不应再上报", name, AttentionMetric)
		}
	}
}

func collectAttention(t *testing.T, registry *prometheus.Registry) map[string]*dto.Metric {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*dto.Metric{}
	for _, family := range families {
		if family.GetName() != AttentionMetric {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "kind" {
					out[label.GetValue()] = metric
				}
			}
		}
	}
	return out
}

func labelOf(metric *dto.Metric, name string) string {
	for _, label := range metric.GetLabel() {
		if label.GetName() == name {
			return label.GetValue()
		}
	}
	return ""
}

func TestAttentionReportsEveryDeclaredKind(t *testing.T) {
	registry := prometheus.NewRegistry()
	attention, err := NewAttention(registry, []AttentionKind{
		{Kind: "a", Name: "甲", Severity: AttentionCritical},
		{Kind: "b", Name: "乙", Severity: AttentionWarning},
	})
	if err != nil {
		t.Fatal(err)
	}
	attention.Set("one", map[string][]string{"a": {"对象 2", "对象 1"}, "unknown": {"没有声明"}})
	attention.Set("two", map[string][]string{"a": {"对象 1"}})
	metrics := collectAttention(t, registry)
	if len(metrics) != 2 {
		t.Fatalf("应报两个种类（未声明的丢弃），实际 %d", len(metrics))
	}
	if got := metrics["a"].GetGauge().GetValue(); got != 1 {
		t.Fatalf("a 应为 1，实际 %v", got)
	}
	if got := labelOf(metrics["a"], "detail"); got != "对象 1；对象 2" {
		t.Fatalf("detail 应排序去重，实际 %q", got)
	}
	if labelOf(metrics["a"], "name") != "甲" || labelOf(metrics["a"], "severity") != AttentionCritical {
		t.Fatalf("名称、级别标签不对: %v", metrics["a"].GetLabel())
	}
	if metrics["b"].GetGauge().GetValue() != 0 || labelOf(metrics["b"], "detail") != "" || labelOf(metrics["b"], "severity") != AttentionWarning {
		t.Fatalf("b 应报 0、detail 为空、级别 warning: %v", metrics["b"])
	}
	// 来源清空后回到 0。
	attention.Set("one", nil)
	attention.Set("two", nil)
	if got := collectAttention(t, registry)["a"].GetGauge().GetValue(); got != 0 {
		t.Fatalf("恢复后应为 0，实际 %v", got)
	}
}

func TestAttentionComputedSourceRunsOnCollect(t *testing.T) {
	registry := prometheus.NewRegistry()
	attention, err := NewAttention(registry, []AttentionKind{{Kind: "lag", Name: "落后", Severity: AttentionCritical}})
	if err != nil {
		t.Fatal(err)
	}
	active := false
	attention.AddSource("lag", func(time.Time) map[string][]string {
		if !active {
			return nil
		}
		return map[string][]string{"lag": {"对象"}}
	})
	if collectAttention(t, registry)["lag"].GetGauge().GetValue() != 0 {
		t.Fatal("没有情况时应为 0")
	}
	active = true
	if collectAttention(t, registry)["lag"].GetGauge().GetValue() != 1 {
		t.Fatal("采集时现算，应翻成 1")
	}
}

func TestAttentionDetailWithinLimitAndValidUTF8(t *testing.T) {
	attention, err := NewAttention(nil, []AttentionKind{{Kind: "a", Name: "甲", Severity: AttentionCritical}})
	if err != nil {
		t.Fatal(err)
	}
	attention.Set("one", map[string][]string{"a": {strings.Repeat("组合账户暂停", 200)}})
	detail := attention.Active()["a"]
	if len(detail) > 512 || !utf8.ValidString(detail) {
		t.Fatalf("detail 长度 %d、合法 UTF-8 %v", len(detail), utf8.ValidString(detail))
	}
}

func TestNewAttentionRejectsBadDeclarations(t *testing.T) {
	for _, kinds := range [][]AttentionKind{
		{{Kind: "a", Name: "甲", Severity: "info"}},
		{{Kind: "a", Severity: AttentionCritical}},
		{{Kind: "a", Name: "甲", Severity: AttentionCritical}, {Kind: "a", Name: "乙", Severity: AttentionCritical}},
	} {
		if _, err := NewAttention(nil, kinds); err == nil {
			t.Fatalf("声明 %+v 应报错", kinds)
		}
	}
}
