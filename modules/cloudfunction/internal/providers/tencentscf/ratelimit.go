package tencentscf

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	tcerrors "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/errors"
	"golang.org/x/time/rate"
	"trpc.group/trpc-go/trpc-go/log"
)

// 腾讯云云 API 默认按「API + 接入地域 + 子账号」限频（https://cloud.tencent.com/document/product/1278/109059）。
// FunctionInstance 是唯一调用 SCF 管理接口的进程，进程内令牌桶即全局限流。

// officialRateLimits 是云函数各接口的官方默认限频（次/秒），来源：https://cloud.tencent.com/document/product/583/17235。
var officialRateLimits = map[string]float64{
	"ListTriggers":                    20,
	"UpdateTrigger":                   20,
	"ListNamespaces":                  20,
	"CreateNamespace":                 20,
	"UpdateFunctionEventInvokeConfig": 20,
	"PutReservedConcurrencyConfig":    20,
	"GetFunction":                     100,
	"CreateTrigger":                   100,
	"DeleteTrigger":                   100,
	"CreateFunction":                  100,
	"DeleteFunction":                  100,
	"UpdateFunctionConfiguration":     100,
	"UpdateFunctionCode":              100,
	"ListFunctions":                   100,
	"Invoke":                          100,
}

const (
	// defaultRateRatio 是默认速率占官方限频的比例，给主账号级、IP 级等未公开的限频策略留余量。
	defaultRateRatio = 0.75
	// unknownActionRate 是未登记接口的默认速率，按最常见的 20 次/秒限频取 75%。
	unknownActionRate = 15
	// rateLimitRetries 是遇到 RequestLimitExceeded 后的最大重试次数（共 retries+1 次尝试）。
	rateLimitRetries = 2
	rateLimitCode    = "RequestLimitExceeded"
)

// ErrRateLimitWaitExceedsDeadline 表示等下一个令牌所需的时间超过了 ctx 的截止时间，请求没有发出。
// 这是调用方预算不足而不是云端失败，外层按瞬时错误重试。
var ErrRateLimitWaitExceedsDeadline = errors.New("tencent api rate limit wait exceeds context deadline")

var (
	apiWaitSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "moox_cloudfunction_tencent_api_wait_seconds",
		Help:    "Time spent waiting for a local rate limit token before calling a Tencent Cloud API.",
		Buckets: []float64{0.001, 0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10},
	}, []string{"action"})
	apiThrottledTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "moox_cloudfunction_tencent_api_throttled_total",
		Help: "Tencent Cloud API calls rejected with RequestLimitExceeded.",
	}, []string{"action"})
)

func init() {
	prometheus.MustRegister(apiWaitSeconds, apiThrottledTotal)
}

type limiterKey struct {
	account  string
	endpoint string
	action   string
}

// limiterRegistry 是进程级令牌桶注册表。scfClient 每次操作都会新建 Client，限流器不能挂在 Client 上。
var limiterRegistry = struct {
	sync.Mutex
	overrides map[string]float64
	limiters  map[limiterKey]*rate.Limiter
}{limiters: make(map[limiterKey]*rate.Limiter)}

// 以下变量供测试替换时钟、睡眠与退避。
var (
	limiterNow   = time.Now
	limiterSleep = sleepContext
	retryDelay   = func() time.Duration { return 500*time.Millisecond + rand.N(1500*time.Millisecond) }
)

// ValidateRateLimits 校验按接口覆盖的速率：接口名必须是已知接口，值在 (0, 官方限频] 内。
func ValidateRateLimits(overrides map[string]float64) error {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		official, ok := officialRateLimits[name]
		if !ok {
			return fmt.Errorf("tencent_scf.api_rate_limits 包含未知接口 %q", name)
		}
		value := overrides[name]
		// YAML 允许 .nan / .inf：NaN 与 0 或负数的比较都为假，必须显式拒绝非有限值。
		if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 || value > official {
			return fmt.Errorf("tencent_scf.api_rate_limits.%s 必须大于 0 且不超过官方限频 %g，当前为 %g", name, official, value)
		}
	}
	return nil
}

