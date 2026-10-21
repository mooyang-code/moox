package stream

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/stretchr/testify/require"
)

type recordingCommitter struct {
	mu      sync.Mutex
	batches []marketfetch.KlineBatch
	fail    int
	block   chan struct{}
}

func (c *recordingCommitter) CommitPeriod(ctx context.Context, batch marketfetch.KlineBatch) (marketfetch.KlineCommitResult, error) {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.fail > 0 {
		c.fail--
		return marketfetch.KlineCommitResult{}, context.DeadlineExceeded
	}
	c.batches = append(c.batches, batch)
	return marketfetch.KlineCommitResult{Rows: len(batch.Bars)}, nil
}

func (c *recordingCommitter) calls() []marketfetch.KlineBatch {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]marketfetch.KlineBatch(nil), c.batches...)
}

type gateHarness struct {
	t         *testing.T
	clock     *FakeClock
	gate      *Gate
	committer *recordingCommitter
	stats     chan PeriodStat
}

var gateMarket = marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "swap", Provider: "binance", SourceID: "swap_http", MarketType: "swap"}

func testStreamPlan(frequency string, lateness int, copies ...marketfetch.PlanCopy) (*marketfetch.Plan, *marketfetch.PlanDataset) {
	plan := &marketfetch.Plan{V: 1, Executor: marketfetch.PlanExecutorStreaming, Market: gateMarket, Fetch: marketfetch.PlanFetch{Concurrency: 1},
		Datasets: []marketfetch.PlanDataset{{DatasetID: "bars_" + frequency, Frequency: frequency, LatenessS: lateness, Copies: copies}}}
	return plan, &plan.Datasets[0]
}

func copyOf(from time.Time, subjects ...string) marketfetch.PlanCopy {
	items := make([]marketfetch.PlanSubject, len(subjects))
	for i, subject := range subjects {
		items[i] = marketfetch.PlanSubject{ID: subject, Index: uint32(i)}
	}
	return marketfetch.PlanCopy{From: from.Unix(), Hash: "h", Count: uint32(len(subjects) + 2), Subjects: items}
}

func newGateHarness(t *testing.T, start time.Time, frequency marketdata.Frequency, plan *marketfetch.Plan, dataset *marketfetch.PlanDataset) *gateHarness {
	t.Helper()
	clock := NewFakeClock(start)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	queue := NewCommitQueue(clock)
	go queue.Run(ctx)
	committer := &recordingCommitter{}
	stats := make(chan PeriodStat, 16)
	gate, err := NewGate(GateConfig{SpaceID: "crypto", Market: gateMarket, DatasetID: dataset.DatasetID, Frequency: frequency, Committer: committer, Queue: queue, Clock: clock, Report: func(stat PeriodStat) { stats <- stat }})
	require.NoError(t, err)
	gate.Start(ctx)
	gate.SetPlan(plan, dataset)
	gate.Sync()
	return &gateHarness{t: t, clock: clock, gate: gate, committer: committer, stats: stats}
}

// advance 按 100 毫秒小步推进：闸门在自己的协程里建立定时器，每步之后等它处理完。
func (h *gateHarness) advance(d time.Duration) {
	for d > 0 {
		step := min(d, 100*time.Millisecond)
		h.clock.Advance(step)
		h.gate.Sync()
		d -= step
	}
}

func (h *gateHarness) final(symbol string, period time.Time, close float64) {
	h.gate.Offer(Event{Kind: EventKlineFinal, Symbol: symbol, Kline: marketdata.NormalizedKline{SubjectID: symbol, ProviderID: "binance", SourceID: "swap_http", Frequency: "1m", BarStart: period, BarEnd: period.Add(time.Minute), Close: close}})
	h.gate.Sync()
}

