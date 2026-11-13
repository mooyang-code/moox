package cnstock

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testPush2     = "push2.eastmoney.com"
	testPush2Test = "push2test.eastmoney.com"
)

func testConfig() RequestLimitConfig { return DefaultRequestLimitConfig() }

func mustAcquire(t *testing.T, l *RequestLimiter, host string) func(Outcome) {
	t.Helper()
	release, err := l.Acquire(context.Background(), host)
	if err != nil {
		t.Fatalf("Acquire(%s) 失败：%v", host, err)
	}
	return release
}

func failTransport(t *testing.T, l *RequestLimiter, host string, times int) {
	t.Helper()
	for i := 0; i < times; i++ {
		mustAcquire(t, l, host)(Outcome{Kind: OutcomeTransportFailure})
	}
}

func cooldowns(events *[]LimitEvent) []LimitEvent {
	var out []LimitEvent
	for _, event := range *events {
		if event.Kind == EventCooldownStarted {
			out = append(out, event)
		}
	}
	return out
}

func TestDefaultRequestLimitConfig(t *testing.T) {
	cfg := DefaultRequestLimitConfig()
	want := RequestLimitConfig{RequestsPerSecond: 2, Burst: 2, MaxConcurrent: 1, CooldownFailures: 5, CooldownInitial: 10 * time.Minute, CooldownMax: time.Hour}
	if cfg != want {
		t.Fatalf("默认值 = %+v", cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestLimitConfigValidate(t *testing.T) {
	cases := map[string]func(*RequestLimitConfig){
		"rps 为零":          func(c *RequestLimitConfig) { c.RequestsPerSecond = 0 },
		"rps 过大":          func(c *RequestLimitConfig) { c.RequestsPerSecond = 21 },
		"burst 为零":        func(c *RequestLimitConfig) { c.Burst = 0 },
		"并发为零":            func(c *RequestLimitConfig) { c.MaxConcurrent = 0 },
		"并发过大":            func(c *RequestLimitConfig) { c.MaxConcurrent = 9 },
		"失败次数为零":          func(c *RequestLimitConfig) { c.CooldownFailures = 0 },
		"首次冷却过短":          func(c *RequestLimitConfig) { c.CooldownInitial = time.Millisecond },
		"首次冷却过长":          func(c *RequestLimitConfig) { c.CooldownInitial = 2 * time.Hour },
		"最长冷却小于首次":        func(c *RequestLimitConfig) { c.CooldownMax = time.Minute },
		"最长冷却过长":          func(c *RequestLimitConfig) { c.CooldownMax = 48 * time.Hour },
		"burst 过大":        func(c *RequestLimitConfig) { c.Burst = 51 },
		"cooldown 失败次数过大": func(c *RequestLimitConfig) { c.CooldownFailures = 51 },
	}
	for name, mutate := range cases {
		cfg := DefaultRequestLimitConfig()
		mutate(&cfg)
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s：应当校验失败", name)
		}
		if _, err := NewRequestLimiter(cfg, nil); err == nil {
			t.Errorf("%s：NewRequestLimiter 应当失败", name)
		}
	}
	edge := DefaultRequestLimitConfig()
	edge.RequestsPerSecond, edge.MaxConcurrent, edge.CooldownFailures = 20, 8, 1
	if err := edge.Validate(); err != nil {
		t.Fatalf("边界值应当通过：%v", err)
	}
}

// 令牌桶节奏：突发 2 个立即放行，之后每 0.5 秒一个；空闲后令牌补回，但不超过突发。
func TestTokenBucketPacing(t *testing.T) {
	limiter, clock, _ := newTestLimiter(testConfig())
	for i := 0; i < 5; i++ {
		mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	}
	want := []time.Duration{500 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}
	if got := clock.Sleeps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("等待 = %v，期望 %v", got, want)
	}

	clock.Advance(time.Hour)
	before := len(clock.Sleeps())
	for i := 0; i < 2; i++ {
		mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	}
	if len(clock.Sleeps()) != before {
		t.Fatalf("空闲后突发内的请求不应等待：%v", clock.Sleeps())
	}
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	if len(clock.Sleeps()) != before+1 {
		t.Fatalf("令牌补回不超过突发，第三个请求要等：%v", clock.Sleeps())
	}
}

// 令牌桶是全局的：几个东方财富主机合计限速。
func TestTokenBucketIsSharedAcrossHosts(t *testing.T) {
	limiter, clock, _ := newTestLimiter(testConfig())
	for _, host := range []string{testPush2, testPush2Test, "push2his.eastmoney.com", testPush2} {
		mustAcquire(t, limiter, host)(Outcome{Kind: OutcomeSuccess})
	}
	if got := clock.Sleeps(); len(got) != 2 {
		t.Fatalf("四个请求合计只有 2 个突发令牌，应等待 2 次：%v", got)
	}
}

// 其它速率：每秒 4 个，间隔 250 毫秒。
func TestTokenBucketRate(t *testing.T) {
	cfg := testConfig()
	cfg.RequestsPerSecond, cfg.Burst = 4, 1
	limiter, clock, _ := newTestLimiter(cfg)
	for i := 0; i < 4; i++ {
		mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	}
	want := []time.Duration{250 * time.Millisecond, 250 * time.Millisecond, 250 * time.Millisecond}
	if got := clock.Sleeps(); !reflect.DeepEqual(got, want) {
		t.Fatalf("等待 = %v", got)
	}
}

// 主机名不是 *.eastmoney.com（本地测试服务等）不限流、不记冷却。
func TestNonEastMoneyHostsAreNotLimited(t *testing.T) {
	limiter, clock, events := newTestLimiter(testConfig())
	for i := 0; i < 50; i++ {
		release, err := limiter.Acquire(context.Background(), "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		release(Outcome{Kind: OutcomeTransportFailure})
	}
	if len(clock.Sleeps()) != 0 || len(*events) != 0 {
		t.Fatalf("不应有等待与事件：%v %v", clock.Sleeps(), *events)
	}
	for _, host := range []string{"notEastmoney.com", "eastmoney.com.evil.test", "xeastmoney.com"} {
		if limitedHost(normalizeHost(host)) {
			t.Errorf("%s 不应受限流约束", host)
		}
	}
	for _, host := range []string{"eastmoney.com", "push2.eastmoney.com", "PUSH2.EastMoney.com.", "a.b.eastmoney.com"} {
		if !limitedHost(normalizeHost(host)) {
			t.Errorf("%s 应受限流约束", host)
		}
	}
}

// 并发上限：名额用完后，下一个请求等到有人释放；等待期间 ctx 到期立即返回。
func TestConcurrencyLimit(t *testing.T) {
	limiter, _, _ := newTestLimiter(testConfig())
	first := mustAcquire(t, limiter, testPush2)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := limiter.Acquire(ctx, testPush2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("名额被占用时应等到 ctx 到期，err = %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("没有及时返回")
	}

	acquired := make(chan func(Outcome), 1)
	go func() {
		release, err := limiter.Acquire(context.Background(), testPush2Test)
		if err == nil {
			acquired <- release
		}
	}()
	select {
	case <-acquired:
		t.Fatal("并发上限为 1，第一个请求没有释放时第二个不应拿到许可")
	case <-time.After(50 * time.Millisecond):
	}
	first(Outcome{Kind: OutcomeSuccess})
	select {
	case release := <-acquired:
		release(Outcome{Kind: OutcomeSuccess})
	case <-time.After(2 * time.Second):
		t.Fatal("释放之后第二个请求应当拿到许可")
	}

	cfg := testConfig()
	cfg.MaxConcurrent = 2
	limiter2, _, _ := newTestLimiter(cfg)
	a, b := mustAcquire(t, limiter2, testPush2), mustAcquire(t, limiter2, testPush2)
	a(Outcome{Kind: OutcomeSuccess})
	b(Outcome{Kind: OutcomeSuccess})
}

// 重复调用 release 只有第一次生效：名额只还一次，结果只记一次。
func TestReleaseIsIdempotent(t *testing.T) {
	cfg := testConfig()
	cfg.CooldownFailures = 2
	limiter, _, events := newTestLimiter(cfg)
	release := mustAcquire(t, limiter, testPush2)
	release(Outcome{Kind: OutcomeTransportFailure})
	release(Outcome{Kind: OutcomeTransportFailure})
	release(Outcome{Kind: OutcomeTransportFailure})
	if len(cooldowns(events)) != 0 {
		t.Fatal("同一个请求的结果只能记一次")
	}
	if len(limiter.slots) != 0 {
		t.Fatal("名额只能还一次")
	}
}

// ctx 在等待令牌时取消：立即返回，并把令牌还回去、释放并发名额。
func TestContextCancelWhileWaitingForToken(t *testing.T) {
	cfg := testConfig()
	cfg.Burst = 1
	clock := newFakeClock()
	blocked := func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
	limiter := newRequestLimiter(cfg, nil, clock.Now, blocked)
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess}) // 用掉唯一的令牌

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx, testPush2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if len(limiter.slots) != 0 {
		t.Fatal("等令牌被取消后必须释放并发名额")
	}
	if limiter.tokens < -0.001 || limiter.tokens > 0.001 {
		t.Fatalf("被取消的请求应还回令牌，tokens = %v", limiter.tokens)
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := limiter.Acquire(cancelled, testPush2); !errors.Is(err, context.Canceled) {
		t.Fatalf("ctx 已取消时应立即返回：%v", err)
	}
}

// 连续 5 次传输层失败进入冷却；之前的第 4 次不会；中间成功一次清零计数。
func TestCooldownAfterConsecutiveTransportFailures(t *testing.T) {
	limiter, _, events := newTestLimiter(testConfig())
	failTransport(t, limiter, testPush2, 4)
	if len(cooldowns(events)) != 0 {
		t.Fatal("4 次失败不应进入冷却")
	}
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	failTransport(t, limiter, testPush2, 4)
	if len(cooldowns(events)) != 0 {
		t.Fatal("成功一次后计数清零，再失败 4 次不应进入冷却")
	}
	failTransport(t, limiter, testPush2, 1)
	got := cooldowns(events)
	if len(got) != 1 || got[0].Host != testPush2 || got[0].Cooldown != 10*time.Minute || got[0].Level != 1 || got[0].Reason != ReasonTransportFailure || got[0].Failures != 5 {
		t.Fatalf("冷却事件 = %+v", got)
	}
	_, err := limiter.Acquire(context.Background(), testPush2)
	var cooling *CoolingDownError
	if !errors.Is(err, ErrCoolingDown) || !errors.As(err, &cooling) || cooling.Host != testPush2 || cooling.Remaining != 10*time.Minute {
		t.Fatalf("冷却期间应返回带主机与剩余时间的冷却错误：%v", err)
	}
}

// 一次 429、一次 403 立即进入冷却；原因不同。
func TestCooldownOnThrottleStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		reason string
	}{{http.StatusTooManyRequests, ReasonRateLimited}, {http.StatusForbidden, ReasonForbidden}} {
		limiter, _, events := newTestLimiter(testConfig())
		mustAcquire(t, limiter, testPush2)(outcomeOfResponse(tc.status, http.Header{}, time.Now()))
		got := cooldowns(events)
		if len(got) != 1 || got[0].Reason != tc.reason || got[0].Cooldown != 10*time.Minute {
			t.Fatalf("状态 %d：%+v", tc.status, got)
		}
		if _, err := limiter.Acquire(context.Background(), testPush2); !errors.Is(err, ErrCoolingDown) {
			t.Fatalf("状态 %d 之后应处于冷却：%v", tc.status, err)
		}
	}
}

