package binance

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"trpc.group/trpc-go/trpc-go/log"
)

// Binance 对每个出口 IP 按分钟统计请求权重（现货 6000、合约 2400），超限先返回 429，继续请求会升级为 418 封禁 IP。
// 每个响应都带 X-MBX-USED-WEIGHT-1M，是该 IP 当前分钟的总用量（同一出口 IP 上的所有函数共享），
// 所以读取它既是自己的用量也是整个函数群在这个 IP 上的用量：达到软上限就等到下一分钟，
// 429 按 Retry-After 退避，418 立即停止，不再给封禁添柴。
const (
	spotWeightLimit = 6000
	swapWeightLimit = 2400
	// softWeightRatio 是开始主动放慢的已用权重占比。
	softWeightRatio = 0.7
)

type weightState struct {
	used         int
	observedAt   time.Time
	blockedUntil time.Time
	banned       bool
}

// weightGovernor 是进程级的限流状态，按产品（spot/swap）分开。
type weightGovernor struct {
	mu     sync.Mutex
	now    func() time.Time
	states map[string]*weightState
}

func newWeightGovernor(now func() time.Time) *weightGovernor {
	if now == nil {
		now = time.Now
	}
	return &weightGovernor{now: now, states: map[string]*weightState{}}
}

var defaultWeightGovernor = newWeightGovernor(nil)

func productForDomain(domain string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(domain)), "fapi") {
		return "swap"
	}
	return "spot"
}

func weightLimit(product string) int {
	if product == "swap" {
		return swapWeightLimit
	}
	return spotWeightLimit
}

func (g *weightGovernor) state(product string) *weightState {
	state := g.states[product]
	if state == nil {
		state = &weightState{}
		g.states[product] = state
	}
	return state
}

// Observe 记录一次响应：已用权重、429 的 Retry-After、418 封禁。
func (g *weightGovernor) Observe(domain string, status int, header http.Header) {
	product := productForDomain(domain)
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	state := g.state(product)
	if value := strings.TrimSpace(header.Get("X-MBX-USED-WEIGHT-1M")); value != "" {
		if used, err := strconv.Atoi(value); err == nil {
			state.used, state.observedAt = used, now
			marketdata.NoteUsedWeight(used)
		}
	}
	retryAfter := time.Duration(0)
	if seconds, err := strconv.Atoi(strings.TrimSpace(header.Get("Retry-After"))); err == nil && seconds > 0 {
		retryAfter = time.Duration(seconds) * time.Second
	}
	switch status {
	case http.StatusTeapot:
		state.banned = true
		if retryAfter <= 0 {
			retryAfter = 2 * time.Minute
		}
		state.blockedUntil = now.Add(retryAfter)
		log.Warnf("binance_ip_banned product=%s retry_after=%s", product, retryAfter)
	case http.StatusTooManyRequests:
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		if until := now.Add(retryAfter); until.After(state.blockedUntil) {
			state.blockedUntil = until
		}
		log.Warnf("binance_rate_limited product=%s used=%d retry_after=%s", product, state.used, retryAfter)
	}
}

// Admit 在发请求前调用：必要时等待，等不起（超过 ctx 截止时间）或被封禁就直接返回限流错误。
func (g *weightGovernor) Admit(ctx context.Context, domain string) error {
	product := productForDomain(domain)
	for {
		now := g.now()
		g.mu.Lock()
		state := g.state(product)
		var wait time.Duration
		var reason string
		switch {
		case state.banned && now.Before(state.blockedUntil):
			g.mu.Unlock()
			return fmt.Errorf("%w: binance %s IP is banned until %s", marketdata.ErrRateLimited, product, state.blockedUntil.UTC().Format(time.RFC3339))
		case now.Before(state.blockedUntil):
			wait, reason = state.blockedUntil.Sub(now), "retry-after"
		case state.used >= int(float64(weightLimit(product))*softWeightRatio) && !state.observedAt.IsZero() &&
			state.observedAt.Truncate(time.Minute).Equal(now.Truncate(time.Minute)):
			wait, reason = now.Truncate(time.Minute).Add(time.Minute).Sub(now), "soft-limit"
		default:
			if state.banned {
				state.banned = false
			}
			g.mu.Unlock()
			return nil
		}
		used := state.used
		g.mu.Unlock()
		if deadline, ok := ctx.Deadline(); ok && now.Add(wait).After(deadline) {
			return fmt.Errorf("%w: binance %s %s wait %s exceeds the request budget (used weight %d)", marketdata.ErrRateLimited, product, reason, wait.Round(time.Millisecond), used)
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}
