package stream

import (
	"sort"
	"sync"
	"time"
)

// Clock 是闸门与连接管理使用的时钟；生产用本机时钟（单调时钟计时），测试注入假时钟。
type Clock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) Stopper
}

// Stopper 取消一个尚未触发的定时器。
type Stopper interface{ Stop() bool }

// SystemClock 是本机时钟。
type SystemClock struct{}

func (SystemClock) Now() time.Time                              { return time.Now() }
func (SystemClock) AfterFunc(d time.Duration, f func()) Stopper { return time.AfterFunc(d, f) }

// FakeClock 是测试用时钟：Advance 时按时间顺序同步触发到期的定时器。
type FakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
	clock   *FakeClock
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}

// NewFakeClock 返回停在 now 的假时钟。
func NewFakeClock(now time.Time) *FakeClock { return &FakeClock{now: now} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) AfterFunc(d time.Duration, f func()) Stopper {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &fakeTimer{at: c.now.Add(d), f: f, clock: c}
	c.timers = append(c.timers, timer)
	return timer
}

// Advance 把时间推进 d，按到期先后执行定时器（定时器里新建的定时器只要在区间内也会执行）。
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	target := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		sort.SliceStable(c.timers, func(i, j int) bool { return c.timers[i].at.Before(c.timers[j].at) })
		var next *fakeTimer
		for index, timer := range c.timers {
			if timer.stopped {
				continue
			}
			if !timer.at.After(target) {
				next = timer
				c.timers = append(c.timers[:index:index], c.timers[index+1:]...)
				break
			}
		}
		if next == nil {
			c.now = target
			c.mu.Unlock()
			return
		}
		if next.at.After(c.now) {
			c.now = next.at
		}
		next.stopped = true
		c.mu.Unlock()
		next.f()
	}
}
