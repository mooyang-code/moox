package cnstock

import (
	"context"
	"sync"
	"time"
)

// fakeClock 是限流器测试用的假时钟：Sleep 不真的等待，只把时钟拨快并记下等了多久。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.sleeps = append(c.sleeps, d)
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

func (c *fakeClock) Sleeps() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.sleeps...)
}

// scenarioLimiter 给比对场景换上全新的、用假时钟的限流器（默认配置），限流器的等待不真的睡眠，也不记进场景的等待序列。
func scenarioLimiter(query *ClistQuery) {
	clock := newFakeClock()
	query.Limiter = newRequestLimiter(DefaultRequestLimitConfig(), nil, clock.Now, clock.Sleep)
}

// newTestLimiter 创建用假时钟的限流器，observer 收集到的事件按顺序记在返回的切片指针里。
func newTestLimiter(cfg RequestLimitConfig) (*RequestLimiter, *fakeClock, *[]LimitEvent) {
	clock := newFakeClock()
	var mu sync.Mutex
	events := &[]LimitEvent{}
	limiter := newRequestLimiter(cfg, func(event LimitEvent) {
		mu.Lock()
		*events = append(*events, event)
		mu.Unlock()
	}, clock.Now, clock.Sleep)
	return limiter, clock, events
}
