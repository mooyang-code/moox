package httpclient

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// hostState 是一个域名的进程级限流状态：同一 IP 上所有账户共用权重和封禁。
type hostState struct {
	mu     sync.Mutex
	limit  int
	minute int64
	used   int
	until  time.Time
	reason exchange.ErrorKind
}

var hosts sync.Map

func hostKey(baseURL string) string {
	if parsed, err := url.Parse(baseURL); err == nil && parsed.Host != "" {
		return parsed.Host
	}
	return baseURL
}

func hostOf(baseURL string) *hostState {
	value, _ := hosts.LoadOrStore(hostKey(baseURL), &hostState{})
	return value.(*hostState)
}

func (h *hostState) setLimit(limit int) {
	h.mu.Lock()
	h.limit = limit
	h.mu.Unlock()
}

// recordWeight 记录响应头里的已用权重。同一分钟取最大值：部分接口的响应头不准（例如 bookTicker）。
func (h *hostState) recordWeight(header http.Header, now time.Time) bool {
	raw := strings.TrimSpace(header.Get("X-MBX-USED-WEIGHT-1M"))
	if raw == "" {
		return false
	}
	used, err := strconv.Atoi(raw)
	if err != nil || used < 0 {
		return false
	}
	minute := now.Unix() / 60
	h.mu.Lock()
	defer h.mu.Unlock()
	if minute != h.minute {
		h.minute, h.used = minute, 0
	}
	if used > h.used {
		h.used = used
	}
	return true
}

func (h *hostState) pause(until time.Time, reason exchange.ErrorKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if until.After(h.until) {
		h.until, h.reason = until, reason
	}
}

func (h *hostState) pausedUntil(now time.Time) (time.Time, exchange.ErrorKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if now.Before(h.until) {
		return h.until, h.reason
	}
	return time.Time{}, ""
}

// snapshot 返回本分钟用量。读数按分钟作废，新一分钟没有响应就当 0。
func (h *hostState) snapshot(now time.Time) (int, int, time.Time, exchange.ErrorKind) {
	h.mu.Lock()
	defer h.mu.Unlock()
	used := 0
	if h.minute == now.Unix()/60 {
		used = h.used
	}
	var until time.Time
	var reason exchange.ErrorKind
	if now.Before(h.until) {
		until, reason = h.until, h.reason
	}
	return used, h.limit, until, reason
}

// pauseState 是单个客户端（账户）的下单暂停。
type pauseState struct {
	mu     sync.Mutex
	at     time.Time
	reason exchange.ErrorKind
}

func (p *pauseState) extend(until time.Time, reason exchange.ErrorKind) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if until.After(p.at) {
		p.at, p.reason = until, reason
	}
}

func (p *pauseState) until(now time.Time) (time.Time, exchange.ErrorKind) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if now.Before(p.at) {
		return p.at, p.reason
	}
	return time.Time{}, ""
}
