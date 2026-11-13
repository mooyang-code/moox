package cnstock

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// 东方财富的主动限流与冷却。
//
// 2026-11-13 凌晨，一个没有页数上限的临时脚本在控制机上连续翻东方财富美股列表约 40 分钟，出口 IP 随后被对方限流
// （请求立即被断开），8 小时以上没有恢复。这里在程序里保证两件事：
//   - 限流：所有发往 *.eastmoney.com 的请求合计受一个令牌桶和一个并发上限约束，程序自己不可能把对方打到限流；
//   - 冷却：按主机记录连续的传输层失败与 429、403，一旦发现被限流就主动停手，过一段时间只放一个探测请求试探。
//
// 限流器是进程内单例（见 ConfigureRequestLimit），FetchClist 的每一次请求（含重试和回退到备用地址）都要先取许可。
// 主机名不是 *.eastmoney.com 的请求（本地测试服务等）不受限流。

// ErrCoolingDown 表示请求的主机正在冷却：请求没有发出去，也不应该重试。errors.Is 可识别。
var ErrCoolingDown = errors.New("cnstock: 东方财富主机冷却中")

// CoolingDownError 带着冷却中的主机与剩余时间。Probing 为真表示冷却已结束、探测请求正在进行，其余请求继续等它的结果。
type CoolingDownError struct {
	Host      string
	Remaining time.Duration
	Probing   bool
}

func (e *CoolingDownError) Error() string {
	if e.Probing {
		return fmt.Sprintf("%v：%s 的探测请求正在进行，稍后再试", ErrCoolingDown, e.Host)
	}
	return fmt.Sprintf("%v：%s 还要冷却 %s，期间不发请求", ErrCoolingDown, e.Host, e.Remaining.Round(time.Second))
}

func (e *CoolingDownError) Unwrap() error { return ErrCoolingDown }

// RequestLimitConfig 是限流与冷却的参数。零值不可用，用 DefaultRequestLimitConfig 取默认值再改。
type RequestLimitConfig struct {
	// RequestsPerSecond 与 Burst 是全局令牌桶（所有东方财富主机合计）。
	RequestsPerSecond float64 `yaml:"requests_per_second"`
	Burst             int     `yaml:"burst"`
	// MaxConcurrent 是同时在途的请求数上限。
	MaxConcurrent int `yaml:"max_concurrent"`
	// CooldownFailures 是连续多少次传输层失败后进入冷却；一次 429 或 403 立即进入冷却。
	CooldownFailures int `yaml:"cooldown_failures"`
	// CooldownInitial 是首次冷却时长，之后每次再触发翻倍，最长 CooldownMax；恢复后重新从首次算起。
	CooldownInitial time.Duration `yaml:"cooldown_initial"`
	CooldownMax     time.Duration `yaml:"cooldown_max"`
}

// 校验范围的上下限。
const (
	maxRequestsPerSecond = 20
	maxBurst             = 50
	maxConcurrentLimit   = 8
	maxCooldownFailures  = 50
	minCooldown          = time.Second
	maxCooldownInitial   = time.Hour
	maxCooldownCeiling   = 24 * time.Hour
)

// DefaultRequestLimitConfig 返回默认值：每秒 2 个请求、突发 2、顺序请求；连续 5 次传输层失败进入冷却，
// 首次 10 分钟，翻倍，最长 60 分钟。
func DefaultRequestLimitConfig() RequestLimitConfig {
	return RequestLimitConfig{
		RequestsPerSecond: 2,
		Burst:             2,
		MaxConcurrent:     1,
		CooldownFailures:  5,
		CooldownInitial:   10 * time.Minute,
		CooldownMax:       time.Hour,
	}
}

// Validate 检查各项都在合理范围内。
func (c RequestLimitConfig) Validate() error {
	switch {
	case !(c.RequestsPerSecond > 0) || c.RequestsPerSecond > maxRequestsPerSecond:
		return fmt.Errorf("requests_per_second 必须大于 0 且不超过 %d", maxRequestsPerSecond)
	case c.Burst < 1 || c.Burst > maxBurst:
		return fmt.Errorf("burst 必须在 1～%d 之间", maxBurst)
	case c.MaxConcurrent < 1 || c.MaxConcurrent > maxConcurrentLimit:
		return fmt.Errorf("max_concurrent 必须在 1～%d 之间", maxConcurrentLimit)
	case c.CooldownFailures < 1 || c.CooldownFailures > maxCooldownFailures:
		return fmt.Errorf("cooldown_failures 必须在 1～%d 之间", maxCooldownFailures)
	case c.CooldownInitial < minCooldown || c.CooldownInitial > maxCooldownInitial:
		return fmt.Errorf("cooldown_initial 必须在 %s～%s 之间", minCooldown, maxCooldownInitial)
	case c.CooldownMax < c.CooldownInitial || c.CooldownMax > maxCooldownCeiling:
		return fmt.Errorf("cooldown_max 不能小于 cooldown_initial，也不能超过 %s", maxCooldownCeiling)
	}
	return nil
}