// 5xx、ctx 取消没有结论：不计入失败，也不清零计数。
func TestNeutralOutcomesDoNotCountOrReset(t *testing.T) {
	limiter, _, events := newTestLimiter(testConfig())
	failTransport(t, limiter, testPush2, 4)
	mustAcquire(t, limiter, testPush2)(outcomeOfResponse(502, http.Header{}, time.Now()))
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeNeutral})
	if len(cooldowns(events)) != 0 {
		t.Fatal("没有结论的结果不应触发冷却")
	}
	failTransport(t, limiter, testPush2, 1)
	if len(cooldowns(events)) != 1 {
		t.Fatal("没有结论的结果不应清零计数：第 5 次失败应进入冷却")
	}
	// 10 次只有“没有结论”的结果：永远不冷却。
	limiter2, _, events2 := newTestLimiter(testConfig())
	for i := 0; i < 10; i++ {
		mustAcquire(t, limiter2, testPush2)(Outcome{Kind: OutcomeNeutral})
	}
	if len(cooldowns(events2)) != 0 {
		t.Fatal("取消或 5xx 不应进入冷却")
	}
}

// 冷却期间请求不发出去：Acquire 立即返回错误，不占令牌（令牌桶不被消耗）、不占并发名额。
func TestCoolingRejectsWithoutConsumingTokens(t *testing.T) {
	limiter, clock, events := newTestLimiter(testConfig())
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeThrottled, Status: 429})
	clock.Advance(time.Second) // 令牌补满
	sleepsBefore := len(clock.Sleeps())
	for i := 0; i < 100; i++ {
		if _, err := limiter.Acquire(context.Background(), testPush2); !errors.Is(err, ErrCoolingDown) {
			t.Fatalf("err = %v", err)
		}
	}
	if len(clock.Sleeps()) != sleepsBefore || len(limiter.slots) != 0 {
		t.Fatalf("被拒绝的请求不应占用令牌与名额")
	}
	rejected := 0
	for _, event := range *events {
		if event.Kind == EventRejected {
			rejected++
		}
	}
	if rejected != 100 {
		t.Fatalf("拒绝事件 = %d", rejected)
	}
	// 突发令牌仍然完整：另一个主机的两个请求都不用等。
	mustAcquire(t, limiter, testPush2Test)(Outcome{Kind: OutcomeSuccess})
	mustAcquire(t, limiter, testPush2Test)(Outcome{Kind: OutcomeSuccess})
	if len(clock.Sleeps()) != sleepsBefore {
		t.Fatalf("冷却的拒绝不应消耗令牌：%v", clock.Sleeps())
	}
}

