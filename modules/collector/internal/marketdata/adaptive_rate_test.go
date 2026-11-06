package marketdata

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// run 在 duration 内均匀发出 n 个请求，结果都是 err，返回时时钟走到周期末尾。
func run(a *AdaptiveRate, clock *fakeClock, n int, duration time.Duration, err error) {
	for i := 0; i < n; i++ {
		a.Observe(err)
		clock.Advance(duration / time.Duration(n))
	}
}

func TestAdaptiveRateBacksOffImmediatelyOnRateLimiting(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 100, clock)
	require.Equal(t, 100.0, a.Rate(), "从配置的上限开始")

	a.Observe(ErrRateLimited)
	require.Equal(t, 50.0, a.Rate(), "明确限流立即减半")

	// 同一批并发请求一起被限流，只算一次。
	a.Observe(ErrRateLimited)
	a.Observe(fmt.Errorf("繁忙：%w", ErrRemoteBusy))
	require.Equal(t, 50.0, a.Rate())

	clock.Advance(adaptiveTick)
	a.Observe(ErrRateLimited)
	require.Equal(t, 25.0, a.Rate(), "过了一个周期再被限流，再减半")
}

func TestAdaptiveRateToleratesSmallSamplesAndSmallFailureShares(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 100, clock)

	// 样本太少（不足 5 个）：一个超时不算数。
	a.Observe(ErrTimeout)
	a.Observe(nil)
	a.Observe(nil)
	clock.Advance(adaptiveTick)
	require.Equal(t, 100.0, a.Rate())

	// 20 个请求里 1 个超时（5%）在容忍范围内：不降。
	for i := 0; i < 19; i++ {
		a.Observe(nil)
	}
	a.Observe(ErrTimeout)
	clock.Advance(adaptiveTick)
	require.Equal(t, 100.0, a.Rate())

	// 10 个请求里 5 个超时、断连：占比远超容忍值加 3 倍标准误差，降 20%。
	for i := 0; i < 5; i++ {
		a.Observe(nil)
		a.Observe(fmt.Errorf("读超时：%w", ErrTimeout))
	}
	clock.Advance(adaptiveTick)
	require.InDelta(t, 80.0, a.Rate(), 1e-9)

	// 整周期都失败（对方挂了）也只降 20%，之后每个周期继续降。
	run(a, clock, 10, adaptiveTick, ErrTCP)
	require.InDelta(t, 64.0, a.Rate(), 1e-9)
	run(a, clock, 10, adaptiveTick, ErrHTTPStatus)
	require.InDelta(t, 51.2, a.Rate(), 1e-9)

	// 与速率无关的错误既不算成功也不算压力。
	before := a.Rate()
	for i := 0; i < 50; i++ {
		a.Observe(ErrNoClosedBar)
	}
	clock.Advance(adaptiveTick)
	require.Equal(t, before, a.Rate())
}

func TestAdaptiveRateRecoversOnlyWhenTrafficUsesTheRate(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 100, clock)
	a.Observe(ErrRateLimited) // 50
	require.Equal(t, 50.0, a.Rate())

	// 降速后 3 秒内不升。
	run(a, clock, 50, adaptiveTick, nil)
	run(a, clock, 50, adaptiveTick, nil)
	require.Equal(t, 50.0, a.Rate(), "冷静期内不升")

	// 冷静期过后：连续 2 个健康周期且流量用到速率，升 10%。
	run(a, clock, 50, adaptiveTick, nil)
	run(a, clock, 50, adaptiveTick, nil)
	require.InDelta(t, 55.0, a.Rate(), 1e-9)

	// 流量只有 10/s、远没有用满 55/s：不升（流量被别的因素限制住了）。
	before := a.Rate()
	for i := 0; i < 6; i++ {
		run(a, clock, 10, adaptiveTick, nil)
	}
	require.Equal(t, before, a.Rate(), "没有用满速率不升")

	// 有弱信号（没超标）的周期不算健康，打断连续计数：健康周期与带一个超时的周期交替，一直不升。
	timeoutTick := func() {
		for i := 0; i < 39; i++ {
			a.Observe(nil)
		}
		a.Observe(ErrTimeout)
		clock.Advance(adaptiveTick)
	}
	timeoutTick()
	for i := 0; i < 5; i++ {
		run(a, clock, 40, adaptiveTick, nil)
		timeoutTick()
	}
	require.Equal(t, before, a.Rate())
}

func TestAdaptiveRateStaysWithinFloorAndMax(t *testing.T) {
	clock := newFakeClock(time.Unix(1_000, 0))
	a := newAdaptiveRate("p", 50, clock)
	for i := 0; i < 20; i++ {
		clock.Advance(adaptiveTick)
		a.Observe(ErrRateLimited)
	}
	require.Equal(t, 5.0, a.Rate(), "下限是上限的 10%")
	small := newAdaptiveRate("p", 4, clock)
	for i := 0; i < 4; i++ {
		clock.Advance(adaptiveTick)
		small.Observe(ErrRateLimited)
	}
	require.Equal(t, 1.0, small.Rate(), "绝对下限每秒 1 次")

	for i := 0; i < 400; i++ {
		run(a, clock, int(math.Ceil(a.Rate())), adaptiveTick, nil)
	}
	require.Equal(t, 50.0, a.Rate(), "一直健康且用满时回到上限，不会超过")
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
