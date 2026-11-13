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
	// bothCooling 是两个主机都在冷却时关注项的 detail。
	bothCooling = "push2.eastmoney.com 与 push2test.eastmoney.com 都在冷却，东方财富标的列表同步暂缓"
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

	// push2 单独冷却：FetchClist 会回退 push2test，同步照常，只记日志与指标，不上报关注项。
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventCooldownStarted, Host: host1, Cooldown: 10 * time.Minute, Level: 1, Reason: cnstock.ReasonTransportFailure, Failures: 5})
	require.Equal(t, 1.0, testutil.ToFloat64(observer.cooldowns.WithLabelValues(host1)))
	require.Equal(t, 1.0, testutil.ToFloat64(observer.cooling.WithLabelValues(host1)))
	require.Equal(t, 0.0, attentionValue(attn, bothCooling), "push2 单独冷却不上报")
	require.Equal(t, 0.0, attentionValue(attn, ""))

	// push2test 也进入冷却（探测失败的 429，Failures 为 0）：同步真的做不了，上报；detail 是固定文字，不含时长与次数。
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventCooldownStarted, Host: host2, Cooldown: 20 * time.Minute, Level: 2, Reason: cnstock.ReasonRateLimited})
	require.Equal(t, 1.0, attentionValue(attn, bothCooling))

	// 任一恢复后清零。
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventProbe, Host: host1, Level: 1})
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventRecovered, Host: host1})
	require.Equal(t, 0.0, testutil.ToFloat64(observer.cooling.WithLabelValues(host1)))
	require.Equal(t, 0.0, attentionValue(attn, bothCooling))

	// push2 再次冷却时又是两个都冷却，恢复 push2test 同样清零；push2his 的冷却不影响关注项。
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventCooldownStarted, Host: host1, Cooldown: 10 * time.Minute, Level: 1, Reason: cnstock.ReasonForbidden})
	require.Equal(t, 1.0, attentionValue(attn, bothCooling))
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventRecovered, Host: host2})
	require.Equal(t, 0.0, attentionValue(attn, bothCooling))
	observer.Observe(cnstock.LimitEvent{Kind: cnstock.EventCooldownStarted, Host: "push2his.eastmoney.com", Cooldown: time.Minute, Level: 1, Reason: cnstock.ReasonForbidden})
	require.Equal(t, 0.0, attentionValue(attn, bothCooling))
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
	require.Equal(t, 0.0, attentionValue(attn, bothCooling), "push2 单独冷却不上报")

	// push2test 也连续失败 5 次：两个都冷却，上报。
	for i := 0; i < 5; i++ {
		release, err := limiter.Acquire(t.Context(), host2)
		require.NoError(t, err)
		release(cnstock.Outcome{Kind: cnstock.OutcomeTransportFailure})
	}
	require.Equal(t, 1.0, attentionValue(attn, bothCooling))
}
