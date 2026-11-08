package report

import "testing"

// 各模块的“关注项”指标由 Monitor 生成检查并发告警：不在白名单里，上报时就被丢掉，告警永远不会触发。
func TestAttentionMetricsAreMonitored(t *testing.T) {
	for _, name := range []string{"moox_trade_attention_active", "moox_callbackdispatcher_attention_active", "moox_collector_attention_active"} {
		if !IsMonitoredMetric(name) {
			t.Errorf("%s 必须在上报白名单里", name)
		}
	}
	if IsMonitoredMetric("moox_callbackdispatcher_event_errors_total") {
		t.Error("事件中心的事件错误计数没有 Monitor 消费方，不应该上报")
	}
}
