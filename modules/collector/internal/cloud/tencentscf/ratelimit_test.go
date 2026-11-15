package tencentscf

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"golang.org/x/time/rate"
)

// fakeClock 让令牌桶与退避在测试里不真正睡眠：睡眠只推进假时钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	f.now = f.now.Add(d)
	f.mu.Unlock()
	return nil
}

func useFakeClock(t *testing.T) *fakeClock {
	t.Helper()
	// 假时钟从真实当前时间起步：ctx 的截止时间用真实时钟判断，必须落在将来。
	clock := &fakeClock{now: time.Now()}
	oldNow, oldSleep, oldDelay := limiterNow, limiterSleep, retryDelay
	limiterNow, limiterSleep = clock.Now, clock.Sleep
	retryDelay = func() time.Duration { return time.Second }
	require.NoError(t, ConfigureRateLimits(nil))
	t.Cleanup(func() {
		limiterNow, limiterSleep, retryDelay = oldNow, oldSleep, oldDelay
		require.NoError(t, ConfigureRateLimits(nil))
	})
	return clock
}

func rateLimited() error {
	return tcerrors.NewTencentCloudSDKError("RequestLimitExceeded", "您当前每秒请求 `21` 次，超过了每秒频率上限 `20`", "req-1")
}

// maxPerWindow 返回任意长度为 window 的滑动窗口内的最大请求数。
func maxPerWindow(stamps []time.Time, window time.Duration) int {
	sorted := append([]time.Time(nil), stamps...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Before(sorted[j]) })
	best := 0
	for i := range sorted {
		count := 0
		for j := i; j < len(sorted) && sorted[j].Sub(sorted[i]) < window; j++ {
			count++
		}
		best = max(best, count)
	}
	return best
}

func TestDefaultRatesAreSeventyFivePercentOfOfficialLimit(t *testing.T) {
	require.NoError(t, ConfigureRateLimits(nil))
	limiterRegistry.Lock()
	defer limiterRegistry.Unlock()
	require.InDelta(t, 15, rateFor("ListTriggers"), 1e-9)
	require.InDelta(t, 15, rateFor("UpdateTrigger"), 1e-9)
	require.InDelta(t, 75, rateFor("GetFunction"), 1e-9)
	require.InDelta(t, 75, rateFor("Invoke"), 1e-9)
	require.InDelta(t, unknownActionRate, rateFor("SomethingNew"), 1e-9)
}

func TestLimiterNeverExceedsRateInAnyOneSecondWindow(t *testing.T) {
	clock := useFakeClock(t)
	client := New("acct", "id", "key")
	var stamps []time.Time
	// 回读 42 个函数：每个函数一次 ListTriggers，突发容量为 1，任意 1 秒窗口不超过 15 次。
	for i := 0; i < 42; i++ {
		require.NoError(t, client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error {
			stamps = append(stamps, clock.Now())
			return nil
		}))
	}
	require.Len(t, stamps, 42)
	require.LessOrEqual(t, maxPerWindow(stamps, time.Second), 15)
	// 42 次按 15 次/秒摊开，总耗时约 2.7 秒。
	elapsed := stamps[len(stamps)-1].Sub(stamps[0])
	require.Greater(t, elapsed, 2500*time.Millisecond)
	require.Less(t, elapsed, 3*time.Second)
}

func TestLimiterConcurrentCallersShareOneBucket(t *testing.T) {
	// 官方限频是 20 次/秒，真实计时的并发测试要更高的速率才跑得快，直接写入覆盖表绕过校验。
	limiterRegistry.Lock()
	limiterRegistry.overrides = map[string]float64{"ListTriggers": 100}
	limiterRegistry.limiters = make(map[limiterKey]*rate.Limiter)
	limiterRegistry.Unlock()
	t.Cleanup(func() { require.NoError(t, ConfigureRateLimits(nil)) })
	// 多个 Client 实例（同一账号换了密钥）同时调用，也共用一个桶。
	clients := []*Client{New("acct-concurrent", "id-old", "key"), New("acct-concurrent", "id-new", "key")}
	var (
		mu     sync.Mutex
		stamps []time.Time
		wg     sync.WaitGroup
	)
	// 250 个样本按 100 次/秒要跑约 2.5 秒，窗口断言覆盖多个完整的 1 秒窗口，不会因为样本太少而必然通过。
	for i := 0; i < 250; i++ {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			require.NoError(t, client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error {
				mu.Lock()
				stamps = append(stamps, time.Now())
				mu.Unlock()
				return nil
			}))
		}(clients[i%len(clients)])
	}
	wg.Wait()
	require.Len(t, stamps, 250)
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Before(stamps[j]) })
	span := stamps[len(stamps)-1].Sub(stamps[0])
	require.Greater(t, span, 2200*time.Millisecond, "250 个调用按 100 次/秒至少要跑 2.5 秒，说明确实被限流摊开了")
	// 任意 1 秒窗口的次数由假时钟测试精确验证（TestLimiterNeverExceedsRateInAnyOneSecondWindow）；
	// 真实计时的并发测试只确认共桶与总体耗时，不依赖调度的精确性。
}

