package marketdata

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAdaptiveRateBacksOffOnPressureAndRecovers(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 100, clock)
	require.Equal(t, 100.0, a.Rate(), "从配置的上限开始")

	a.Observe(ErrRateLimited)
	require.Equal(t, 50.0, a.Rate(), "明确限流减半")

	// 同一批并发请求一起失败，只算一次压力信号。
	a.Observe(ErrRateLimited)
	a.Observe(ErrRateLimited)
	require.Equal(t, 50.0, a.Rate())

	clock.Advance(adaptiveDownGap)
	a.Observe(fmt.Errorf("读超时：%w", ErrTimeout))
	require.Equal(t, 40.0, a.Rate(), "超时、断连是弱信号，降 20%")

	// 与速率无关的错误既不算成功也不算压力。
	for i := 0; i < 50; i++ {
		a.Observe(ErrNoClosedBar)
	}
	clock.Advance(adaptiveHold)
	require.Equal(t, 40.0, a.Rate())

	// 降速之后冷静期内不升；冷静期过后连续成功才升。
	clock.Advance(-adaptiveHold)
	for i := 0; i < adaptiveStreak; i++ {
		a.Observe(nil)
	}
	require.Equal(t, 40.0, a.Rate(), "冷静期内不升")
	clock.Advance(adaptiveHold)
	for i := 0; i < adaptiveStreak; i++ {
		a.Observe(nil)
	}
	require.InDelta(t, 44.0, a.Rate(), 1e-9)

	// 失败打断连续成功的计数。
	clock.Advance(adaptiveDownGap)
	for i := 0; i < adaptiveStreak-1; i++ {
		a.Observe(nil)
	}
	a.Observe(ErrHTTPStatus)
	clock.Advance(adaptiveHold)
	a.Observe(nil)
	require.Less(t, a.Rate(), 44.0)
}

func TestAdaptiveRateStaysWithinFloorAndMax(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 50, clock)
	for i := 0; i < 20; i++ {
		clock.Advance(adaptiveDownGap)
		a.Observe(ErrRateLimited)
	}
	require.Equal(t, 5.0, a.Rate(), "下限是上限的 10%")
	small := newAdaptiveRate("p", 4, clock)
	clock.Advance(adaptiveDownGap)
	small.Observe(ErrRateLimited)
	clock.Advance(adaptiveDownGap)
	small.Observe(ErrRateLimited)
	clock.Advance(adaptiveDownGap)
	small.Observe(ErrRateLimited)
	require.Equal(t, 1.0, small.Rate(), "绝对下限每秒 1 次")

	for i := 0; i < 5000; i++ {
		clock.Advance(100 * time.Millisecond)
		a.Observe(nil)
	}
	require.Equal(t, 50.0, a.Rate(), "一直成功时回到上限，不会超过")
}

// 模拟一个隐藏了限额的行情源（每秒最多 L 次，超过就返回限流）：自适应速率应收敛到限额附近，限流占比很小。
func TestAdaptiveRateConvergesNearAHiddenLimit(t *testing.T) {
	const hidden = 30.0
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 200, clock)
	var window []time.Time
	total, limited := 0, 0
	var sum float64
	samples := 0
	start := clock.Now()
	end := start.Add(10 * time.Minute)
	for clock.Now().Before(end) {
		now := clock.Now()
		for len(window) > 0 && now.Sub(window[0]) >= time.Second {
			window = window[1:]
		}
		total++
		if float64(len(window)) >= hidden {
			limited++
			a.Observe(ErrRateLimited)
		} else {
			window = append(window, now)
			a.Observe(nil)
		}
		clock.Advance(time.Duration(float64(time.Second) / a.Rate()))
		if clock.Now().Before(start.Add(5 * time.Minute)) {
			continue // 前 5 分钟是探索期，只统计后 5 分钟
		}
		sum += a.Rate()
		samples++
	}
	require.Less(t, float64(limited)/float64(total), 0.15, "限流占比")
	require.Positive(t, samples)
	avg := sum / float64(samples)
	t.Logf("隐藏限额 %.0f/s：后 5 分钟平均速率 %.1f/s，限流占比 %.1f%%", hidden, avg, 100*float64(limited)/float64(total))
	require.Greater(t, avg, 0.4*hidden, "平均速率不能太低")
	require.Less(t, avg, 1.5*hidden, "平均速率不能远超限额")
}

