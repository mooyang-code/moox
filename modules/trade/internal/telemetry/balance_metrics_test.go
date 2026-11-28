package telemetry

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// 余额同步的关注项：连续失败 3 次、超过 15 分钟没成功、余额差异超过上限都报；detail 只写账户与最近成功时刻，不随失败次数变。
func TestBalanceAttention(t *testing.T) {
	metrics, err := NewBalanceMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	failure := errors.New("Exchange unavailable")
	metrics.Observe("account-1", now, 0.03, nil)
	metrics.Observe("account-2", now, 0.01, nil)
	require.Equal(t, float64(2), testutil.ToFloat64(metrics.runs.WithLabelValues("success")))
	require.Nil(t, metrics.Attention("k", 0.05, now.Add(time.Minute)))

	for i := 1; i <= 2; i++ {
		metrics.Observe("account-2", now.Add(time.Duration(i)*time.Minute), 0, failure)
	}
	require.Nil(t, metrics.Attention("k", 0.05, now.Add(3*time.Minute)), "失败不到 3 次不报")
	metrics.Observe("account-2", now.Add(3*time.Minute), 0, failure)
	first := metrics.Attention("k", 0.05, now.Add(3*time.Minute))["k"]
	require.Len(t, first, 1)
	require.True(t, strings.HasPrefix(first[0], "account-2 连续 3 次以上同步失败（最近成功 07-28 12:00 UTC）"), first[0])
	metrics.Observe("account-2", now.Add(4*time.Minute), 0, failure)
	require.Equal(t, first, metrics.Attention("k", 0.05, now.Add(4*time.Minute))["k"], "多失败一次 detail 不变")

	// 超过 15 分钟没成功：随时间翻成 1。
	require.Len(t, metrics.Attention("k", 0.05, now.Add(16*time.Minute))["k"], 2)

	metrics.Observe("account-1", now.Add(5*time.Minute), 0.2, nil)
	metrics.Observe("account-2", now.Add(5*time.Minute), 0.01, nil)
	items := metrics.Attention("k", 0.05, now.Add(6*time.Minute))["k"]
	require.Equal(t, []string{"account-1 本地与交易所余额差异超过 5.00%"}, items)

	metrics.Remove("account-1")
	require.Nil(t, metrics.Attention("k", 0.05, now.Add(6*time.Minute)))
}