func TestLimiterBurstOfOneNeverReleasesTwoCallsAtTheSameInstant(t *testing.T) {
	clock := useFakeClock(t)
	client := New("acct-burst", "id", "key")
	var stamps []time.Time
	for i := 0; i < 5; i++ {
		require.NoError(t, client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error {
			stamps = append(stamps, clock.Now())
			return nil
		}))
	}
	for i := 1; i < len(stamps); i++ {
		// 15 次/秒：相邻调用至少间隔约 66ms。突发容量等于速率时，前 15 个会在同一时刻放行。
		require.GreaterOrEqual(t, stamps[i].Sub(stamps[i-1]), 60*time.Millisecond)
	}
}

func TestLimiterKeyedByAccountAndActionNotBySecret(t *testing.T) {
	useFakeClock(t)
	a := New("acct-1", "secret-old", "k")
	b := New("acct-1", "secret-new", "k")
	other := New("acct-2", "secret-old", "k")
	keyOf := func(c *Client, action string) limiterKey {
		return limiterKey{account: c.limitKey, endpoint: scfEndpoint("ap-hongkong"), action: action}
	}
	require.Same(t, limiterFor(keyOf(a, "ListTriggers")), limiterFor(keyOf(b, "ListTriggers")), "同一账号换密钥必须共用令牌桶")
	require.NotSame(t, limiterFor(keyOf(a, "ListTriggers")), limiterFor(keyOf(other, "ListTriggers")))
	require.NotSame(t, limiterFor(keyOf(a, "ListTriggers")), limiterFor(keyOf(a, "UpdateTrigger")))
	// 未提供账号标识时退回按 SecretID 区分。
	require.Equal(t, "secret:abc", New("", "abc", "k").limitKey)
	require.NotSame(t,
		limiterFor(limiterKey{account: scfEndpoint("ap-hongkong"), action: "ListTriggers"}),
		limiterFor(limiterKey{account: "x", endpoint: scfEndpoint("ap-shanghai-fsi"), action: "ListTriggers"}))
	require.NotEqual(t, scfEndpoint("ap-hongkong"), scfEndpoint("ap-shanghai-fsi"))
}

func TestCallDoesNotSendWhenTokenWaitExceedsDeadline(t *testing.T) {
	clock := useFakeClock(t)
	client := New("acct", "id", "key")
	// 先用掉当前令牌，下一个令牌要等约 66ms。
	require.NoError(t, client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error { return nil }))
	ctx, cancel := context.WithDeadline(context.Background(), clock.Now().Add(40*time.Millisecond))
	defer cancel()
	sent := false
	err := client.call(ctx, "ap-hongkong", "ListTriggers", func(context.Context) error { sent = true; return nil })
	require.ErrorIs(t, err, ErrRateLimitWaitExceedsDeadline)
	require.NoError(t, ctx.Err(), "此时 ctx 本身尚未到期，错误必须能被识别")
	require.False(t, sent)
	// 被取消的预约把令牌还回去，后续调用不被拖慢。
	require.NoError(t, client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error { return nil }))
}

func TestCallStopsWaitingWhenContextCanceled(t *testing.T) {
	useFakeClock(t)
	client := New("acct", "id", "key")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := client.call(ctx, "ap-hongkong", "Invoke", func(context.Context) error { t.Fatal("不应发出请求"); return nil })
	require.ErrorIs(t, err, context.Canceled)
}

func TestCallRetriesRateLimitExceededWithJitter(t *testing.T) {
	useFakeClock(t)
	client := New("acct", "id", "key")
	attempts := 0
	err := client.call(context.Background(), "ap-hongkong", "CreateFunction", func(context.Context) error {
		attempts++
		if attempts <= 2 {
			return rateLimited()
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, 3, attempts, "Create 类接口在限频后重试是安全的：请求在服务端被拒，没有执行")
}

func TestCallReturnsOriginalErrorAfterRetriesExhausted(t *testing.T) {
	useFakeClock(t)
	client := New("acct", "id", "key")
	attempts := 0
	err := client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error { attempts++; return rateLimited() })
	require.Equal(t, rateLimitRetries+1, attempts)
	var sdkErr *tcerrors.TencentCloudSDKError
	require.True(t, errors.As(err, &sdkErr))
	require.Equal(t, "RequestLimitExceeded", sdkErr.GetCode())
}

func TestCallDoesNotRetryOtherErrors(t *testing.T) {
	useFakeClock(t)
	client := New("acct", "id", "key")
	attempts := 0
	boom := tcerrors.NewTencentCloudSDKError("InvalidParameterValue", "bad", "req-2")
	err := client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error { attempts++; return boom })
	require.Equal(t, 1, attempts)
	require.Equal(t, boom, err)
	plain := errors.New("connection reset")
	attempts = 0
	require.Equal(t, plain, client.call(context.Background(), "ap-hongkong", "ListTriggers", func(context.Context) error { attempts++; return plain }))
	require.Equal(t, 1, attempts)
}

