package marketfetch

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

// fakeFundingClock 是手动推进的时钟：Advance 依次触发到期的定时器。
type fakeFundingClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeFundingTimer
}

type fakeFundingTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (c *fakeFundingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeFundingClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeFundingTimer{at: c.now.Add(d), f: f}
	c.timers = append(c.timers, timer)
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		stopped := !timer.stopped
		timer.stopped = true
		return stopped
	}
}

func (c *fakeFundingClock) pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, timer := range c.timers {
		if !timer.stopped {
			count++
		}
	}
	return count
}

func (c *fakeFundingClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var due *fakeFundingTimer
		for index, timer := range c.timers {
			if !timer.at.After(target) {
				due = timer
				c.timers = append(c.timers[:index], c.timers[index+1:]...)
				break
			}
		}
		if due == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		if due.at.After(c.now) {
			c.now = due.at
		}
		c.mu.Unlock()
		if !due.stopped {
			due.f()
		}
	}
}

// fakeFundingSource 按 [start, end) 返回内存记录，记下每次查询；fail 非空时前几次返回错误。
type fakeFundingSource struct {
	mu       sync.Mutex
	rates    []marketdata.FundingRate
	queries  []fundingQuery
	failures int
}

type fundingQuery struct {
	symbol     string
	start, end time.Time
	realtime   bool
}

func (s *fakeFundingSource) FundingRates(ctx context.Context, symbol string, start, end time.Time) ([]marketdata.FundingRate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, fundingQuery{symbol: symbol, start: start, end: end, realtime: marketdata.IsRealtimeFunding(ctx)})
	if s.failures > 0 {
		s.failures--
		return nil, errors.New("上游暂时不可用")
	}
	var out []marketdata.FundingRate
	for _, rate := range s.rates {
		if (symbol == "" || rate.ProviderSymbol == symbol) && !rate.FundingTime.Before(start) && rate.FundingTime.Before(end) {
			out = append(out, rate)
		}
	}
	return out, nil
}

func (s *fakeFundingSource) queried() []fundingQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fundingQuery(nil), s.queries...)
}

func (s *fakeFundingSource) set(rates ...marketdata.FundingRate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rates = rates
}

var cacheBase = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

func fundingAt(symbol string, at time.Time, rate float64) marketdata.FundingRate {
	return marketdata.FundingRate{ProviderSymbol: symbol, FundingTime: at, Rate: rate}
}

func waitReady(t *testing.T, cache *FundingCache, symbol string, start, end time.Time) float64 {
	t.Helper()
	var value float64
	require.Eventually(t, func() bool {
		var ok bool
		value, ok = cache.Sum(symbol, start, end)
		return ok
	}, time.Second, time.Millisecond)
	return value
}

// 整点后约 2 分钟取上一个整点开始的这一小时；收盘前 5 分钟再取一次覆盖。
func TestFundingCacheFetchesAfterHourAndRefreshesBeforeClose(t *testing.T) {
	clock := &fakeFundingClock{now: cacheBase.Add(30 * time.Minute)}
	source := &fakeFundingSource{}
	cache := NewFundingCache(source, clock)
	cache.Activate(time.Hour)
	// 启动时补取：上一根与本根已过去的小时（00:00 这一小时已过 2 分钟）。
	waitReady(t, cache, "BTCUSDT", cacheBase.Add(-time.Hour), cacheBase.Add(time.Hour))
	require.Len(t, source.queried(), 2)

	source.set(fundingAt("BTCUSDT", cacheBase.Add(time.Hour+time.Millisecond), 0.0001))
	clock.Advance(32 * time.Minute) // 01:02
	require.Equal(t, 0.0001, waitReady(t, cache, "BTCUSDT", cacheBase.Add(time.Hour), cacheBase.Add(2*time.Hour)), "晚 1 毫秒的结算算在这一小时")
	_, ready := cache.Sum("ETHUSDT", cacheBase.Add(time.Hour), cacheBase.Add(2*time.Hour))
	require.True(t, ready, "就绪的小时槽里没有结算的合约按 0")

	// 晚公布的记录在收盘前 5 分钟的覆盖里补上。
	source.set(fundingAt("BTCUSDT", cacheBase.Add(time.Hour+time.Millisecond), 0.0001), fundingAt("ETHUSDT", cacheBase.Add(time.Hour), 0.0003))
	clock.Advance(53 * time.Minute) // 01:55
	require.Eventually(t, func() bool {
		value, _ := cache.Sum("ETHUSDT", cacheBase.Add(time.Hour), cacheBase.Add(2*time.Hour))
		return value == 0.0003
	}, time.Second, time.Millisecond)
	for _, query := range source.queried() {
		require.Empty(t, query.symbol, "缓存只做全市场查询")
		require.True(t, query.realtime, "缓存走实时优先级")
		require.Equal(t, time.Hour, query.end.Sub(query.start))
	}
}

// 默认关闭：没有启用时不做整点取数，只按需补取。
func TestFundingCacheIdleUntilActivated(t *testing.T) {
	clock := &fakeFundingClock{now: cacheBase.Add(30 * time.Minute)}
	source := &fakeFundingSource{}
	_ = NewFundingCache(source, clock)
	clock.Advance(3 * time.Hour)
	require.Empty(t, source.queried())
}