// 按主机独立：push2 冷却不影响 push2test；失败计数也是各记各的。
func TestCooldownIsPerHost(t *testing.T) {
	limiter, _, events := newTestLimiter(testConfig())
	failTransport(t, limiter, testPush2, 3)
	failTransport(t, limiter, testPush2Test, 3)
	if len(cooldowns(events)) != 0 {
		t.Fatal("各主机各记各的失败次数")
	}
	failTransport(t, limiter, testPush2, 2)
	if _, err := limiter.Acquire(context.Background(), testPush2); !errors.Is(err, ErrCoolingDown) {
		t.Fatalf("push2 应在冷却：%v", err)
	}
	mustAcquire(t, limiter, testPush2Test)(Outcome{Kind: OutcomeSuccess})
}

// 冷却时长：首次 10 分钟，探测失败后翻倍，最长 60 分钟；到期前仍拒绝，到期后放过一个探测。
func TestCooldownDoublesUpToMax(t *testing.T) {
	limiter, clock, events := newTestLimiter(testConfig())
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeThrottled, Status: 429})
	want := []time.Duration{10 * time.Minute, 20 * time.Minute, 40 * time.Minute, time.Hour, time.Hour}
	for i, d := range want {
		got := cooldowns(events)
		if len(got) != i+1 || got[i].Cooldown != d || got[i].Level != i+1 {
			t.Fatalf("第 %d 次冷却 = %+v，期望 %v", i+1, got, d)
		}
		clock.Advance(d - time.Second)
		if _, err := limiter.Acquire(context.Background(), testPush2); !errors.Is(err, ErrCoolingDown) {
			t.Fatalf("冷却结束前一秒仍应拒绝：%v", err)
		}
		clock.Advance(time.Second)
		mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeTransportFailure}) // 探测失败
	}
}