// 守卫在自适应时按实时速率放行：被限流降速后，同样的请求数需要更长的等待。
func TestFeedGuardFollowsTheAdaptiveRate(t *testing.T) {
	run := func(adaptive *AdaptiveRate) time.Duration {
		clock := newFakeClock(time.Unix(1_000, 0))
		sleeper := &fakeSleeper{clock: clock}
		guard, err := NewFeedGuard(RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Millisecond, RequestTimeout: time.Second}, clock, sleeper.Sleep)
		require.NoError(t, err)
		guard.adaptive = adaptive
		start := clock.Now()
		for i := 0; i < 11; i++ {
			require.NoError(t, guard.Do(context.Background(), func(context.Context) error { return nil }))
		}
		return clock.Now().Sub(start)
	}
	fixed := run(nil)
	require.InDelta(t, 100*time.Millisecond, float64(fixed), float64(5*time.Millisecond), "每秒 100 次，10 个间隔约 100ms")

	slowClock := newFakeClock(time.Unix(1_000, 0))
	slow := newAdaptiveRate("p", 100, slowClock)
	slow.Observe(ErrRateLimited)
	require.Equal(t, 50.0, slow.Rate())
	require.InDelta(t, 200*time.Millisecond, float64(run(slow)), float64(5*time.Millisecond), "降到每秒 50 次后约 200ms")
}

func TestFeedGuardReportsPressureToTheAdaptiveRate(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 100, clock)
	sleeper := &fakeSleeper{clock: clock}
	guard, err := NewFeedGuard(RateLimitPolicy{RequestsPerSecond: 100, Burst: 1, MaxConcurrent: 1, Cooldown: time.Millisecond, RequestTimeout: time.Second}, clock, sleeper.Sleep)
	require.NoError(t, err)
	guard.adaptive = a
	require.ErrorIs(t, guard.Do(context.Background(), func(context.Context) error { return fmt.Errorf("上游返回 429：%w", ErrRateLimited) }), ErrRateLimited)
	require.Equal(t, 50.0, a.Rate())
	require.Error(t, guard.Do(context.Background(), func(context.Context) error { return errors.New("解析失败") }))
	require.Equal(t, 50.0, a.Rate(), "与速率无关的错误不调节")
}

// 路由只对补采类请求（Adaptive）使用共享的自适应速率，实时请求仍用配置里的固定值。
func TestRouterUsesAdaptiveRateOnlyForAdaptiveRequests(t *testing.T) {
	adaptiveRates.Delete("tdx-test")
	t.Cleanup(func() { adaptiveRates.Delete("tdx-test") })
	router, err := NewRouter(NewRegistry(), 3, nil, nil)
	require.NoError(t, err)
	policy := RateLimitPolicy{RequestsPerSecond: 40, Burst: 4, MaxConcurrent: 2, Cooldown: time.Second, RequestTimeout: time.Second}
	realtime, err := router.feedGuard("tdx-test", policy, 0, false)
	require.NoError(t, err)
	require.Nil(t, realtime.adaptive)
	backfill, err := router.feedGuard("tdx-test", policy, 0, true)
	require.NoError(t, err)
	require.NotNil(t, backfill.adaptive)
	require.Equal(t, 40.0, backfill.adaptive.Rate(), "以配置的上限作为最大值")
	again, err := router.feedGuard("tdx-test", policy, 0, true)
	require.NoError(t, err)
	require.Same(t, backfill, again)
	other, err := NewRouter(NewRegistry(), 3, nil, nil)
	require.NoError(t, err)
	shared, err := other.feedGuard("tdx-test", policy, 0, true)
	require.NoError(t, err)
	require.Same(t, backfill.adaptive, shared.adaptive, "不同 Router（每次调用新建）共享同一个自适应速率")
}
