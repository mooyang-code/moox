package cnstock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMain 给整个包的测试换上一个宽松的进程内限流器：用真实主机名加假网络的测试不必真的按每秒 2 个等待，
// 也不会在测试之间互相留下冷却状态。要测限流行为的用例自己创建限流器，通过 ClistQuery.Limiter 传入。
func TestMain(m *testing.M) {
	cfg := DefaultRequestLimitConfig()
	cfg.RequestsPerSecond, cfg.Burst, cfg.MaxConcurrent, cfg.CooldownFailures = 20, 50, 8, 50
	if err := ConfigureRequestLimit(cfg, nil); err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

// limitNetwork 是按主机脚本化的假网络：fail 决定某次请求的结果（空串表示成功），并记录每个主机实际收到的请求数。
type limitNetwork struct {
	mu      sync.Mutex
	fail    func(host string, n int) string // reset、s429、s403、s500；Retry-After 用 "s429:1800"
	perHost map[string]int
	order   []string
}

func (n *limitNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Hostname()
	n.mu.Lock()
	if n.perHost == nil {
		n.perHost = map[string]int{}
	}
	n.perHost[host]++
	count := n.perHost[host]
	n.order = append(n.order, host)
	n.mu.Unlock()
	action := ""
	if n.fail != nil {
		action = n.fail(host, count)
	}
	status, retryAfter := 200, ""
	switch {
	case action == "reset":
		return nil, errors.New("read: connection reset by peer")
	case strings.HasPrefix(action, "s"):
		var code int
		if _, err := fmt.Sscanf(strings.SplitN(action[1:], ":", 2)[0], "%d", &code); err == nil {
			status = code
		}
		if parts := strings.SplitN(action, ":", 2); len(parts) == 2 {
			retryAfter = parts[1]
		}
	}
	header := http.Header{}
	if retryAfter != "" {
		header.Set("Retry-After", retryAfter)
	}
	body := ""
	if status == 200 {
		body = `{"data":{"total":2,"diff":[{"f12":"A1"},{"f12":"A2"}]}}`
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
}

func (n *limitNetwork) requests(host string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.perHost[host]
}

func (n *limitNetwork) total() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.order)
}

func fetchWith(ctx context.Context, limiter *RequestLimiter, network *limitNetwork, query ClistQuery) (ClistResult, error) {
	query.PageSize = 2
	query.HTTPClient = &http.Client{Transport: network}
	query.Limiter = limiter
	query.Sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	return FetchClist(ctx, query, func(ClistItem) error { return nil })
}

// push2 连续失败到阈值后进入冷却：之后的列表请求不再碰 push2（零请求），直接用 push2test。
func TestFetchClistSkipsCoolingPrimaryAndFallsBack(t *testing.T) {
	cfg := DefaultRequestLimitConfig()
	cfg.CooldownFailures = 3
	limiter, _, events := newTestLimiter(cfg)
	network := &limitNetwork{fail: func(host string, _ int) string {
		if host == testPush2 {
			return "reset"
		}
		return ""
	}}
	for i := 0; i < 3; i++ {
		result, err := fetchWith(context.Background(), limiter, network, ClistQuery{})
		if err != nil || result.Base != EastMoneyPushFallbackBase {
			t.Fatalf("第 %d 次：result=%+v err=%v", i+1, result, err)
		}
	}
	if network.requests(testPush2) != 3 || len(cooldowns(events)) != 1 {
		t.Fatalf("push2 应收到 3 次请求后进入冷却：%d，冷却 %d 次", network.requests(testPush2), len(cooldowns(events)))
	}

	before := network.requests(testPush2)
	result, err := fetchWith(context.Background(), limiter, network, ClistQuery{})
	if err != nil || result.Base != EastMoneyPushFallbackBase || result.Total != 2 {
		t.Fatalf("冷却期间应直接回退 push2test：result=%+v err=%v", result, err)
	}
	if network.requests(testPush2) != before {
		t.Fatalf("push2 冷却期间不应再收到请求：%d -> %d", before, network.requests(testPush2))
	}
}

