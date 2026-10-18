// Package pacing keeps the archive worker from competing with market-data writes: requests are only
// started inside an allowed window of every minute, never around the top of the hour, and all runs
// share one bandwidth token bucket.
package pacing

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config 是节奏配置（worker 配置 pacing 段）。
type Config struct {
	// RequestSeconds 是每分钟内允许发起请求的秒区间 [from, to]（含）。
	RequestSeconds [2]int
	// DisableMinuteWindow 为 true 时不再限制每分钟内的秒区间（整点静默、带宽和请求间隔仍然生效）。默认 false，即启用。
	DisableMinuteWindow bool
	// HourQuiet 是每小时内不发起请求的区间 [from, to)，格式 "MM:SS"，from 大于 to 表示跨整点。
	HourQuiet [2]string
	// RequestInterval 是同一运行两次请求之间的最小间隔。
	RequestInterval time.Duration
	// MaxBytesPerSecond 是所有并发运行共享的带宽上限（按解压后的响应字节计）。
	MaxBytesPerSecond int64
}

// Default 返回默认节奏：每分钟 25–55 秒、整点 :58～:04 避让、请求间隔 200ms、512KB/s。
func Default() Config {
	return Config{RequestSeconds: [2]int{25, 55}, HourQuiet: [2]string{"58:00", "04:00"}, RequestInterval: 200 * time.Millisecond, MaxBytesPerSecond: 512 << 10}
}

// Validate 检查配置。
func (c Config) Validate() error {
	if c.RequestSeconds[0] < 0 || c.RequestSeconds[1] > 59 || c.RequestSeconds[0] > c.RequestSeconds[1] {
		return fmt.Errorf("pacing.request_seconds must be an increasing range inside 0..59")
	}
	if _, err := parseMinuteSecond(c.HourQuiet[0]); err != nil {
		return fmt.Errorf("pacing.hour_quiet[0]: %w", err)
	}
	if _, err := parseMinuteSecond(c.HourQuiet[1]); err != nil {
		return fmt.Errorf("pacing.hour_quiet[1]: %w", err)
	}
	if c.RequestInterval < 0 {
		return fmt.Errorf("pacing.request_interval must not be negative")
	}
	if c.MaxBytesPerSecond <= 0 {
		return fmt.Errorf("pacing.max_bytes_per_second must be positive")
	}
	return nil
}

func parseMinuteSecond(value string) (int, error) {
	minute, second, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok {
		return 0, fmt.Errorf("%q must be MM:SS", value)
	}
	m, err1 := strconv.Atoi(minute)
	s, err2 := strconv.Atoi(second)
	if err1 != nil || err2 != nil || m < 0 || m > 59 || s < 0 || s > 59 {
		return 0, fmt.Errorf("%q must be MM:SS", value)
	}
	return m*60 + s, nil
}

// Clock 抽象时间，测试用。
type Clock struct {
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error
}

func realClock() Clock {
	return Clock{Now: time.Now, Sleep: func(ctx context.Context, d time.Duration) error {
		if d <= 0 {
			return ctx.Err()
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}}
}

// Pacer 同时实现时间窗避让、请求间隔和共享带宽令牌桶。
type Pacer struct {
	cfg   Config
	clock Clock
	quiet [2]int

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// New 创建 Pacer；clock 为零值时使用真实时钟。
func New(cfg Config, clock *Clock) (*Pacer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p := &Pacer{cfg: cfg, clock: realClock()}
	if clock != nil {
		p.clock = *clock
	}
	p.quiet[0], _ = parseMinuteSecond(cfg.HourQuiet[0])
	p.quiet[1], _ = parseMinuteSecond(cfg.HourQuiet[1])
	p.tokens = float64(cfg.MaxBytesPerSecond)
	p.last = p.clock.Now()
	return p, nil
}

// Interval 返回同一运行两次请求之间的最小间隔。
func (p *Pacer) Interval() time.Duration { return p.cfg.RequestInterval }

func (p *Pacer) inQuiet(offset int) bool {
	start, end := p.quiet[0], p.quiet[1]
	if start == end {
		return false
	}
	if start > end {
		return offset >= start || offset < end
	}
	return offset >= start && offset < end
}

// AllowedAt 返回不早于 t 的第一个允许发起请求的时刻。
func (p *Pacer) AllowedAt(t time.Time) time.Time {
	lo, hi := p.cfg.RequestSeconds[0], p.cfg.RequestSeconds[1]
	for i := 0; i < 16; i++ {
		hour := t.Truncate(time.Hour)
		offset := t.Minute()*60 + t.Second()
		if p.inQuiet(offset) {
			// 跳到静默区间结束：同一小时内结束，或跨到下一个小时。
			end := p.quiet[1]
			next := hour.Add(time.Duration(end) * time.Second)
			if !next.After(t) {
				next = hour.Add(time.Hour).Add(time.Duration(end) * time.Second)
			}
			t = next
			continue
		}
		if p.cfg.DisableMinuteWindow {
			return t
		}
		minute := t.Truncate(time.Minute)
		switch {
		case t.Second() < lo:
			t = minute.Add(time.Duration(lo) * time.Second)
		case t.Second() > hi:
			t = minute.Add(time.Minute).Add(time.Duration(lo) * time.Second)
		default:
			return t
		}
	}
	return t
}

// WaitTurn 阻塞到当前允许发起请求的时刻。
func (p *Pacer) WaitTurn(ctx context.Context) error {
	for {
		now := p.clock.Now()
		at := p.AllowedAt(now)
		if !at.After(now) {
			return ctx.Err()
		}
		if err := p.clock.Sleep(ctx, at.Sub(now)); err != nil {
			return err
		}
	}
}

// Throttle 记入一次响应的字节数，超出共享带宽上限时睡眠补足。
func (p *Pacer) Throttle(ctx context.Context, bytes int64) error {
	if bytes <= 0 {
		return ctx.Err()
	}
	p.mu.Lock()
	now := p.clock.Now()
	rate := float64(p.cfg.MaxBytesPerSecond)
	p.tokens += now.Sub(p.last).Seconds() * rate
	if p.tokens > rate {
		p.tokens = rate
	}
	p.last = now
	p.tokens -= float64(bytes)
	var wait time.Duration
	if p.tokens < 0 {
		wait = time.Duration(-p.tokens / rate * float64(time.Second))
	}
	p.mu.Unlock()
	return p.clock.Sleep(ctx, wait)
}

// Sleep 按时钟睡眠（请求间隔使用）。
func (p *Pacer) Sleep(ctx context.Context, d time.Duration) error { return p.clock.Sleep(ctx, d) }
