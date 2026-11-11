package stream

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

// fundingClock 让资金费缓存与闸门共用同一个假时钟。
type fundingClock struct{ clock *FakeClock }

func (c fundingClock) Now() time.Time { return c.clock.Now() }
func (c fundingClock) AfterFunc(d time.Duration, f func()) func() bool {
	return c.clock.AfterFunc(d, f).Stop
}

// gateFundingSource 返回固定记录；release 非空时每次查询先等它放行；fail 为真时一直失败。
type gateFundingSource struct {
	mu      sync.Mutex
	rates   []marketdata.FundingRate
	release chan struct{}
	fail    bool
	calls   int
}

func (s *gateFundingSource) FundingRates(ctx context.Context, symbol string, start, end time.Time) ([]marketdata.FundingRate, error) {
	s.mu.Lock()
	s.calls++
	release, fail := s.release, s.fail
	s.mu.Unlock()
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if fail {
		return nil, errors.New("上游不可用")
	}
	var out []marketdata.FundingRate
	for _, rate := range s.rates {
		if !rate.FundingTime.Before(start) && rate.FundingTime.Before(end) {
			out = append(out, rate)
		}
	}
	return out, nil
}

var hourClose = time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)

func newFundingGateHarness(t *testing.T, source *gateFundingSource) (*gateHarness, *marketfetch.FundingCache, time.Time) {
	t.Helper()
	return newFundingGateHarnessAt(t, source, marketdata.FrequencyHour, hourClose, true)
}

