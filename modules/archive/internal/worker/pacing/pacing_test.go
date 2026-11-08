package pacing

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	now   time.Time
	slept time.Duration
}

func (f *fakeClock) clock() *Clock {
	return &Clock{
		Now: func() time.Time { return f.now },
		Sleep: func(ctx context.Context, d time.Duration) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			f.now = f.now.Add(d)
			f.slept += d
			return nil
		},
	}
}

func mustPacer(t *testing.T, cfg Config, f *fakeClock) *Pacer {
	t.Helper()
	p, err := New(cfg, f.clock())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func at(h, m, s int) time.Time { return time.Date(2026, 10, 17, h, m, s, 0, time.UTC) }

func TestAllowedAtAvoidsMinuteEdgesAndTheTopOfTheHour(t *testing.T) {
	p := mustPacer(t, Default(), &fakeClock{now: at(1, 0, 0)})
	for _, tc := range []struct{ in, want time.Time }{
		{at(1, 30, 30), at(1, 30, 30)},                  // 窗口内：立即
		{at(1, 30, 25), at(1, 30, 25)},                  // 窗口起点含
		{at(1, 30, 55), at(1, 30, 55)},                  // 窗口终点含
		{at(1, 30, 10), at(1, 30, 25)},                  // 分钟开头：等到 25 秒
		{at(1, 30, 56), at(1, 31, 25)},                  // 55 秒之后：下一分钟 25 秒
		{at(1, 57, 56), at(2, 4, 25)},                   // 57:56 → 下一分钟 58:25 落在静默区 → 整点后 04:25
		{at(1, 58, 0), at(2, 4, 25)},                    // 静默区开头
		{at(1, 59, 59), at(2, 4, 25)},                   // 跨整点
		{at(2, 0, 0), at(2, 4, 25)},                     // 整点
		{at(2, 3, 59), at(2, 4, 25)},                    // 静默区末尾前一秒
		{at(2, 4, 0), at(2, 4, 25)},                     // 静默区结束，但落在分钟开头
		{at(2, 4, 30), at(2, 4, 30)},                    // 静默区结束后的窗口内
		{at(23, 58, 30), at(0, 4, 25).AddDate(0, 0, 1)}, // 跨零点
	} {
		if got := p.AllowedAt(tc.in); !got.Equal(tc.want) {
			t.Fatalf("AllowedAt(%s) = %s, want %s", tc.in.Format("15:04:05"), got.Format("15:04:05"), tc.want.Format("15:04:05"))
		}
	}
}

func TestWaitTurnSleepsUntilTheWindowOpens(t *testing.T) {
	f := &fakeClock{now: at(3, 10, 5)}
	p := mustPacer(t, Default(), f)
	if err := p.WaitTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.now.Equal(at(3, 10, 25)) || f.slept != 20*time.Second {
		t.Fatalf("now=%s slept=%s", f.now.Format("15:04:05"), f.slept)
	}
	// 已在窗口内：不睡眠。
	f.slept = 0
	if err := p.WaitTurn(context.Background()); err != nil || f.slept != 0 {
		t.Fatalf("slept=%s err=%v", f.slept, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.now = at(3, 58, 0)
	if err := p.WaitTurn(ctx); err == nil {
		t.Fatal("取消后 WaitTurn 必须返回")
	}
}

func TestThrottleCapsTheSharedBandwidth(t *testing.T) {
	cfg := Default()
	cfg.MaxBytesPerSecond = 1000
	f := &fakeClock{now: at(1, 30, 30)}
	p := mustPacer(t, cfg, f)
	// 初始满桶 1000 字节：第一个 1000 字节不用等。
	if err := p.Throttle(context.Background(), 1000); err != nil || f.slept != 0 {
		t.Fatalf("slept=%s err=%v", f.slept, err)
	}
	// 再来 3000 字节：欠 3 秒。
	if err := p.Throttle(context.Background(), 3000); err != nil || f.slept != 3*time.Second {
		t.Fatalf("slept=%s err=%v", f.slept, err)
	}
	// 睡眠期间令牌已补足；之后空闲 10 秒，桶最多补到一个突发（1 秒的量），不会无限积累。
	f.now = f.now.Add(10 * time.Second)
	f.slept = 0
	_ = p.Throttle(context.Background(), 1000)
	if f.slept != 0 {
		t.Fatalf("空闲后第一个突发不用等: %s", f.slept)
	}
	_ = p.Throttle(context.Background(), 1000)
	if f.slept != time.Second {
		t.Fatalf("桶不能无限积累: slept=%s", f.slept)
	}
	if err := p.Throttle(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsBadConfigs(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"seconds reversed": func(c *Config) { c.RequestSeconds = [2]int{55, 25} },
		"seconds too big":  func(c *Config) { c.RequestSeconds = [2]int{0, 60} },
		"quiet format":     func(c *Config) { c.HourQuiet[0] = "58" },
		"quiet range":      func(c *Config) { c.HourQuiet[1] = "61:00" },
		"negative gap":     func(c *Config) { c.RequestInterval = -1 },
		"no bandwidth":     func(c *Config) { c.MaxBytesPerSecond = 0 },
	} {
		cfg := Default()
		mutate(&cfg)
		if _, err := New(cfg, nil); err == nil {
			t.Fatalf("%s: 必须报错", name)
		}
	}
	p, err := New(Default(), nil)
	if err != nil || p.Interval() != 200*time.Millisecond {
		t.Fatalf("p=%v err=%v", p, err)
	}
	// 不跨整点的静默区间（10:00–12:00）。
	cfg := Default()
	cfg.HourQuiet = [2]string{"10:00", "12:00"}
	q := mustPacer(t, cfg, &fakeClock{now: at(1, 0, 0)})
	if got := q.AllowedAt(at(1, 10, 30)); !got.Equal(at(1, 12, 25)) {
		t.Fatalf("AllowedAt = %s", got.Format("15:04:05"))
	}
	if got := q.AllowedAt(at(1, 13, 30)); !got.Equal(at(1, 13, 30)) {
		t.Fatalf("AllowedAt = %s", got.Format("15:04:05"))
	}
}

func TestDisablingTheMinuteWindowKeepsTheHourQuietZone(t *testing.T) {
	cfg := Default()
	cfg.MinuteWindow = false
	p := mustPacer(t, cfg, &fakeClock{now: at(1, 10, 0)})
	// 关闭每分钟窗口：整分钟都可以发请求。
	if got := p.AllowedAt(at(1, 10, 3)); !got.Equal(at(1, 10, 3)) {
		t.Fatalf("关闭后每分钟开头也应放行: %s", got)
	}
	// 整点静默仍然生效。
	if got := p.AllowedAt(at(1, 58, 30)); !got.Equal(at(2, 4, 0)) {
		t.Fatalf("整点静默应仍生效: %s", got)
	}
}

func TestConfigRulesMatchTheRequestSeconds(t *testing.T) {
	r := Default().ConfigRules()
	if r.EveryMinutes != 1 || r.BeforeSeconds != 4 || r.AfterSeconds != 25 || r.HourQuiet == nil || *r.HourQuiet != [2]string{"58:00", "04:00"} || r.Source != "config" {
		t.Fatalf("rules = %+v", r)
	}
	cfg := Default()
	cfg.RequestSeconds, cfg.HourQuiet = [2]int{0, 59}, [2]string{"00:00", "00:00"}
	if r := cfg.ConfigRules(); r.EveryMinutes != 0 || r.HourQuiet != nil {
		t.Fatalf("整分钟可发、无静默: %+v", r)
	}
}

func TestSetRulesReplacesTheAvoidanceWhileRunning(t *testing.T) {
	p := mustPacer(t, Default(), &fakeClock{now: at(1, 0, 0)})
	// mgr 下发：每 5 分钟触发、[T−5s, T+25s) 不发、不避让整点。
	if err := p.SetRules(Rules{EveryMinutes: 5, BeforeSeconds: 5, AfterSeconds: 25, Source: "mgr"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ in, want time.Time }{
		{at(1, 4, 54), at(1, 4, 54)},  // 触发前 6 秒：可发
		{at(1, 4, 55), at(1, 5, 25)},  // :04:55～:05:25 不发
		{at(1, 5, 10), at(1, 5, 25)},  // 触发后 10 秒
		{at(1, 5, 25), at(1, 5, 25)},  // 触发后 25 秒起可发（半开区间）
		{at(1, 6, 3), at(1, 6, 3)},    // 非触发分钟的开头也可发
		{at(1, 59, 0), at(1, 59, 0)},  // 没有整点窗口
		{at(1, 59, 57), at(2, 0, 25)}, // 整点也是触发时刻
	} {
		if got := p.AllowedAt(tc.in); !got.Equal(tc.want) {
			t.Fatalf("AllowedAt(%s) = %s, want %s", tc.in.Format("15:04:05"), got.Format("15:04:05"), tc.want.Format("15:04:05"))
		}
	}
	// 改回每分钟触发并加整点窗口。
	if err := p.SetRules(Rules{EveryMinutes: 1, BeforeSeconds: 5, AfterSeconds: 25, HourQuiet: &[2]string{"58:00", "04:00"}, Source: "mgr"}); err != nil {
		t.Fatal(err)
	}
	if got := p.AllowedAt(at(1, 6, 3)); !got.Equal(at(1, 6, 25)) {
		t.Fatalf("每分钟触发: %s", got.Format("15:04:05"))
	}
	if got := p.AllowedAt(at(1, 30, 55)); !got.Equal(at(1, 31, 25)) {
		t.Fatalf("前 5 秒不发: %s", got.Format("15:04:05"))
	}
	if got := p.AllowedAt(at(1, 58, 30)); !got.Equal(at(2, 4, 25)) {
		t.Fatalf("整点窗口: %s", got.Format("15:04:05"))
	}
	// 不限秒、无整点：随时可发。
	if err := p.SetRules(Rules{Source: "mgr"}); err != nil {
		t.Fatal(err)
	}
	if got := p.AllowedAt(at(1, 0, 1)); !got.Equal(at(1, 0, 1)) {
		t.Fatalf("不限秒: %s", got.Format("15:04:05"))
	}
	// 非法规则被拒绝，原规则保留。
	if err := p.SetRules(Rules{EveryMinutes: 7}); err == nil {
		t.Fatal("触发间隔不整除 60 必须报错")
	}
	if err := p.SetRules(Rules{EveryMinutes: 1, BeforeSeconds: 30, AfterSeconds: 30}); err == nil {
		t.Fatal("秒窗口占满一分钟必须报错")
	}
	if r := p.Rules(); r.EveryMinutes != 0 || r.Source != "mgr" {
		t.Fatalf("rules = %+v", r)
	}
}

func TestMinuteWindowOffOverridesManagerRules(t *testing.T) {
	cfg := Default()
	cfg.MinuteWindow = false
	p := mustPacer(t, cfg, &fakeClock{now: at(1, 0, 0)})
	if err := p.SetRules(Rules{EveryMinutes: 1, BeforeSeconds: 5, AfterSeconds: 25, HourQuiet: &[2]string{"58:00", "04:00"}, Source: "mgr"}); err != nil {
		t.Fatal(err)
	}
	if got := p.AllowedAt(at(1, 10, 3)); !got.Equal(at(1, 10, 3)) {
		t.Fatalf("minute_window=false 时整分钟可发: %s", got.Format("15:04:05"))
	}
	if got := p.AllowedAt(at(1, 59, 0)); !got.Equal(at(2, 4, 0)) {
		t.Fatalf("整点窗口仍生效: %s", got.Format("15:04:05"))
	}
}

func TestSetRulesIsSafeForConcurrentUse(t *testing.T) {
	p := mustPacer(t, Default(), &fakeClock{now: at(1, 0, 0)})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_ = p.AllowedAt(time.Now())
					_ = p.Rules()
				}
			}
		}()
	}
	for i := 0; i < 200; i++ {
		every := []int{1, 5, 15}[i%3]
		if err := p.SetRules(Rules{EveryMinutes: every, BeforeSeconds: 5, AfterSeconds: 25, Source: "mgr"}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}
