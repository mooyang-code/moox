package marketfetch

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// fundingSlotSpan 是缓存的粒度：一个小时槽保存结算时刻落在这一小时内的费率之和。
	fundingSlotSpan = time.Hour
	// fundingPublishDelay：整点后约 2 分钟取上一个整点开始的这一小时（结算发生在整点，或晚 1 毫秒）。
	fundingPublishDelay = 2 * time.Minute
	// fundingRefreshLead：这一小时收盘前约 5 分钟再取一次覆盖，避免个别晚公布的记录被写成 0。
	fundingRefreshLead = 5 * time.Minute
	// fundingRetryBase、fundingRetryMax 是小时槽取数失败后的重试间隔（连续失败翻倍）。
	fundingRetryBase = 5 * time.Second
	fundingRetryMax  = time.Minute
	// fundingFetchTimeout 是一次全市场一小时查询的时长上限（含限速排队）。
	fundingFetchTimeout = time.Minute
)

// FundingClock 是资金费缓存使用的时钟，测试注入假时钟。
type FundingClock interface {
	Now() time.Time
	// AfterFunc 在 d 之后调用 f；返回的函数取消尚未触发的调用。
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type systemFundingClock struct{}

func (systemFundingClock) Now() time.Time { return time.Now() }
func (systemFundingClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// FundingCache 是进程内按小时的全市场资金费结算缓存：键是（小时, 合约），值是这一小时内的结算之和。
// 推送提交与实时拉取只读缓存；没就绪的小时槽异步按需补取。启用后（计划里有数据集选了 funding_rate）
// 每个整点后约 2 分钟取一次、收盘前约 5 分钟再取一次覆盖。补采按合约直接查，不经缓存。
type FundingCache struct {
	source marketdata.FundingRateSource
	clock  FundingClock

	mu       sync.Mutex
	slots    map[int64]*fundingHour
	active   bool
	maxStep  time.Duration
	stopTick func() bool
}

type fundingHour struct {
	ready     bool
	rates     map[string]float64
	fetching  bool
	scheduled bool
	failures  int
	waiters   []*fundingWaiter
}

// fundingWaiter 等若干个小时槽全部就绪；remaining 归零时调用一次 notify。
type fundingWaiter struct {
	remaining int
	notify    func()
	canceled  bool
}

// NewFundingCache 构造缓存；clock 为空时用系统时钟。
func NewFundingCache(source marketdata.FundingRateSource, clock FundingClock) *FundingCache {
	if clock == nil {
		clock = systemFundingClock{}
	}
	return &FundingCache{source: source, clock: clock, slots: map[int64]*fundingHour{}}
}

// fundingHours 返回 [start, end) 覆盖的各小时槽起点（1d 是本根覆盖的 24 个，不是“最近 24 个”）。
func fundingHours(start, end time.Time) []time.Time {
	var hours []time.Time
	for hour := start.UTC().Truncate(fundingSlotSpan); hour.Before(end); hour = hour.Add(fundingSlotSpan) {
		hours = append(hours, hour)
	}
	return hours
}

// Sum 返回合约在 [start, end) 覆盖的各小时槽的结算之和；本区间没有结算时为 0。任何一个小时槽未就绪时返回 false。
func (c *FundingCache) Sum(symbol string, start, end time.Time) (float64, bool) {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0.0
	for _, hour := range fundingHours(start, end) {
		slot := c.slots[hour.Unix()]
		if slot == nil || !slot.ready {
			return 0, false
		}
		total += slot.rates[symbol]
	}
	return total, true
}

// Prepare 确保 [start, end) 覆盖的小时槽都在取数。全部已就绪时返回 true，不调用 notify；
// 否则在全部就绪后调用一次 notify（在缓存的取数协程里调用，必须很快返回）。返回的 cancel 撤销等待。
func (c *FundingCache) Prepare(start, end time.Time, notify func()) (ready bool, cancel func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	waiter := &fundingWaiter{notify: notify}
	for _, hour := range fundingHours(start, end) {
		slot := c.slot(hour)
		if slot.ready {
			continue
		}
		waiter.remaining++
		slot.waiters = append(slot.waiters, waiter)
		c.ensure(hour)
	}
	if waiter.remaining == 0 {
		return true, func() {}
	}
	return false, func() {
		c.mu.Lock()
		waiter.canceled = true
		c.mu.Unlock()
	}
}

// Await 阻塞到 [start, end) 覆盖的小时槽全部就绪，或 ctx 结束。
func (c *FundingCache) Await(ctx context.Context, start, end time.Time) error {
	done := make(chan struct{})
	ready, cancel := c.Prepare(start, end, func() { close(done) })
	if ready {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

// Query 直接查一个合约在 [start, end) 内的结算记录（补采用，按补采优先级排队），不经缓存。
func (c *FundingCache) Query(ctx context.Context, symbol string, start, end time.Time) ([]marketdata.FundingRate, error) {
	return c.source.FundingRates(ctx, symbol, start, end)
}

// Activate 启动整点取数；maxStep 是计划里选了 funding_rate 的最长一根（1h 或 1d）。
// 启动时，以及已启用后最长一根变长（例如原来只有 1h、又加了 1d）时，补取最长一根需要的小时槽。
func (c *FundingCache) Activate(maxStep time.Duration) {
	if maxStep < fundingSlotSpan {
		maxStep = fundingSlotSpan
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	grown := maxStep > c.maxStep
	c.maxStep = maxStep
	if c.active && !grown {
		return
	}
	now := c.clock.Now().UTC()
	// 上一根（可能还在截止前等待提交）与正在进行的这一根已经过去的小时；已就绪或正在取的小时槽不重复取。
	for hour := now.Truncate(maxStep).Add(-maxStep); !hour.Add(fundingPublishDelay).After(now); hour = hour.Add(fundingSlotSpan) {
		c.ensure(hour)
	}
	if !c.active {
		c.active = true
		c.scheduleTick()
	}
}

// Deactivate 停止整点取数；已取到的小时槽保留到被清理。
func (c *FundingCache) Deactivate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = false
	if c.stopTick != nil {
		c.stopTick()
		c.stopTick = nil
	}
}

// scheduleTick 安排下一次整点取数：每小时 H+2 分钟取 H 这一小时，H+55 分钟再取一次覆盖（调用方持有锁）。
func (c *FundingCache) scheduleTick() {
	now := c.clock.Now().UTC()
	hour := now.Truncate(fundingSlotSpan)
	var next time.Time
	refresh := false
	switch {
	case now.Before(hour.Add(fundingPublishDelay)):
		next = hour.Add(fundingPublishDelay)
	case now.Before(hour.Add(fundingSlotSpan - fundingRefreshLead)):
		next, refresh = hour.Add(fundingSlotSpan-fundingRefreshLead), true
	default:
		hour = hour.Add(fundingSlotSpan)
		next = hour.Add(fundingPublishDelay)
	}
	c.stopTick = c.clock.AfterFunc(next.Sub(now), func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.active {
			return
		}
		if refresh {
			c.refresh(hour)
		} else {
			c.ensure(hour)
		}
		c.prune()
		c.scheduleTick()
	})
}

// prune 清理早于“最长一根再加两小时”且没人等的小时槽（调用方持有锁）。
func (c *FundingCache) prune() {
	cutoff := c.clock.Now().UTC().Add(-c.maxStep - 2*fundingSlotSpan)
	for key, slot := range c.slots {
		if time.Unix(key, 0).Before(cutoff) && !slot.fetching && len(slot.liveWaiters()) == 0 {
			delete(c.slots, key)
		}
	}
}

func (c *FundingCache) slot(hour time.Time) *fundingHour {
	slot := c.slots[hour.Unix()]
	if slot == nil {
		slot = &fundingHour{}
		c.slots[hour.Unix()] = slot
	}
	return slot
}

func (s *fundingHour) liveWaiters() []*fundingWaiter {
	live := s.waiters[:0]
	for _, waiter := range s.waiters {
		if !waiter.canceled {
			live = append(live, waiter)
		}
	}
	s.waiters = live
	return live
}

// ensure 让一个没就绪的小时槽开始取数：已过可取数时刻就立即异步取，否则安排到那个时刻（调用方持有锁）。
func (c *FundingCache) ensure(hour time.Time) {
	slot := c.slot(hour)
	if slot.ready || slot.fetching || slot.scheduled {
		return
	}
	if wait := hour.Add(fundingPublishDelay).Sub(c.clock.Now()); wait > 0 {
		slot.scheduled = true
		c.clock.AfterFunc(wait, func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			slot.scheduled = false
			c.ensure(hour)
		})
		return
	}
	c.startFetch(hour, slot)
}

// refresh 重新取一个小时槽并覆盖（取失败保留原值）（调用方持有锁）。
func (c *FundingCache) refresh(hour time.Time) {
	slot := c.slot(hour)
	if slot.fetching {
		return
	}
	c.startFetch(hour, slot)
}

func (c *FundingCache) startFetch(hour time.Time, slot *fundingHour) {
	slot.fetching = true
	go c.fetch(hour, slot)
}

func (c *FundingCache) fetch(hour time.Time, slot *fundingHour) {
	ctx, cancel := context.WithTimeout(marketdata.WithRealtimeFunding(context.Background()), fundingFetchTimeout)
	rates, err := c.source.FundingRates(ctx, "", hour, hour.Add(fundingSlotSpan))
	cancel()
	c.mu.Lock()
	slot.fetching = false
	if err != nil {
		slot.failures++
		needed := len(slot.liveWaiters()) > 0 || (!slot.ready && c.active && !hour.Before(c.clock.Now().Add(-c.maxStep-fundingSlotSpan)))
		retry := min(fundingRetryBase<<min(slot.failures-1, 8), fundingRetryMax)
		log.Warnf("资金费率小时槽取数失败 hour=%s failures=%d retry=%t: %v", hour.UTC().Format(time.RFC3339), slot.failures, needed && !slot.ready, err)
		if needed && !slot.ready && !slot.scheduled {
			slot.scheduled = true
			c.clock.AfterFunc(retry, func() {
				c.mu.Lock()
				defer c.mu.Unlock()
				slot.scheduled = false
				if !slot.ready && !slot.fetching {
					c.startFetch(hour, slot)
				}
			})
		}
		c.mu.Unlock()
		return
	}
	sums := make(map[string]float64, len(rates))
	for symbol, value := range marketdata.SumFundingRates(rates, hour, hour.Add(fundingSlotSpan)) {
		sums[strings.ToUpper(symbol)] = value
	}
	slot.rates, slot.ready, slot.failures = sums, true, 0
	var notify []func()
	for _, waiter := range slot.liveWaiters() {
		waiter.remaining--
		if waiter.remaining == 0 {
			notify = append(notify, waiter.notify)
		}
	}
	slot.waiters = nil
	c.mu.Unlock()
	for _, f := range notify {
		f()
	}
}