func (h *gateHarness) waitCalls(n int) []marketfetch.KlineBatch {
	h.t.Helper()
	require.Eventually(h.t, func() bool { return len(h.committer.calls()) >= n }, time.Second, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	h.gate.Sync()
	return h.committer.calls()
}

func (h *gateHarness) stat() PeriodStat {
	h.t.Helper()
	select {
	case stat := <-h.stats:
		return stat
	case <-time.After(time.Second):
		h.t.Fatal("no period stat")
		return PeriodStat{}
	}
}

var closeAt = time.Date(2026, 10, 21, 8, 1, 0, 0, time.UTC)

func TestGateCommitsAsSoonAsAllSubjectsArrive(t *testing.T) {
	period := closeAt.Add(-time.Minute)
	plan, dataset := testStreamPlan("1m", 5, copyOf(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT", "XRP-USDT"))
	h := newGateHarness(t, closeAt.Add(-10*time.Second), marketdata.FrequencyMinute, plan, dataset)
	h.advance(10 * time.Second) // 收盘：定时器建立这一期
	h.advance(100 * time.Millisecond)
	h.final("BTCUSDT", period, 1)
	h.final("ETHUSDT", period, 2)
	require.Empty(t, h.committer.calls(), "没到齐、也没到 D 时不提交")
	h.final("XRPUSDT", period, 3)
	calls := h.waitCalls(1)
	require.Len(t, calls[0].Bars, 3)
	require.Equal(t, "ETH-USDT", calls[0].Bars[1].SubjectID, "按份里的标的回填 subject_id")
	h.advance(10 * time.Second)
	stat := h.stat()
	require.Equal(t, 3, stat.Arrived)
	require.Zero(t, stat.Missing)
	require.True(t, stat.Committed)
}

// 没到齐时在 D = 收盘 + 截止 − 预留（1m 为 4 秒）提交已到的；提交后截止前到的增量再提交；截止后到的丢弃。
func TestGateCommitsWhatArrivedAtTheLatestStartAndThenLateFinals(t *testing.T) {
	period := closeAt.Add(-time.Minute)
	plan, dataset := testStreamPlan("1m", 5, copyOf(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT", "XRP-USDT"))
	h := newGateHarness(t, closeAt.Add(-time.Second), marketdata.FrequencyMinute, plan, dataset)
	h.advance(time.Second)
	h.final("BTCUSDT", period, 1)
	h.advance(3900 * time.Millisecond)
	require.Empty(t, h.committer.calls())
	h.advance(100 * time.Millisecond) // D
	calls := h.waitCalls(1)
	require.Len(t, calls[0].Bars, 1)
	h.advance(500 * time.Millisecond)
	h.final("ETHUSDT", period, 2) // 截止前迟到：增量提交
	calls = h.waitCalls(2)
	require.Len(t, calls[1].Bars, 1)
	require.Equal(t, "ETH-USDT", calls[1].Bars[0].SubjectID)
	h.advance(time.Second) // 过截止
	h.final("XRPUSDT", period, 3)
	h.advance(2 * time.Second)
	stat := h.stat()
	require.Equal(t, 1, stat.LateAfterCommit)
	require.Equal(t, []string{"XRP-USDT"}, stat.MissingSubjects)
	require.Len(t, h.committer.calls(), 2, "截止之后到的不再提交")
}

func TestGateZeroArrivalDoesNotCommitAndDeduplicates(t *testing.T) {
	period := closeAt.Add(-time.Minute)
	plan, dataset := testStreamPlan("1m", 5, copyOf(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT"))
	h := newGateHarness(t, closeAt.Add(-time.Second), marketdata.FrequencyMinute, plan, dataset)
	h.advance(7 * time.Second)
	stat := h.stat()
	require.Zero(t, stat.Arrived)
	require.Equal(t, 2, stat.Missing)
	require.Empty(t, h.committer.calls(), "零到达不提交，周期由补采补齐")

	// 下一期：两条连接（轮换）各送一次，同值去重、不同值记冲突；一个不在期望里的标的丢弃计数。
	next := closeAt
	h.advance(54 * time.Second)
	h.final("BTCUSDT", next, 1)
	h.final("BTCUSDT", next, 1)
	h.final("BTCUSDT", next, 9)
	h.final("DOGEUSDT", next, 1)
	h.final("ETHUSDT", next, 2)
	calls := h.waitCalls(1)
	require.Len(t, calls[0].Bars, 2)
	require.Equal(t, 1.0, calls[0].Bars[0].Close, "先到的定稿为准")
	h.advance(10 * time.Second)
	stat = h.stat()
	require.Equal(t, 2, stat.Duplicates)
	require.Equal(t, 1, stat.Conflicts)
	require.Equal(t, 1, stat.Unplanned)
}

// 计划切换：每一期按自己生效的那份判断期望标的（P 之前的新标的不进闸门）。
func TestGateUsesTheCopyEffectiveForEachPeriod(t *testing.T) {
	p := closeAt
	plan, dataset := testStreamPlan("1m", 5, copyOf(p.Add(-time.Hour), "BTC-USDT", "ETH-USDT"), copyOf(p, "BTC-USDT", "SOL-USDT"))
	h := newGateHarness(t, p.Add(-time.Second), marketdata.FrequencyMinute, plan, dataset)
	h.advance(time.Second)
	before := p.Add(-time.Minute)
	h.final("SOLUSDT", before, 1)
	h.final("BTCUSDT", before, 1)
	h.final("ETHUSDT", before, 1)
	calls := h.waitCalls(1)
	require.Len(t, calls[0].Bars, 2)
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, []string{calls[0].Bars[0].SubjectID, calls[0].Bars[1].SubjectID})
	h.advance(time.Minute)
	h.final("ETHUSDT", p, 1)
	h.final("BTCUSDT", p, 1)
	h.final("SOLUSDT", p, 1)
	calls = h.waitCalls(2)
	require.Equal(t, []string{"BTC-USDT", "SOL-USDT"}, []string{calls[1].Bars[0].SubjectID, calls[1].Bars[1].SubjectID})
	require.Equal(t, p.Unix(), calls[1].Dataset.Copies[0].From)
}

// 合约按 pair 订阅、按 symbol 写入：事件里的符号是 pair。
func TestGateMatchesFinalsByPair(t *testing.T) {
	period := closeAt.Add(-time.Minute)
	plan, dataset := testStreamPlan("1m", 5, copyOf(period.Add(-time.Hour), "BTC-USDT"))
	plan.Pairs = map[string]string{"BTC-USDT": "BTCUSDT_PAIR"}
	h := newGateHarness(t, closeAt.Add(-time.Second), marketdata.FrequencyMinute, plan, dataset)
	h.advance(time.Second)
	h.final("BTCUSDT", period, 1)
	h.final("BTCUSDT_PAIR", period, 1)
	calls := h.waitCalls(1)
	require.Equal(t, "BTCUSDT", calls[0].Symbols("BTC-USDT"))
}

// 提交失败按 300 毫秒重试，直到截止前 200 毫秒。
func TestCommitQueueRetriesUntilShortlyBeforeTheDeadline(t *testing.T) {
	clock := NewFakeClock(closeAt)
	queue := NewCommitQueue(clock)
	committer := &recordingCommitter{fail: 100}
	done := make(chan commitOutcome, 1)
	queue.submit(&commitJob{committer: committer, deadline: closeAt.Add(time.Second), done: func(o commitOutcome) { done <- o }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go queue.Run(ctx)
	var outcome commitOutcome
	require.Eventually(t, func() bool {
		clock.Advance(100 * time.Millisecond)
		select {
		case outcome = <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second, time.Millisecond)
	require.Error(t, outcome.err)
	require.Equal(t, 3, outcome.attempts, "0、300、600 毫秒各一次，900 毫秒已在截止前 200 毫秒之内")
}

// 整点 1m 与 1h 同时排队时 1m 先提交（截止早）。
func TestCommitQueueOrdersByDeadlineThenFrequency(t *testing.T) {
	clock := NewFakeClock(closeAt)
	queue := NewCommitQueue(clock)
	var order []string
	var mu sync.Mutex
	record := func(name string) func(commitOutcome) {
		return func(commitOutcome) { mu.Lock(); order = append(order, name); mu.Unlock() }
	}
	committer := &recordingCommitter{}
	queue.submit(&commitJob{committer: committer, deadline: closeAt.Add(8 * time.Second), step: time.Hour, done: record("1h-swap")})
	queue.submit(&commitJob{committer: committer, deadline: closeAt.Add(5 * time.Second), step: time.Minute, done: record("1m-spot")})
	queue.submit(&commitJob{committer: committer, deadline: closeAt.Add(5 * time.Second), step: time.Hour, done: record("tie-1h")})
	queue.submit(&commitJob{committer: committer, deadline: closeAt.Add(5 * time.Second), step: time.Minute, done: record("1m-swap")})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go queue.Run(ctx)
	require.Eventually(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(order) == 4 }, time.Second, time.Millisecond)
	require.Equal(t, "1h-swap", order[3])
	require.Equal(t, "tie-1h", order[2])
}

// 收到新计划后，P 起不再属于本执行端的标的（这一份为空）不再提交。
func TestGateStopsCommittingFromTheEffectivePeriodOfAnEmptyCopy(t *testing.T) {
	p := closeAt
	plan, dataset := testStreamPlan("1m", 5, copyOf(p.Add(-time.Hour), "BTC-USDT"))
	h := newGateHarness(t, p.Add(-time.Second), marketdata.FrequencyMinute, plan, dataset)
	next, nextDataset := testStreamPlan("1m", 5, copyOf(p.Add(-time.Hour), "BTC-USDT"), marketfetch.PlanCopy{From: p.Unix(), Hash: "h", Count: 3})
	h.gate.SetPlan(next, nextDataset)
	h.advance(time.Second)
	h.final("BTCUSDT", p.Add(-time.Minute), 1)
	h.waitCalls(1)
	h.advance(time.Minute)
	h.final("BTCUSDT", p, 1)
	h.advance(10 * time.Second)
	require.Len(t, h.committer.calls(), 1, "P 起没有期望标的，不建立周期也不提交")
}

type namedCommitter struct {
	mu    sync.Mutex
	fail  map[string]bool
	order []string
}

func (c *namedCommitter) CommitPeriod(_ context.Context, batch marketfetch.KlineBatch) (marketfetch.KlineCommitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order = append(c.order, batch.BatchID)
	if c.fail[batch.BatchID] {
		return marketfetch.KlineCommitResult{}, context.DeadlineExceeded
	}
	return marketfetch.KlineCommitResult{}, nil
}

// 整点 1h 先收齐、提交失败重试期间 1m 到达：1m 在 1h 的下一次重试之前提交，不被长截止任务的重试占住。
func TestCommitQueueRetryDoesNotHoldBackAnEarlierDeadline(t *testing.T) {
	clock := NewFakeClock(closeAt)
	queue := NewCommitQueue(clock)
	committer := &namedCommitter{fail: map[string]bool{"1h": true}}
	done := make(chan string, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go queue.Run(ctx)
	queue.submit(&commitJob{committer: committer, batch: marketfetch.KlineBatch{BatchID: "1h"}, deadline: closeAt.Add(8 * time.Second), step: time.Hour, done: func(commitOutcome) { done <- "1h" }})
	require.Eventually(t, func() bool { committer.mu.Lock(); defer committer.mu.Unlock(); return len(committer.order) == 1 }, time.Second, time.Millisecond)
	queue.submit(&commitJob{committer: committer, batch: marketfetch.KlineBatch{BatchID: "1m"}, deadline: closeAt.Add(5 * time.Second), step: time.Minute, done: func(commitOutcome) { done <- "1m" }})
	require.Equal(t, "1m", <-done)
	committer.mu.Lock()
	require.Equal(t, []string{"1h", "1m"}, committer.order)
	committer.mu.Unlock()
	clock.Advance(commitRetryInterval)
	require.Eventually(t, func() bool { committer.mu.Lock(); defer committer.mu.Unlock(); return len(committer.order) == 3 }, time.Second, time.Millisecond)
}

// 提交预留随心跳更新：已有闸门之后建立的周期按新值提前提交（预留 3 秒：D = 收盘 + 5 − 3）。
func TestGateUsesAnUpdatedCommitReserveForLaterPeriods(t *testing.T) {
	period := closeAt.Add(-time.Minute)
	plan, dataset := testStreamPlan("1m", 5, copyOf(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT"))
	h := newGateHarness(t, closeAt.Add(-time.Second), marketdata.FrequencyMinute, plan, dataset)
	h.gate.SetCommitReserve(3 * time.Second)
	h.advance(time.Second)
	h.final("BTCUSDT", period, 1)
	h.advance(1900 * time.Millisecond)
	require.Empty(t, h.committer.calls())
	h.advance(100 * time.Millisecond)
	require.Len(t, h.waitCalls(1)[0].Bars, 1)
}