// 半开：到期后只有一个探测请求，其余请求继续被拒绝；探测成功则恢复并清零，下一次冷却又从首次时长算起。
func TestHalfOpenProbeSuccessRecovers(t *testing.T) {
	limiter, clock, events := newTestLimiter(testConfig())
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeThrottled, Status: 403})
	clock.Advance(10 * time.Minute)

	probe := mustAcquire(t, limiter, testPush2)
	_, err := limiter.Acquire(context.Background(), testPush2)
	var cooling *CoolingDownError
	if !errors.As(err, &cooling) || !cooling.Probing {
		t.Fatalf("探测进行时其余请求应被拒绝：%v", err)
	}
	probe(Outcome{Kind: OutcomeSuccess})
	kinds := []LimitEventKind{}
	for _, event := range *events {
		if event.Kind == EventProbe || event.Kind == EventRecovered {
			kinds = append(kinds, event.Kind)
		}
	}
	if !reflect.DeepEqual(kinds, []LimitEventKind{EventProbe, EventRecovered}) {
		t.Fatalf("探测事件 = %v", kinds)
	}
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})

	// 恢复后计数与层级清零。
	failTransport(t, limiter, testPush2, 5)
	got := cooldowns(events)
	if last := got[len(got)-1]; last.Level != 1 || last.Cooldown != 10*time.Minute {
		t.Fatalf("恢复后再次冷却应从首次时长算起：%+v", last)
	}
}