// 两个主机都在冷却：整次失败，错误能 errors.Is 到 ErrCoolingDown，不重试、不等待、零请求。
func TestFetchClistFailsWhenEveryHostIsCooling(t *testing.T) {
	limiter, _, _ := newTestLimiter(DefaultRequestLimitConfig())
	mustAcquire(t, limiter, testPush2)(Outcome{Kind: OutcomeThrottled, Status: 429})
	mustAcquire(t, limiter, testPush2Test)(Outcome{Kind: OutcomeThrottled, Status: 403})
	network := &limitNetwork{}
	slept := 0
	query := ClistQuery{PageRetries: 3, RetryBackoff: time.Second, Sleep: func(ctx context.Context, d time.Duration) error { slept++; return nil }}
	query.PageSize = 2
	query.HTTPClient = &http.Client{Transport: network}
	query.Limiter = limiter
	_, err := FetchClist(context.Background(), query, func(ClistItem) error { return nil })
	var cooling *CoolingDownError
	if !errors.Is(err, ErrCoolingDown) || !errors.As(err, &cooling) || errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v", err)
	}
	if network.total() != 0 || slept != 0 {
		t.Fatalf("冷却错误不应重试：请求 %d 次，等待 %d 次", network.total(), slept)
	}
}

// 只有备用地址可用时（从备用地址起步）也一样：它在冷却就整次失败，不去碰 push2。
func TestFetchClistFallbackBaseCoolingFailsWithoutTouchingPrimary(t *testing.T) {
	limiter, _, _ := newTestLimiter(DefaultRequestLimitConfig())
	mustAcquire(t, limiter, testPush2Test)(Outcome{Kind: OutcomeThrottled, Status: 429})
	network := &limitNetwork{}
	_, err := fetchWith(context.Background(), limiter, network, ClistQuery{BaseURL: EastMoneyPushFallbackBase})
	if !errors.Is(err, ErrCoolingDown) || network.total() != 0 {
		t.Fatalf("err=%v 请求 %d 次", err, network.total())
	}
}

// 服务端返回 429 就进入冷却，Retry-After 比默认冷却长时取它；这次请求自己仍返回原来的限流错误，下一次才是冷却。
func TestFetchClistThrottleStartsCooldown(t *testing.T) {
	limiter, _, events := newTestLimiter(DefaultRequestLimitConfig())
	network := &limitNetwork{fail: func(host string, _ int) string {
		if host == testPush2 {
			return "s429:1800"
		}
		return ""
	}}
	_, err := fetchWith(context.Background(), limiter, network, ClistQuery{})
	if !errors.Is(err, ErrRateLimited) || errors.Is(err, ErrCoolingDown) {
		t.Fatalf("触发冷却的那次请求应返回原来的 429 错误：%v", err)
	}
	if got := cooldowns(events); len(got) != 1 || got[0].Cooldown != 30*time.Minute || got[0].Reason != ReasonRateLimited {
		t.Fatalf("冷却 = %+v", got)
	}
	result, err := fetchWith(context.Background(), limiter, network, ClistQuery{})
	if err != nil || result.Base != EastMoneyPushFallbackBase || network.requests(testPush2) != 1 {
		t.Fatalf("下一次应跳过 push2：result=%+v err=%v push2 请求 %d 次", result, err, network.requests(testPush2))
	}
}

// 403 同样触发冷却。
func TestFetchClistForbiddenStartsCooldown(t *testing.T) {
	limiter, _, events := newTestLimiter(DefaultRequestLimitConfig())
	network := &limitNetwork{fail: func(host string, _ int) string {
		if host == testPush2 {
			return "s403"
		}
		return ""
	}}
	if _, err := fetchWith(context.Background(), limiter, network, ClistQuery{}); !errors.Is(err, ErrHTTPStatus) {
		t.Fatalf("err = %v", err)
	}
	if got := cooldowns(events); len(got) != 1 || got[0].Reason != ReasonForbidden {
		t.Fatalf("冷却 = %+v", got)
	}
}