// ConfigureRateLimits 设置按接口覆盖的速率并清空已创建的令牌桶。进程启动时调用一次。
func ConfigureRateLimits(overrides map[string]float64) error {
	if err := ValidateRateLimits(overrides); err != nil {
		return err
	}
	limiterRegistry.Lock()
	defer limiterRegistry.Unlock()
	limiterRegistry.overrides = make(map[string]float64, len(overrides))
	for name, value := range overrides {
		limiterRegistry.overrides[name] = value
	}
	limiterRegistry.limiters = make(map[limiterKey]*rate.Limiter)
	return nil
}

// rateFor 返回接口的生效速率：配置覆盖优先，其次官方限频的 75%，未登记接口取 15。调用方需持有注册表锁。
func rateFor(action string) float64 {
	if value, ok := limiterRegistry.overrides[action]; ok {
		return value
	}
	if official, ok := officialRateLimits[action]; ok {
		return official * defaultRateRatio
	}
	return unknownActionRate
}

// limiterFor 返回（账号, 接入点, 接口）对应的令牌桶。突发容量为 1：桶初始是满的，
// 突发容量等于速率时第一秒可放行约两倍速率，仍会超限。
func limiterFor(key limiterKey) *rate.Limiter {
	limiterRegistry.Lock()
	defer limiterRegistry.Unlock()
	limiter, ok := limiterRegistry.limiters[key]
	if !ok {
		limiter = rate.NewLimiter(rate.Limit(rateFor(key.action)), 1)
		limiterRegistry.limiters[key] = limiter
	}
	return limiter
}

// call 是所有腾讯云 SCF API 调用的统一入口：先取令牌，再执行；遇到 RequestLimitExceeded 抖动退避重试。
// 不额外设置超时，只沿用调用方的 ctx（Invoke 有 960 秒的 HTTP 预算）。
func (c *Client) call(ctx context.Context, region, action string, fn func(context.Context) error) error {
	key := limiterKey{account: c.limitKey, endpoint: scfEndpoint(region), action: action}
	for attempt := 0; ; attempt++ {
		if err := acquireToken(ctx, key); err != nil {
			return err
		}
		err := fn(ctx)
		if !isRateLimitExceeded(err) {
			return err
		}
		apiThrottledTotal.WithLabelValues(action).Inc()
		requestID := ""
		var sdkErr *tcerrors.TencentCloudSDKError
		if errors.As(err, &sdkErr) {
			requestID = sdkErr.GetRequestId()
		}
		log.WarnContextf(ctx, "[CloudFunction-TencentSCF] %s 被腾讯云限频 attempt=%d/%d request_id=%s", action, attempt+1, rateLimitRetries+1, requestID)
		if attempt >= rateLimitRetries {
			return err
		}
		delay := retryDelay()
		if deadline, ok := ctx.Deadline(); ok && limiterNow().Add(delay).After(deadline) {
			return err
		}
		if sleepErr := limiterSleep(ctx, delay); sleepErr != nil {
			return sleepErr
		}
	}
}

// acquireToken 等待令牌；等待时间会超过 ctx 截止时间时不发请求，返回 ErrRateLimitWaitExceedsDeadline。
func acquireToken(ctx context.Context, key limiterKey) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := limiterNow()
	reservation := limiterFor(key).ReserveN(now, 1)
	if !reservation.OK() {
		return fmt.Errorf("%w: action=%s", ErrRateLimitWaitExceedsDeadline, key.action)
	}
	delay := reservation.DelayFrom(now)
	if delay <= 0 {
		apiWaitSeconds.WithLabelValues(key.action).Observe(0)
		return nil
	}
	if deadline, ok := ctx.Deadline(); ok && now.Add(delay).After(deadline) {
		reservation.CancelAt(now)
		return fmt.Errorf("%w: action=%s wait=%s", ErrRateLimitWaitExceedsDeadline, key.action, delay)
	}
	if err := limiterSleep(ctx, delay); err != nil {
		reservation.CancelAt(limiterNow())
		return err
	}
	apiWaitSeconds.WithLabelValues(key.action).Observe(delay.Seconds())
	return nil
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRateLimitExceeded(err error) bool {
	if err == nil {
		return false
	}
	var sdkErr *tcerrors.TencentCloudSDKError
	if errors.As(err, &sdkErr) {
		return strings.HasPrefix(sdkErr.GetCode(), rateLimitCode)
	}
	return false
}