// OutcomeKind 是一次请求的结果类别。
type OutcomeKind int

const (
	// OutcomeSuccess 表示收到了 HTTP 响应，且不是 429、403、5xx：清零连续失败计数；探测请求成功则恢复。
	OutcomeSuccess OutcomeKind = iota
	// OutcomeTransportFailure 表示传输层失败（连接被断开、EOF、重置、拨号失败、超时、读响应体失败）。
	OutcomeTransportFailure
	// OutcomeThrottled 表示 HTTP 429 或 403：立即进入冷却。
	OutcomeThrottled
	// OutcomeNeutral 表示没有结论：ctx 取消或到期、5xx。不计入失败，也不清零；探测请求得到它时释放探测名额，下一个请求再探测。
	OutcomeNeutral
)

// Outcome 是一次请求的结果。RetryAfter 只在 429 带 Retry-After 响应头时有值。
type Outcome struct {
	Kind       OutcomeKind
	Status     int
	RetryAfter time.Duration
}

// outcomeOfResponse 按状态码分类：429、403 是限流信号，5xx 没有结论，其余算收到了正常响应。
func outcomeOfResponse(status int, header http.Header, now time.Time) Outcome {
	switch {
	case status == http.StatusTooManyRequests || status == http.StatusForbidden:
		return Outcome{Kind: OutcomeThrottled, Status: status, RetryAfter: parseRetryAfter(header.Get("Retry-After"), now)}
	case status >= 500:
		return Outcome{Kind: OutcomeNeutral, Status: status}
	}
	return Outcome{Kind: OutcomeSuccess, Status: status}
}

// parseRetryAfter 解析 Retry-After：秒数或 HTTP 日期；解析不了或已过去按 0 处理。
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// LimitEventKind 是限流器对外报告的事件类别。
type LimitEventKind int

const (
	// EventSent 表示一个请求拿到了许可，Wait 是它在限流器里等待的时间。
	EventSent LimitEventKind = iota
	// EventRejected 表示请求因主机冷却被拒绝（没有发出去）。
	EventRejected
	// EventCooldownStarted 表示主机进入冷却：Cooldown 是时长，Level 是连续触发的次数，Reason 是原因。
	EventCooldownStarted
	// EventProbe 表示冷却结束，放出一个探测请求。
	EventProbe
	// EventRecovered 表示探测成功，主机恢复。
	EventRecovered
)

// 进入冷却的原因。
const (
	ReasonTransportFailure = "transport_failure"
	ReasonRateLimited      = "rate_limited"
	ReasonForbidden        = "forbidden"
)

// LimitEvent 是限流器报告的一个事件。
type LimitEvent struct {
	Kind     LimitEventKind
	Host     string
	Wait     time.Duration
	Cooldown time.Duration
	Level    int
	Reason   string
	// Failures 是进入冷却时连续传输层失败的次数。
	Failures int
}

// LimitObserver 接收限流器的事件，用于日志与指标；在限流器的互斥锁之外同步调用（但此时并发名额可能还没归还），
// 实现必须并发安全、不能阻塞，也不能回调限流器。
type LimitObserver func(LimitEvent)

type hostState struct {
	failures int
	level    int
	until    time.Time
	cooling  bool
	probing  bool
}

// RequestLimiter 是东方财富请求的限流器：全局令牌桶 + 并发上限 + 按主机的冷却。
type RequestLimiter struct {
	cfg      RequestLimitConfig
	observer LimitObserver
	now      func() time.Time
	sleep    func(ctx context.Context, d time.Duration) error
	slots    chan struct{}

	mu     sync.Mutex
	tokens float64
	last   time.Time
	hosts  map[string]*hostState
}