// 取消 ctx 引起的失败不计入连续失败：反复取消也不会让主机进入冷却。
func TestFetchClistCancelDoesNotCountAsFailure(t *testing.T) {
	limiter, _, events := newTestLimiter(DefaultRequestLimitConfig())
	network := &limitNetwork{}
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _ = fetchWith(ctx, limiter, network, ClistQuery{})
	}
	if len(cooldowns(events)) != 0 || network.total() != 0 {
		t.Fatalf("取消不应触发冷却，也不应发出请求：冷却 %d 次，请求 %d 次", len(cooldowns(events)), network.total())
	}
	// 请求在途中取消：同样不计失败。
	slow := &cancelNetwork{}
	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		slow.cancel = cancel
		query := ClistQuery{PageSize: 2, HTTPClient: &http.Client{Transport: slow}, Limiter: limiter}
		_, _ = FetchClist(ctx, query, func(ClistItem) error { return nil })
		cancel()
	}
	if len(cooldowns(events)) != 0 {
		t.Fatal("请求途中取消不应触发冷却")
	}
}

type cancelNetwork struct{ cancel context.CancelFunc }

func (n *cancelNetwork) RoundTrip(req *http.Request) (*http.Response, error) {
	n.cancel()
	return nil, req.Context().Err()
}

// 5xx 回退到 push2test 的老行为不变，并且 5xx 本身不计入冷却。
func TestFetchClistServerErrorsDoNotCoolDown(t *testing.T) {
	limiter, _, events := newTestLimiter(DefaultRequestLimitConfig())
	network := &limitNetwork{fail: func(host string, _ int) string {
		if host == testPush2 {
			return "s502"
		}
		return ""
	}}
	for i := 0; i < 10; i++ {
		if _, err := fetchWith(context.Background(), limiter, network, ClistQuery{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(cooldowns(events)) != 0 || network.requests(testPush2) != 10 {
		t.Fatalf("冷却 %d 次，push2 请求 %d 次", len(cooldowns(events)), network.requests(testPush2))
	}
}

// 每一次请求都过限流器：翻页、重试、回退各占一个令牌。
func TestFetchClistEveryRequestPassesTheLimiter(t *testing.T) {
	limiter, _, events := newTestLimiter(DefaultRequestLimitConfig())
	network := &limitNetwork{fail: func(host string, n int) string {
		if host == testPush2 {
			return "reset"
		}
		if host == testPush2Test && n == 1 {
			return "s502"
		}
		return ""
	}}
	if _, err := fetchWith(context.Background(), limiter, network, ClistQuery{PageRetries: 1}); err != nil {
		t.Fatal(err)
	}
	sent := 0
	for _, event := range *events {
		if event.Kind == EventSent {
			sent++
		}
	}
	if sent != network.total() || sent < 3 {
		t.Fatalf("限流器放行 %d 次，实际请求 %d 次", sent, network.total())
	}
}

// 默认限流器下，非东方财富主机（本地测试服务）不受限流。
func TestFetchClistNonEastMoneyHostBypassesLimiter(t *testing.T) {
	limiter, clock, events := newTestLimiter(DefaultRequestLimitConfig())
	network := &limitNetwork{}
	for i := 0; i < 5; i++ {
		if _, err := fetchWith(context.Background(), limiter, network, ClistQuery{BaseURL: "http://127.0.0.1:9"}); err != nil {
			t.Fatal(err)
		}
	}
	if len(clock.Sleeps()) != 0 || len(*events) != 0 {
		t.Fatalf("不应限流：%v %v", clock.Sleeps(), *events)
	}
}