func TestCallSkipsRetryWhenBackoffWouldPassDeadline(t *testing.T) {
	clock := useFakeClock(t)
	client := New("acct", "id", "key")
	ctx, cancel := context.WithDeadline(context.Background(), clock.Now().Add(500*time.Millisecond))
	defer cancel()
	attempts := 0
	err := client.call(ctx, "ap-hongkong", "ListTriggers", func(context.Context) error { attempts++; return rateLimited() })
	require.Equal(t, 1, attempts, "退避 1 秒会越过截止时间，直接返回限频错误交给外层重试")
	require.True(t, isRateLimitExceeded(err))
}

func TestRetryWaitRespectsContextCancel(t *testing.T) {
	useFakeClock(t)
	client := New("acct", "id", "key")
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := client.call(ctx, "ap-hongkong", "ListTriggers", func(context.Context) error {
		attempts++
		cancel()
		return rateLimited()
	})
	require.Equal(t, 1, attempts)
	require.ErrorIs(t, err, context.Canceled)
}

func TestRateLimitConfigValidation(t *testing.T) {
	require.NoError(t, ValidateRateLimits(nil))
	require.NoError(t, ValidateRateLimits(map[string]float64{"ListTriggers": 20, "GetFunction": 10}))
	require.ErrorContains(t, ValidateRateLimits(map[string]float64{"NoSuchAction": 5}), "未知接口")
	require.ErrorContains(t, ValidateRateLimits(map[string]float64{"ListTriggers": 0}), "必须大于 0")
	require.ErrorContains(t, ValidateRateLimits(map[string]float64{"ListTriggers": -1}), "必须大于 0")
	require.Error(t, ValidateRateLimits(map[string]float64{"ListTriggers": math.NaN()}), "YAML 的 .nan 必须被拒绝")
	require.Error(t, ValidateRateLimits(map[string]float64{"ListTriggers": math.Inf(1)}))
	require.Error(t, ValidateRateLimits(map[string]float64{"ListTriggers": math.Inf(-1)}))
	require.ErrorContains(t, ValidateRateLimits(map[string]float64{"ListTriggers": 21}), "不超过官方限频 20")
	require.Error(t, ConfigureRateLimits(map[string]float64{"UpdateTrigger": 100}))
}

func TestConfiguredOverrideWinsOverDefault(t *testing.T) {
	require.NoError(t, ConfigureRateLimits(map[string]float64{"ListTriggers": 5}))
	t.Cleanup(func() { require.NoError(t, ConfigureRateLimits(nil)) })
	limiterRegistry.Lock()
	defer limiterRegistry.Unlock()
	require.InDelta(t, 5, rateFor("ListTriggers"), 1e-9)
	require.InDelta(t, 15, rateFor("UpdateTrigger"), 1e-9)
}

// TestEverySDKCallGoesThroughCall 扫描本包：SDK 的 XxxWithContext 调用必须出现在 c.call 的闭包里，
// 防止新增调用绕过限流。
func TestEverySDKCallGoesThroughCall(t *testing.T) {
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, 0)
		require.NoError(t, err)
		var stack []ast.Node
		ast.Inspect(file, func(instance ast.Node) bool {
			if instance == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, instance)
			call, ok := instance.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !strings.HasSuffix(selector.Sel.Name, "WithContext") || selector.Sel.Name == "WithContext" {
				return true
			}
			checked++
			require.Truef(t, insideCallClosure(stack), "%s: %s 绕过了 c.call", name, selector.Sel.Name)
			return true
		})
	}
	require.GreaterOrEqual(t, checked, 17, "扫描到的 SDK 调用数量异常，扫描规则可能失效")
}

func insideCallClosure(stack []ast.Node) bool {
	for i := len(stack) - 2; i >= 1; i-- {
		lit, ok := stack[i].(*ast.FuncLit)
		if !ok {
			continue
		}
		outer, ok := stack[i-1].(*ast.CallExpr)
		if !ok {
			continue
		}
		for _, arg := range outer.Args {
			if arg != ast.Expr(lit) {
				continue
			}
			switch fun := outer.Fun.(type) {
			case *ast.SelectorExpr:
				return fun.Sel.Name == "call"
			case *ast.Ident:
				return fun.Name == "call"
			}
		}
	}
	return false
}