// NewRequestLimiter 按配置创建限流器；配置不合法时返回错误。observer 可为空。
func NewRequestLimiter(cfg RequestLimitConfig, observer LimitObserver) (*RequestLimiter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return newRequestLimiter(cfg, observer, time.Now, sleepContext), nil
}

// newRequestLimiter 允许注入时钟与等待，测试用。
func newRequestLimiter(cfg RequestLimitConfig, observer LimitObserver, now func() time.Time, sleep func(context.Context, time.Duration) error) *RequestLimiter {
	return &RequestLimiter{
		cfg:      cfg,
		observer: observer,
		now:      now,
		sleep:    sleep,
		slots:    make(chan struct{}, cfg.MaxConcurrent),
		tokens:   float64(cfg.Burst),
		last:     now(),
		hosts:    map[string]*hostState{},
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// 进程内单例：Collector 启动时用 ConfigureRequestLimit 配置一次；没配置时用代码默认值。
var activeLimiter atomic.Pointer[RequestLimiter]

// ConfigureRequestLimit 按配置重建进程内的限流器（并丢弃已有的冷却状态），应在启动时、发出任何东方财富请求之前调用一次。
func ConfigureRequestLimit(cfg RequestLimitConfig, observer LimitObserver) error {
	limiter, err := NewRequestLimiter(cfg, observer)
	if err != nil {
		return err
	}
	activeLimiter.Store(limiter)
	return nil
}

// processRequestLimiter 返回进程内的限流器，没配置过时按默认值创建。
func processRequestLimiter() *RequestLimiter {
	if limiter := activeLimiter.Load(); limiter != nil {
		return limiter
	}
	limiter := newRequestLimiter(DefaultRequestLimitConfig(), nil, time.Now, sleepContext)
	if activeLimiter.CompareAndSwap(nil, limiter) {
		return limiter
	}
	return activeLimiter.Load()
}

// limitedHost 判断主机是否受限流约束：eastmoney.com 及其子域名。
func limitedHost(host string) bool {
	return host == "eastmoney.com" || strings.HasSuffix(host, ".eastmoney.com")
}

func normalizeHost(host string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), "."))
}

func noopRelease(Outcome) {}