// 探测失败（传输失败、429）立即重新冷却，不用再累计 5 次；探测没有结论（取消）则释放探测名额，下一个请求再探测。
func TestHalfOpenProbeFailureAndNeutral(t *testing.T) {
	limiter, clock, events := newTestLimiter(testConfig())
	failTransport(t, limiter, testPush2, 5)
	clock.Advance(10 * time.Minute)
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeTransportFailure})
	if got := cooldowns(events); len(got) != 2 || got[1].Cooldown != 20*time.Minute {
		t.Fatalf("探测失败应按翻倍后的时长重新冷却：%+v", got)
	}

	clock.Advance(20 * time.Minute)
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeNeutral})
	probe := mustAcquire(t, limiter, testPush2) // 名额已释放，下一个请求成为探测
	probe(Outcome{Kind: OutcomeThrottled, Status: 429})
	if got := cooldowns(events); len(got) != 3 || got[2].Cooldown != 40*time.Minute || got[2].Reason != ReasonRateLimited {
		t.Fatalf("探测 429 应重新冷却：%+v", got)
	}
}

// 探测请求在拿到令牌之前被取消：必须释放探测名额，否则主机永远卡在冷却里。
func TestProbeAbandonedWhenCancelledBeforeToken(t *testing.T) {
	cfg := testConfig()
	cfg.Burst = 1
	clock := newFakeClock()
	var block atomic.Bool
	sleep := func(ctx context.Context, d time.Duration) error {
		if block.Load() {
			<-ctx.Done()
			return ctx.Err()
		}
		return clock.Sleep(ctx, d)
	}
	limiter := newRequestLimiter(cfg, nil, clock.Now, sleep)
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeThrottled, Status: 429})
	clock.Advance(10 * time.Minute)
	mustAcquire(t, limiter, testPush2Test)(Outcome{Kind: OutcomeSuccess}) // 用掉补满的令牌

	block.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := limiter.Acquire(ctx, testPush2); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	block.Store(false)
	clock.Advance(time.Second)
	release, err := limiter.Acquire(context.Background(), testPush2)
	if err != nil {
		t.Fatalf("探测名额应已释放，下一个请求可以探测：%v", err)
	}
	release(Outcome{Kind: OutcomeSuccess})
}

