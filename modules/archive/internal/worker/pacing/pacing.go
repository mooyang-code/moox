// Package pacing 让归档 worker 读取 Storage 时不与行情写入抢资源：每分钟只在第 25～55 秒发起请求（避开采集函数在第 0 秒的
// 触发与写入），每小时 :58～:04 不发（避开 1h 及以上数据集在整点的集中收盘），所有运行共用一个带宽令牌桶。
// 两个窗口写死在 internal/schedule，与 mgr 的整点跳过同一份；只有每分钟的秒窗口可用 pacing.minute_window 关掉。
package pacing

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
)

// Config 是节奏配置（worker 配置 pacing 段）。
type Config struct {
	// MinuteWindow 为 true 时只在每分钟第 25～55 秒发起请求；为 false 时整分钟都可发（整点窗口、带宽和请求间隔仍然生效）。
	MinuteWindow bool
	// RequestInterval 是同一运行两次请求之间的最小间隔。
	RequestInterval time.Duration
	// MaxBytesPerSecond 是所有并发运行共享的带宽上限（按解压后的响应字节计）。
	MaxBytesPerSecond int64
}

// Default 返回默认节奏：按秒窗口避让、请求间隔 200ms、512KB/s。
func Default() Config {
	return Config{MinuteWindow: true, RequestInterval: 200 * time.Millisecond, MaxBytesPerSecond: 512 << 10}
}

// Validate 检查配置。
func (c Config) Validate() error {
	if c.RequestInterval < 0 {
		return fmt.Errorf("pacing.request_interval 不能为负")
	}
	if c.MaxBytesPerSecond <= 0 {
		return fmt.Errorf("pacing.max_bytes_per_second 必须为正")
	}
	return nil
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

	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// New 创建 Pacer；clock 为 nil 时使用真实时钟。
func New(cfg Config, clock *Clock) (*Pacer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p := &Pacer{cfg: cfg, clock: realClock()}
	if clock != nil {
		p.clock = *clock
	}
	p.tokens = float64(cfg.MaxBytesPerSecond)
	p.last = p.clock.Now()
	return p, nil
}

// Interval 返回同一运行两次请求之间的最小间隔。
func (p *Pacer) Interval() time.Duration { return p.cfg.RequestInterval }

// AllowedAt 返回不早于 t 的第一个允许发起请求的时刻（UTC）。
func (p *Pacer) AllowedAt(t time.Time) time.Time {
	t = t.UTC()
	// 每一步都只往后跳；最长的路径是 :57:55 → :58:25（落进整点窗口）→ :04:00 → :04:25，循环上限只为防御。
	for i := 0; i < 8; i++ {
		if schedule.InHourQuiet(t) {
			t = schedule.HourQuietEnd(t)
			continue
		}
		if !p.cfg.MinuteWindow {
			return t
		}
		minute := t.Truncate(time.Minute)
		switch offset := t.Sub(minute); {
		case offset < schedule.AllowFromSecond*time.Second:
			t = minute.Add(schedule.AllowFromSecond * time.Second)
		case offset >= schedule.AllowUntilSecond*time.Second:
			t = minute.Add(time.Minute + schedule.AllowFromSecond*time.Second)
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