// 取数失败按退避重试，直到就绪。
func TestFundingCacheRetriesFailedSlot(t *testing.T) {
	clock := &fakeFundingClock{now: cacheBase.Add(time.Hour + 10*time.Second)}
	source := &fakeFundingSource{failures: 2, rates: []marketdata.FundingRate{fundingAt("BTCUSDT", cacheBase, 0.0002)}}
	cache := NewFundingCache(source, clock)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- cache.Await(ctx, cacheBase, cacheBase.Add(time.Hour)) }()
	require.Eventually(t, func() bool { return clock.pending() == 1 }, time.Second, time.Millisecond)
	require.Len(t, source.queried(), 1)
	clock.Advance(fundingRetryBase)
	require.Eventually(t, func() bool { return clock.pending() == 1 && len(source.queried()) == 2 }, time.Second, time.Millisecond)
	clock.Advance(2 * fundingRetryBase)
	require.NoError(t, <-done)
	value, ok := cache.Sum("BTCUSDT", cacheBase, cacheBase.Add(time.Hour))
	require.True(t, ok)
	require.Equal(t, 0.0002, value)
}

// 1d 取本根覆盖的 24 个小时槽，全部就绪才算就绪；按需补取只取缺的槽。
func TestFundingCacheDailySumsTwentyFourSlots(t *testing.T) {
	day := cacheBase
	clock := &fakeFundingClock{now: day.Add(24*time.Hour + 5*time.Second)}
	source := &fakeFundingSource{rates: []marketdata.FundingRate{
		fundingAt("BTCUSDT", day, 0.0001),
		fundingAt("BTCUSDT", day.Add(8*time.Hour+time.Millisecond), 0.0002),
		fundingAt("BTCUSDT", day.Add(16*time.Hour), -0.0001),
		fundingAt("BTCUSDT", day.Add(24*time.Hour), 0.0009), // 下一根
	}}
	cache := NewFundingCache(source, clock)
	_, ready := cache.Sum("BTCUSDT", day, day.Add(24*time.Hour))
	require.False(t, ready)
	notified := make(chan struct{})
	ready, _ = cache.Prepare(day, day.Add(24*time.Hour), func() { close(notified) })
	require.False(t, ready)
	<-notified
	value, ok := cache.Sum("BTCUSDT", day, day.Add(24*time.Hour))
	require.True(t, ok)
	require.InDelta(t, 0.0002, value, 1e-12)
	require.Len(t, source.queried(), 24)
	ready, _ = cache.Prepare(day, day.Add(24*time.Hour), func() { t.Fatal("已就绪时不回调") })
	require.True(t, ready)
	require.Len(t, source.queried(), 24, "就绪后不再取")
}

// 还没到可取数时刻的小时槽，等到整点后 2 分钟再取。
func TestFundingCacheDefersSlotUntilPublished(t *testing.T) {
	clock := &fakeFundingClock{now: cacheBase.Add(30 * time.Second)}
	source := &fakeFundingSource{rates: []marketdata.FundingRate{fundingAt("BTCUSDT", cacheBase, 0.0001)}}
	cache := NewFundingCache(source, clock)
	notified := make(chan struct{})
	ready, _ := cache.Prepare(cacheBase, cacheBase.Add(time.Hour), func() { close(notified) })
	require.False(t, ready)
	require.Empty(t, source.queried())
	clock.Advance(fundingPublishDelay)
	<-notified
	require.Len(t, source.queried(), 1)
}

// 启用时补取最长一根需要的小时槽；已因 1h 启用、后来又加了 1d（最长一根变长）时，按 1d 补取当天已过去的小时槽，已就绪的不重取。
func TestFundingCacheActivateBackfillsSlotsAndGrowsWithMaxStep(t *testing.T) {
	now := cacheBase.Add(5*time.Hour + 30*time.Minute) // 05:30
	clock := &fakeFundingClock{now: now}
	source := &fakeFundingSource{}
	cache := NewFundingCache(source, clock)
	cache.Activate(time.Hour)
	waitReady(t, cache, "BTCUSDT", cacheBase.Add(4*time.Hour), cacheBase.Add(6*time.Hour))
	require.Len(t, source.queried(), 2, "1h：上一根 04:00 与当前这一根 05:00")

	cache.Activate(time.Hour)
	require.Len(t, source.queried(), 2, "最长一根没变时不重复补取")

	cache.Activate(24 * time.Hour)
	// 前一天 24 个小时槽加上当天 00:00～05:00 共 30 个，其中 04:00、05:00 已就绪。
	waitReady(t, cache, "BTCUSDT", cacheBase.Add(-24*time.Hour), cacheBase.Add(6*time.Hour))
	require.Len(t, source.queried(), 30)
	starts := map[time.Time]int{}
	for _, query := range source.queried() {
		starts[query.start]++
	}
	for start, count := range starts {
		require.Equal(t, 1, count, "小时槽 %s 只取一次", start)
	}
}

// 等待撤销后，取数失败的小时槽不再无限重试（启用整点取数时只重试最近的小时槽）。
func TestFundingCacheCanceledWaitStopsRetry(t *testing.T) {
	clock := &fakeFundingClock{now: cacheBase.Add(time.Hour + 10*time.Second)}
	source := &fakeFundingSource{failures: 100}
	cache := NewFundingCache(source, clock)
	ready, cancel := cache.Prepare(cacheBase, cacheBase.Add(time.Hour), func() {})
	require.False(t, ready)
	require.Eventually(t, func() bool { return clock.pending() == 1 }, time.Second, time.Millisecond)
	cancel()
	clock.Advance(fundingRetryBase)
	require.Eventually(t, func() bool { return len(source.queried()) == 2 }, time.Second, time.Millisecond)
	time.Sleep(10 * time.Millisecond)
	require.Zero(t, clock.pending(), "没人等了就不再安排重试")
}