// Acquire 在发请求之前取许可：主机冷却中立即返回 CoolingDownError（不占令牌与并发）；否则先等并发名额，再等令牌，
// 等待期间 ctx 取消或到期立即返回 ctx 的错误。成功后必须调用返回的函数报告结果，重复调用只有第一次生效。
// 主机名不是 *.eastmoney.com 时不限流，返回的函数什么也不做。
func (l *RequestLimiter) Acquire(ctx context.Context, host string) (func(Outcome), error) {
	host = normalizeHost(host)
	if !limitedHost(host) {
		return noopRelease, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := l.peekCooling(host); err != nil {
		return nil, err
	}
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	probe, err := l.admit(host)
	if err != nil {
		<-l.slots
		return nil, err
	}
	start := l.now()
	if err := l.waitToken(ctx); err != nil {
		l.abandon(host, probe)
		<-l.slots
		return nil, err
	}
	l.emit(LimitEvent{Kind: EventSent, Host: host, Wait: l.now().Sub(start)})
	var once sync.Once
	return func(outcome Outcome) {
		once.Do(func() {
			l.finish(host, probe, outcome)
			<-l.slots
		})
	}, nil
}

func (l *RequestLimiter) emit(events ...LimitEvent) {
	if l.observer == nil {
		return
	}
	for _, event := range events {
		l.observer(event)
	}
}

// peekCooling 在取并发名额之前先看一眼：冷却中就直接拒绝，不去排队。不占探测名额。
func (l *RequestLimiter) peekCooling(host string) error {
	l.mu.Lock()
	state := l.hosts[host]
	var err error
	if state != nil && state.cooling {
		now := l.now()
		switch {
		case now.Before(state.until):
			err = &CoolingDownError{Host: host, Remaining: state.until.Sub(now)}
		case state.probing:
			err = &CoolingDownError{Host: host, Probing: true}
		}
	}
	l.mu.Unlock()
	if err != nil {
		l.emit(LimitEvent{Kind: EventRejected, Host: host})
	}
	return err
}

// admit 在拿到并发名额之后判断主机状态：未冷却放行；冷却中拒绝；冷却结束且没有探测在进行时，这个请求成为探测。
func (l *RequestLimiter) admit(host string) (probe bool, err error) {
	l.mu.Lock()
	state := l.hosts[host]
	var events []LimitEvent
	if state != nil && state.cooling {
		now := l.now()
		switch {
		case now.Before(state.until):
			err = &CoolingDownError{Host: host, Remaining: state.until.Sub(now)}
		case state.probing:
			err = &CoolingDownError{Host: host, Probing: true}
		default:
			state.probing = true
			probe = true
			events = append(events, LimitEvent{Kind: EventProbe, Host: host, Level: state.level})
		}
	}
	l.mu.Unlock()
	if err != nil {
		events = append(events, LimitEvent{Kind: EventRejected, Host: host})
	}
	l.emit(events...)
	return probe, err
}

// abandon 在取到令牌之前被取消时释放探测名额，否则主机会一直卡在冷却里。
func (l *RequestLimiter) abandon(host string, probe bool) {
	if !probe {
		return
	}
	l.mu.Lock()
	if state := l.hosts[host]; state != nil {
		state.probing = false
	}
	l.mu.Unlock()
}

// waitToken 预定一个令牌：令牌不够时按补充速度算出要等的时间并等待；等待被取消时把令牌还回去。
func (l *RequestLimiter) waitToken(ctx context.Context) error {
	l.mu.Lock()
	now := l.now()
	if elapsed := now.Sub(l.last); elapsed > 0 {
		l.tokens = math.Min(float64(l.cfg.Burst), l.tokens+elapsed.Seconds()*l.cfg.RequestsPerSecond)
		l.last = now
	}
	l.tokens--
	var wait time.Duration
	if l.tokens < 0 {
		wait = time.Duration(math.Ceil(-l.tokens / l.cfg.RequestsPerSecond * float64(time.Second)))
	}
	l.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	if err := l.sleep(ctx, wait); err != nil {
		l.mu.Lock()
		l.tokens = math.Min(float64(l.cfg.Burst), l.tokens+1)
		l.mu.Unlock()
		return err
	}
	return nil
}

// finish 按请求结果更新主机状态。冷却期间收尾的非探测请求（并发大于 1 时可能出现）不影响状态。
func (l *RequestLimiter) finish(host string, probe bool, outcome Outcome) {
	l.mu.Lock()
	state := l.hosts[host]
	if state == nil {
		state = &hostState{}
		l.hosts[host] = state
	}
	var events []LimitEvent
	switch outcome.Kind {
	case OutcomeSuccess:
		if probe {
			*state = hostState{}
			events = append(events, LimitEvent{Kind: EventRecovered, Host: host})
		} else if !state.cooling {
			state.failures = 0
		}
	case OutcomeTransportFailure:
		switch {
		case probe:
			events = append(events, l.startCooldownLocked(host, state, ReasonTransportFailure, 0))
		case !state.cooling:
			state.failures++
			if state.failures >= l.cfg.CooldownFailures {
				events = append(events, l.startCooldownLocked(host, state, ReasonTransportFailure, 0))
			}
		}
	case OutcomeThrottled:
		if probe || !state.cooling {
			reason := ReasonRateLimited
			if outcome.Status == http.StatusForbidden {
				reason = ReasonForbidden
			}
			events = append(events, l.startCooldownLocked(host, state, reason, outcome.RetryAfter))
		}
	case OutcomeNeutral:
		if probe {
			state.probing = false
		}
	}
	l.mu.Unlock()
	l.emit(events...)
}

// startCooldownLocked 让主机进入冷却：时长 = 首次时长 × 2^(已触发次数)，不超过上限；Retry-After 更长时取它，同样不超过上限。
// 调用时必须持有 l.mu。
func (l *RequestLimiter) startCooldownLocked(host string, state *hostState, reason string, retryAfter time.Duration) LimitEvent {
	failures := state.failures
	state.level++
	duration := l.cfg.CooldownInitial
	for i := 1; i < state.level && duration < l.cfg.CooldownMax; i++ {
		duration *= 2
	}
	if retryAfter > duration {
		duration = retryAfter
	}
	if duration > l.cfg.CooldownMax {
		duration = l.cfg.CooldownMax
	}
	state.cooling = true
	state.probing = false
	state.failures = 0
	state.until = l.now().Add(duration)
	return LimitEvent{Kind: EventCooldownStarted, Host: host, Cooldown: duration, Level: state.level, Reason: reason, Failures: failures}
}