// newFundingGateHarnessAt 建一个选了资金费率的闸门，时钟停在收盘时刻；withCache 为假表示行情源没有资金费率能力。
func newFundingGateHarnessAt(t *testing.T, source *gateFundingSource, frequency marketdata.Frequency, closeTime time.Time, withCache bool) (*gateHarness, *marketfetch.FundingCache, time.Time) {
	t.Helper()
	period := closeTime.Add(-frequency.Duration())
	plan, dataset := testStreamPlan(string(frequency), 15, copyOf(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT"))
	dataset.OutputFields = []string{"close", domain.FieldFundingRate}
	clock := NewFakeClock(closeTime.Add(-10 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue := NewCommitQueue(clock)
	go queue.Run(ctx)
	var cache *marketfetch.FundingCache
	if withCache {
		cache = marketfetch.NewFundingCache(source, fundingClock{clock})
	}
	committer := &recordingCommitter{}
	stats := make(chan PeriodStat, 16)
	gate, err := NewGate(GateConfig{SpaceID: "crypto", Market: gateMarket, DatasetID: dataset.DatasetID, Frequency: frequency, Committer: committer, Queue: queue, Clock: clock, Funding: cache, Report: func(stat PeriodStat) { stats <- stat }})
	require.NoError(t, err)
	gate.Start(ctx)
	gate.SetPlan(plan, dataset)
	gate.Sync()
	h := &gateHarness{t: t, clock: clock, gate: gate, committer: committer, stats: stats}
	h.advance(10 * time.Second) // 收盘
	return h, cache, period
}

func fundingOf(t *testing.T, bar marketdata.NormalizedKline) float64 {
	t.Helper()
	value, ok := bar.Extra[domain.FieldFundingRate]
	require.True(t, ok, "提交的每根都带本根资金费")
	return value.Double
}

// 缓存已就绪：到齐即提交，按交易所合约代码取本根的资金费（没有结算的为 0）。
func TestGateFundingReadyCommitsImmediately(t *testing.T) {
	source := &gateFundingSource{}
	h, cache, period := newFundingGateHarness(t, source)
	source.rates = []marketdata.FundingRate{{ProviderSymbol: "BTCUSDT", FundingTime: period.Add(time.Millisecond), Rate: 0.0001}}
	require.NoError(t, cache.Await(context.Background(), period, period.Add(time.Hour)))
	h.final("BTCUSDT", period, 1)
	h.final("ETHUSDT", period, 2)
	calls := h.waitCalls(1)
	require.Len(t, calls[0].Bars, 2)
	require.Equal(t, 0.0001, fundingOf(t, calls[0].Bars[0]))
	require.Equal(t, 0.0, fundingOf(t, calls[0].Bars[1]))
}

// 缓存晚到：先挂起（闸门照常处理消息、不阻塞），就绪后回到闸门协程再提交。
func TestGateFundingLateCacheDefersThenCommits(t *testing.T) {
	source := &gateFundingSource{release: make(chan struct{}), rates: []marketdata.FundingRate{{ProviderSymbol: "ETHUSDT", FundingTime: hourClose.Add(-time.Hour), Rate: -0.0002}}}
	h, _, period := newFundingGateHarness(t, source)
	h.final("BTCUSDT", period, 1)
	h.final("ETHUSDT", period, 2)
	h.advance(2 * time.Second)
	require.Empty(t, h.committer.calls(), "缓存没就绪时不提交")
	close(source.release)
	calls := h.waitCalls(1)
	require.Len(t, calls[0].Bars, 2)
	require.Equal(t, -0.0002, fundingOf(t, calls[0].Bars[1]))
	h.advance(20 * time.Second)
	stat := h.stat()
	require.True(t, stat.Committed)
	require.Zero(t, stat.Missing)
}

// 到最晚提交时刻仍没有资金费：这一期不提交，交给缺口补采；之后到的定稿也不再提交。
func TestGateFundingMissingAtDueSkipsPeriod(t *testing.T) {
	source := &gateFundingSource{fail: true}
	h, _, period := newFundingGateHarness(t, source)
	h.final("BTCUSDT", period, 1)
	h.advance(15 * time.Second) // 过 D（收盘 + 15 秒 − 1 秒预留）
	h.final("ETHUSDT", period, 2)
	h.advance(5 * time.Second)
	require.Empty(t, h.committer.calls())
	stat := h.stat()
	require.False(t, stat.Committed)
	require.Equal(t, 2, stat.Missing)
	require.Contains(t, stat.Err, "资金费率")
}

// 不选资金费率的数据集不读缓存，行为与原来一致。
func TestGateWithoutFundingFieldIgnoresCache(t *testing.T) {
	source := &gateFundingSource{fail: true}
	period := hourClose.Add(-time.Hour)
	plan, dataset := testStreamPlan("1h", 15, copyOf(period.Add(-time.Hour), "BTC-USDT"))
	clock := NewFakeClock(hourClose.Add(-10 * time.Second))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue := NewCommitQueue(clock)
	go queue.Run(ctx)
	committer := &recordingCommitter{}
	gate, err := NewGate(GateConfig{SpaceID: "crypto", Market: gateMarket, DatasetID: dataset.DatasetID, Frequency: marketdata.FrequencyHour, Committer: committer, Queue: queue, Clock: clock,
		Funding: marketfetch.NewFundingCache(source, fundingClock{clock}), Report: func(PeriodStat) {}})
	require.NoError(t, err)
	gate.Start(ctx)
	gate.SetPlan(plan, dataset)
	gate.Sync()
	h := &gateHarness{t: t, clock: clock, gate: gate, committer: committer}
	h.advance(10 * time.Second)
	h.final("BTCUSDT", period, 1)
	calls := h.waitCalls(1)
	require.Empty(t, calls[0].Bars[0].Extra)
	require.Zero(t, source.calls)
}

// 1d：取本根覆盖的 24 个小时槽求和（下一根开盘时刻的结算不算），24 个都就绪才提交。
func TestGateFundingDailySumsTwentyFourSlots(t *testing.T) {
	dayClose := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	day := dayClose.Add(-24 * time.Hour)
	source := &gateFundingSource{rates: []marketdata.FundingRate{
		{ProviderSymbol: "BTCUSDT", FundingTime: day, Rate: 0.0001},
		{ProviderSymbol: "BTCUSDT", FundingTime: day.Add(8*time.Hour + time.Millisecond), Rate: 0.0002},
		{ProviderSymbol: "BTCUSDT", FundingTime: day.Add(16 * time.Hour), Rate: -0.0001},
		{ProviderSymbol: "BTCUSDT", FundingTime: dayClose, Rate: 0.0009},
	}}
	h, _, period := newFundingGateHarnessAt(t, source, marketdata.FrequencyDay, dayClose, true)
	h.final("BTCUSDT", period, 1)
	h.final("ETHUSDT", period, 2)
	calls := h.waitCalls(1)
	require.InDelta(t, 0.0002, fundingOf(t, calls[0].Bars[0]), 1e-12)
	require.Equal(t, 0.0, fundingOf(t, calls[0].Bars[1]))
	source.mu.Lock()
	defer source.mu.Unlock()
	require.Equal(t, 24, source.calls, "按需补取本根的 24 个小时槽")
}

// 行情源没有资金费率能力：到最晚提交时刻放弃这一期，不提交。
func TestGateFundingWithoutCapabilityGivesUp(t *testing.T) {
	h, _, period := newFundingGateHarnessAt(t, &gateFundingSource{}, marketdata.FrequencyHour, hourClose, false)
	h.final("BTCUSDT", period, 1)
	h.final("ETHUSDT", period, 2)
	h.advance(2 * time.Second)
	require.Empty(t, h.committer.calls(), "D 之前挂起")
	h.advance(20 * time.Second)
	require.Empty(t, h.committer.calls())
	stat := h.stat()
	require.False(t, stat.Committed)
	require.Equal(t, 2, stat.Missing)
	require.Contains(t, stat.Err, "不具备")
}
