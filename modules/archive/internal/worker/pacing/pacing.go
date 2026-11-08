// Package pacing keeps the archive worker from competing with market-data writes: requests are never
// started around the collector trigger moments (every N minutes) nor around the top of the hour, and all
// runs share one bandwidth token bucket. The avoidance rules come from archive-mgr's heartbeat replies and
// can be replaced while running; until the first reply the pacing section of app.yaml is used.
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
	// MinuteWindow 为 true 时只在 RequestSeconds 区间内发起请求；为 false 时整分钟都可发（整点静默、带宽和请求间隔仍然生效）。
	MinuteWindow bool
	// HourQuiet 是每小时内不发起请求的区间 [from, to)，格式 "MM:SS"，from 大于 to 表示跨整点。
	HourQuiet [2]string
	// RequestInterval 是同一运行两次请求之间的最小间隔。
	RequestInterval time.Duration
	// MaxBytesPerSecond 是所有并发运行共享的带宽上限（按解压后的响应字节计）。
	MaxBytesPerSecond int64
}

// Default 返回默认节奏：每分钟 25–55 秒、整点 :58～:04 避让、请求间隔 200ms、512KB/s。
func Default() Config {
	return Config{RequestSeconds: [2]int{25, 55}, MinuteWindow: true, HourQuiet: [2]string{"58:00", "04:00"}, RequestInterval: 200 * time.Millisecond, MaxBytesPerSecond: 512 << 10}
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

// Rules 是避让规则：触发时刻 T 是 Unix 纪元起分钟数为 EveryMinutes 倍数的第 0 秒，[T − BeforeSeconds, T + AfterSeconds) 不发请求；
// EveryMinutes 为 0 表示不限秒。HourQuiet 是每小时不发请求的区间（"MM:SS"），nil 表示不避让整点。
type Rules struct {
	EveryMinutes  int
	BeforeSeconds int
	AfterSeconds  int
	HourQuiet     *[2]string
	// Source 是规则来源：config（app.yaml）或 mgr（心跳下发）；ComputedAt 是 mgr 的计算时间。
	Source     string
	ComputedAt string
}

// Validate 检查规则。
func (r Rules) Validate() error {
	if r.EveryMinutes < 0 || r.EveryMinutes > 60 || (r.EveryMinutes > 0 && 60%r.EveryMinutes != 0) {
		return fmt.Errorf("避让的触发间隔 %d 分钟必须整除 60", r.EveryMinutes)
	}
	if r.BeforeSeconds < 0 || r.AfterSeconds < 0 {
		return fmt.Errorf("避让的前后秒数不能为负")
	}
	if r.EveryMinutes > 0 && r.BeforeSeconds+r.AfterSeconds >= r.EveryMinutes*60 {
		return fmt.Errorf("避让的前后秒数合计 %d 必须小于触发间隔 %d 分钟", r.BeforeSeconds+r.AfterSeconds, r.EveryMinutes)
	}
	if r.HourQuiet != nil {
		if _, err := parseMinuteSecond(r.HourQuiet[0]); err != nil {
			return fmt.Errorf("整点窗口起点: %w", err)
		}
		if _, err := parseMinuteSecond(r.HourQuiet[1]); err != nil {
			return fmt.Errorf("整点窗口终点: %w", err)
		}
	}
	return nil
}

// ConfigRules 把 app.yaml 的 pacing 段换算成规则：每分钟可发 [from, to] 秒 ⇔ 每分钟触发、T 前 59−to 秒到 T 后 from 秒不发。
func (c Config) ConfigRules() Rules {
	r := Rules{EveryMinutes: 1, BeforeSeconds: 59 - c.RequestSeconds[1], AfterSeconds: c.RequestSeconds[0], Source: "config"}
	if r.BeforeSeconds == 0 && r.AfterSeconds == 0 {
		r.EveryMinutes = 0
	}
	if from, err1 := parseMinuteSecond(c.HourQuiet[0]); err1 == nil {
		if until, err2 := parseMinuteSecond(c.HourQuiet[1]); err2 == nil && from != until {
			q := c.HourQuiet
			r.HourQuiet = &q
		}
	}
	return r
}

// compiled 是解析后的规则。
type compiled struct {
	rules         Rules
	period        time.Duration // 0 表示不限秒
	before, after time.Duration
	quietOn       bool
	quiet         [2]int
}

func compile(r Rules) (*compiled, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if r.HourQuiet != nil {
		q := *r.HourQuiet
		r.HourQuiet = &q
	}
	c := &compiled{rules: r, period: time.Duration(r.EveryMinutes) * time.Minute,
		before: time.Duration(r.BeforeSeconds) * time.Second, after: time.Duration(r.AfterSeconds) * time.Second}
	if r.HourQuiet != nil {
		c.quiet[0], _ = parseMinuteSecond(r.HourQuiet[0])
		c.quiet[1], _ = parseMinuteSecond(r.HourQuiet[1])
		c.quietOn = c.quiet[0] != c.quiet[1]
	}
	return c, nil
}

// Pacer 同时实现时间窗避让、请求间隔和共享带宽令牌桶。避让规则可在运行中替换（SetRules）。
type Pacer struct {
	cfg   Config
	clock Clock

	rulesMu sync.RWMutex
	rules   *compiled

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
	rules, err := compile(cfg.ConfigRules())
	if err != nil {
		return nil, err
	}
	p.rules = rules
	p.tokens = float64(cfg.MaxBytesPerSecond)
	p.last = p.clock.Now()
	return p, nil
}

// Interval 返回同一运行两次请求之间的最小间隔。
func (p *Pacer) Interval() time.Duration { return p.cfg.RequestInterval }

// SetRules 替换避让规则（mgr 心跳下发）；规则不合法时返回错误并保留原规则。
func (p *Pacer) SetRules(r Rules) error {
	c, err := compile(r)
	if err != nil {
		return err
	}
	p.rulesMu.Lock()
	p.rules = c
	p.rulesMu.Unlock()
	return nil
}

// Rules 返回当前生效的避让规则。
func (p *Pacer) Rules() Rules {
	p.rulesMu.RLock()
	r := p.rules.rules
	p.rulesMu.RUnlock()
	if r.HourQuiet != nil {
		q := *r.HourQuiet
		r.HourQuiet = &q
	}
	return r
}

// MinuteWindow 报告是否按秒窗口避让（pacing.minute_window；为 false 时整分钟都可发，优先于 mgr 下发的规则）。
func (p *Pacer) MinuteWindow() bool { return p.cfg.MinuteWindow }

func (c *compiled) inQuiet(t time.Time) bool {
	if !c.quietOn {
		return false
	}
	offset := t.Minute()*60 + t.Second()
	start, end := c.quiet[0], c.quiet[1]
	if start > end {
		return offset >= start || offset < end
	}
	return offset >= start && offset < end
}

// AllowedAt 返回不早于 t 的第一个允许发起请求的时刻（UTC）。
func (p *Pacer) AllowedAt(t time.Time) time.Time {
	p.rulesMu.RLock()
	c := p.rules
	p.rulesMu.RUnlock()
	t = t.UTC()
	for i := 0; i < 16; i++ {
		if c.inQuiet(t) {
			// 跳到静默区间结束：同一小时内结束，或跨到下一个小时。
			hour := t.Truncate(time.Hour)
			next := hour.Add(time.Duration(c.quiet[1]) * time.Second)
			if !next.After(t) {
				next = next.Add(time.Hour)
			}
			t = next
			continue
		}
		if !p.cfg.MinuteWindow || c.period == 0 {
			return t
		}
		// 距最近一个触发时刻（Unix 纪元起 period 的整数倍）的偏移。
		offset := time.Duration(t.UnixNano() % int64(c.period))
		switch {
		case offset < c.after:
			t = t.Add(c.after - offset)
		case offset >= c.period-c.before:
			t = t.Add(c.period - offset + c.after)
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
