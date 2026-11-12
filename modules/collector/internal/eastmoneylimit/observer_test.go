package eastmoneylimit

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/attention"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

const (
	host1 = "push2.eastmoney.com"
	host2 = "push2test.eastmoney.com"
)

func attentionValue(a *attention.Attention, detail string) float64 {
	return testutil.ToFloat64(a.Gauge().WithLabelValues(attention.KindEastMoneyCooling, detail))
}

// 事件对应的指标：请求数、等待、拒绝、冷却次数与状态；冷却时上报关注项，恢复后回到 0。
func TestObserverMetricsAndAttention(t *testing.T) {
	registry := prometheus.NewRegistry()
	attn := attention.New(registry)
	observer := New(registry, attn)

	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventSent, Host: host1, Wait: 500 * time.Millisecond})
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventSent, Host: host1})
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventRejected, Host: host1})
	require.Equal(t, 2.0, testutil.ToFloat64(observer.requests.WithLabelValues(host1)))
	require.Equal(t, 1.0, testutil.ToFloat64(observer.rejected.WithLabelValues(host1)))
	require.Equal(t, 0.0, testutil.ToFloat64(observer.cooling.WithLabelValues(host1)))

	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventCooldownStarted, Host: host1, Cooldown: 10 * time.Minute, Level: 1, Reason: cnstock.ReasonTransportFailure, Failures: 5})
	require.Equal(t, 1.0, testutil.ToFloat64(observer.cooldowns.WithLabelValues(host1)))
	require.Equal(t, 1.0, testutil.ToFloat64(observer.cooling.WithLabelValues(host1)))
	require.Equal(t, 1.0, attentionValue(attn, host1+" 冷却 10m0s（第 1 次，连续传输层失败）"))

	// 第二个主机也进入冷却：详情合并在同一条关注项里。
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventCooldownStarted, Host: host2, Cooldown: 20 * time.Minute, Level: 2, Reason: cnstock.ReasonRateLimited})
	merged := host1 + " 冷却 10m0s（第 1 次，连续传输层失败）；" + host2 + " 冷却 20m0s（第 2 次，HTTP 429）"
	require.Equal(t, 1.0, attentionValue(attn, merged))

	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventProbe, Host: host1, Level: 1})
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventRecovered, Host: host1})
	require.Equal(t, 0.0, testutil.ToFloat64(observer.cooling.WithLabelValues(host1)))
	require.Equal(t, 1.0, attentionValue(attn, host2+" 冷却 20m0s（第 2 次，HTTP 429）"))
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventRecovered, Host: host2})
	require.Equal(t, 0.0, attentionValue(attn, ""), "全部恢复后关注项回到 0")
}

// 主机标签只取已知的几个，其余记为 other，不会因为意外的主机名产生新的时间序列。
func TestObserverHostLabelIsBounded(t *testing.T) {
	registry := prometheus.NewRegistry()
	observer := New(registry, nil)
	for _, host := range []string{"a.eastmoney.com", "b.eastmoney.com", "eastmoney.com"} {
		observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventSent, Host: host})
	}
	require.Equal(t, 3.0, testutil.ToFloat64(observer.requests.WithLabelValues("other")))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				require.Equal(t, "host", label.GetName(), "指标只允许 host 标签")
				require.Contains(t, append([]string{otherHost}, knownHosts...), label.GetValue())
			}
		}
	}
}

// 与真实限流器连起来：连续 5 次传输失败触发冷却事件，关注项上报，之后被拒绝的请求计数。
func TestObserverWithRealLimiter(t *testing.T) {
	registry := prometheus.NewRegistry()
	attn := attention.New(registry)
	observer := New(registry, attn)
	cfg := cnstock.DefaultRequestLimitConfig()
	cfg.RequestsPerSecond, cfg.Burst = 20, 50
	limiter, err := cnstock.NewRequestLimiter(cfg, observer.Observe)
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		release, err := limiter.Acquire(t.Context(), host1)
		require.NoError(t, err)
		release(cnstock.Outcome{Kind: cnstock.OutcomeTransportFailure})
	}
	_, err = limiter.Acquire(t.Context(), host1)
	require.ErrorIs(t, err, cnstock.ErrCoolingDown)
	require.Equal(t, 5.0, testutil.ToFloat64(observer.requests.WithLabelValues(host1)))
	require.Equal(t, 1.0, testutil.ToFloat64(observer.rejected.WithLabelValues(host1)))
	require.Equal(t, 1.0, testutil.ToFloat64(observer.cooling.WithLabelValues(host1)))
	require.Equal(t, 1.0, attentionValue(attn, host1+" 冷却 10m0s（第 1 次，连续传输层失败）"))
}
