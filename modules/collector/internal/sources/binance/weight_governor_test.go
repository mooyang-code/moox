package binance

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func headerWith(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Set(kv[i], kv[i+1])
	}
	return h
}

func TestWeightGovernorAdmitsBelowSoftLimit(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 10, 0, time.UTC)
	g := newWeightGovernor(func() time.Time { return now })
	g.Observe("api.binance.com", 200, headerWith("X-MBX-USED-WEIGHT-1M", "4000"))
	require.NoError(t, g.Admit(context.Background(), "api.binance.com"))
}

func TestWeightGovernorRejectsWhenSoftLimitWaitExceedsBudget(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 10, 0, time.UTC)
	g := newWeightGovernor(func() time.Time { return now })
	g.Observe("api.binance.com", 200, headerWith("X-MBX-USED-WEIGHT-1M", "4300"))
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(5*time.Second))
	defer cancel()
	err := g.Admit(ctx, "api.binance.com")
	require.ErrorIs(t, err, marketdata.ErrRateLimited)
	// 合约与现货分开计算。
	require.NoError(t, g.Admit(ctx, "fapi.binance.com"))
	// 下一分钟已用权重失效。
	now = now.Add(time.Minute)
	require.NoError(t, g.Admit(ctx, "api.binance.com"))
}

func TestWeightGovernorSwapUsesItsOwnLimit(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 10, 0, time.UTC)
	g := newWeightGovernor(func() time.Time { return now })
	g.Observe("fapi.binance.com", 200, headerWith("X-MBX-USED-WEIGHT-1M", "1700"))
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(time.Second))
	defer cancel()
	require.ErrorIs(t, g.Admit(ctx, "fapi.binance.com"), marketdata.ErrRateLimited)
}

func TestWeightGovernorBacksOffOn429AndStopsOn418(t *testing.T) {
	now := time.Date(2026, 10, 12, 12, 0, 10, 0, time.UTC)
	g := newWeightGovernor(func() time.Time { return now })
	g.Observe("api.binance.com", 429, headerWith("Retry-After", "30"))
	ctx, cancel := context.WithDeadline(context.Background(), now.Add(5*time.Second))
	defer cancel()
	require.ErrorIs(t, g.Admit(ctx, "api.binance.com"), marketdata.ErrRateLimited)

	g.Observe("api.binance.com", 418, headerWith("Retry-After", "120"))
	require.ErrorIs(t, g.Admit(context.Background(), "api.binance.com"), marketdata.ErrRateLimited, "封禁期内不等待、不请求")
	now = now.Add(121 * time.Second)
	require.NoError(t, g.Admit(context.Background(), "api.binance.com"))
}