// 并发大于 1 时，冷却期间才收尾的普通请求不影响状态（不重复触发冷却，也不当作探测）。
func TestInFlightRequestFinishingDuringCooldownIsIgnored(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConcurrent = 2
	limiter, _, events := newTestLimiter(cfg)
	slow := mustAcquire(t, limiter, testPush2)
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeThrottled, Status: 429})
	slow(Outcome{Kind: OutcomeTransportFailure})
	slow2 := mustAcquire(t, limiter, testPush2Test)
	slow2(Outcome{Kind: OutcomeSuccess})
	if got := cooldowns(events); len(got) != 1 || got[0].Level != 1 {
		t.Fatalf("冷却期间收尾的请求不应再次触发冷却：%+v", got)
	}
}

// Retry-After 比计算出的冷却时长长时取它；更短时取计算值；超过最长冷却时按最长算。
func TestRetryAfterTakesTheLarger(t *testing.T) {
	now := time.Date(2026, 11, 13, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		retryAfter string
		want       time.Duration
	}{
		{"1800", 30 * time.Minute},
		{"60", 10 * time.Minute},
		{"999999", time.Hour},
		{now.Add(25 * time.Minute).Format(http.TimeFormat), 25 * time.Minute},
		{"垃圾", 10 * time.Minute},
		{"", 10 * time.Minute},
	}
	for _, tc := range cases {
		limiter, clock, events := newTestLimiter(testConfig())
		header := http.Header{}
		header.Set("Retry-After", tc.retryAfter)
		mustAcquire(t, limiter, testPush2)(outcomeOfResponse(http.StatusTooManyRequests, header, clock.Now()))
		if got := cooldowns(events); len(got) != 1 || got[0].Cooldown != tc.want {
			t.Fatalf("Retry-After=%q：冷却 = %+v，期望 %v", tc.retryAfter, got, tc.want)
		}
	}
	if d := parseRetryAfter("-5", now); d != 0 {
		t.Fatalf("负数 = %v", d)
	}
	if d := parseRetryAfter(now.Add(-time.Hour).Format(http.TimeFormat), now); d != 0 {
		t.Fatalf("已过去的日期 = %v", d)
	}
}

// 观察者收到的等待时间是请求在限流器里等的时间。
func TestObserverReportsWait(t *testing.T) {
	limiter, _, events := newTestLimiter(testConfig())
	for i := 0; i < 3; i++ {
		mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeSuccess})
	}
	var waits []time.Duration
	for _, event := range *events {
		if event.Kind == EventSent {
			waits = append(waits, event.Wait)
		}
	}
	want := []time.Duration{0, 0, 500 * time.Millisecond}
	if !reflect.DeepEqual(waits, want) {
		t.Fatalf("等待时间 = %v", waits)
	}
}

// 进程内单例：没配置时用默认值；ConfigureRequestLimit 校验并替换；配置不合法时保持原来的。
func TestConfigureRequestLimit(t *testing.T) {
	previous := activeLimiter.Load()
	t.Cleanup(func() { activeLimiter.Store(previous) })

	activeLimiter.Store(nil)
	if got := processRequestLimiter(); got == nil || got.cfg != DefaultRequestLimitConfig() {
		t.Fatalf("没配置时应使用默认值：%+v", got)
	}
	cfg := DefaultRequestLimitConfig()
	cfg.RequestsPerSecond, cfg.CooldownFailures = 10, 3
	if err := ConfigureRequestLimit(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if got := processRequestLimiter(); got.cfg != cfg {
		t.Fatalf("配置没有生效：%+v", got.cfg)
	}
	bad := cfg
	bad.Burst = 0
	if err := ConfigureRequestLimit(bad, nil); err == nil {
		t.Fatal("不合法的配置应当失败")
	}
	if got := processRequestLimiter(); got.cfg != cfg {
		t.Fatalf("配置失败后应保持原来的：%+v", got.cfg)
	}
}
