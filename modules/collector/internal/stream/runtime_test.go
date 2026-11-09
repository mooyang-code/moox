package stream_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/mooyang-code/moox/modules/collector/internal/stream"
	"github.com/stretchr/testify/require"
)

type exchangeProvider struct{ url string }

func (p exchangeProvider) StreamSource(market string) (stream.Source, error) {
	return binance.NewStreamSource(market, binance.StreamConfig{SpotEndpoints: []string{p.url}, SwapEndpoints: []string{p.url}, MessagesPerSecond: 50})
}

type memoryCommitter struct {
	mu      sync.Mutex
	batches []marketfetch.KlineBatch
}

func (c *memoryCommitter) CommitPeriod(_ context.Context, batch marketfetch.KlineBatch) (marketfetch.KlineCommitResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.batches = append(c.batches, batch)
	return marketfetch.KlineCommitResult{Rows: len(batch.Bars)}, nil
}

func (c *memoryCommitter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.batches)
}

func spotPlan(from time.Time, subjects ...string) *marketfetch.Plan {
	items := make([]marketfetch.PlanSubject, len(subjects))
	for i, subject := range subjects {
		items[i] = marketfetch.PlanSubject{ID: subject, Index: uint32(i)}
	}
	return &marketfetch.Plan{V: 1, Executor: marketfetch.PlanExecutorStreaming, Fetch: marketfetch.PlanFetch{Concurrency: 1},
		Market:   marketfetch.PlanMarket{MarketID: "crypto", InstrumentType: "spot", Provider: "binance", SourceID: "spot_http", MarketType: "spot"},
		Datasets: []marketfetch.PlanDataset{{DatasetID: "bars_1m", Frequency: "1m", LatenessS: 5, Copies: []marketfetch.PlanCopy{{From: from.Unix(), Hash: "h", Count: uint32(len(subjects)), Subjects: items}}}}}
}

// 运行时按计划订阅、把收盘定稿交给闸门，到齐即提交。
func TestRuntimeSubscribesAndCommitsAPlannedPeriod(t *testing.T) {
	exchange := newFakeExchange(t)
	committer := &memoryCommitter{}
	stats := make(chan stream.PeriodStat, 8)
	period := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	clock := stream.NewFakeClock(period.Add(time.Minute + 150*time.Millisecond))
	runtime := stream.NewRuntime(stream.RuntimeConfig{
		Clock:   clock,
		SpaceID: "crypto", Providers: map[string]stream.Provider{"binance": exchangeProvider{url: exchange.url()}},
		NewCommitter: func(marketfetch.PlanMarket) (stream.PeriodCommitter, error) { return committer, nil },
		Manager:      stream.ManagerOptions{CheckInterval: 20 * time.Millisecond},
		Report:       func(stat stream.PeriodStat) { stats <- stat },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runtime.Start(ctx)
	require.NoError(t, runtime.Apply([]stream.PlanItem{{Name: "stream-crypto-spot", Executor: marketfetch.PlanExecutorStreaming, Market: "spot", Plan: spotPlan(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT")}}))
	require.Eventually(t, runtime.Ready, 3*time.Second, 10*time.Millisecond)
	status := runtime.Status()
	require.Len(t, status, 1)
	require.Equal(t, 2, status[0].Conns[0].Streams)

	exchange.push("btcusdt@kline_1m", "BTCUSDT", period.UnixMilli())
	exchange.push("ethusdt@kline_1m", "ETHUSDT", period.UnixMilli())
	require.Eventually(t, func() bool { return committer.count() == 1 }, 3*time.Second, 10*time.Millisecond)
	require.Len(t, committer.batches[0].Bars, 2)
	require.Equal(t, "BTCUSDT", committer.batches[0].Symbols("BTC-USDT"))

	// 计划去掉这个市场：订阅在最后负责的周期过去后关闭（这里没有后续份，下一次刷新即关闭）。
	clock.Advance(10 * time.Second)
	select {
	case stat := <-stats:
		require.Equal(t, 2, stat.Arrived)
		require.True(t, stat.Committed)
		require.Equal(t, 150*time.Millisecond, stat.CommitDone)
	case <-time.After(time.Second):
		t.Fatal("no period stat")
	}
	require.NoError(t, runtime.Apply(nil))
	require.Eventually(t, func() bool { return len(runtime.Status()[0].Conns) == 0 }, 3*time.Second, 10*time.Millisecond)
}

type countingFundingSource struct {
	mu    sync.Mutex
	calls int
}

func (s *countingFundingSource) FundingRates(context.Context, string, time.Time, time.Time) ([]marketdata.FundingRate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return nil, nil
}

func (s *countingFundingSource) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type streamFundingClock struct{ clock *stream.FakeClock }

func (c streamFundingClock) Now() time.Time { return c.clock.Now() }
func (c streamFundingClock) AfterFunc(d time.Duration, f func()) func() bool {
	return c.clock.AfterFunc(d, f).Stop
}

// 资金费率整点取数默认关闭：只有计划项（推送或本地拉取）里有数据集选了 funding_rate 时才启用，去掉后停用。
func TestRuntimeActivatesFundingOnlyWhenPlanSelectsIt(t *testing.T) {
	clock := stream.NewFakeClock(time.Date(2026, 10, 21, 8, 30, 0, 0, time.UTC))
	source := &countingFundingSource{}
	cache := marketfetch.NewFundingCache(source, streamFundingClock{clock})
	runtime := stream.NewRuntime(stream.RuntimeConfig{
		Clock: clock, SpaceID: "crypto", Providers: map[string]stream.Provider{},
		NewCommitter: func(marketfetch.PlanMarket) (stream.PeriodCommitter, error) { return &memoryCommitter{}, nil },
		FundingFor:   func(marketfetch.PlanMarket) *marketfetch.FundingCache { return cache },
	})
	plan := spotPlan(clock.Now().Add(-time.Hour), "BTC-USDT")
	plan.Executor = marketfetch.PlanExecutorLocal
	items := []stream.PlanItem{{Name: "local-crypto-swap", Executor: marketfetch.PlanExecutorLocal, Market: "swap", Plan: plan}}
	require.NoError(t, runtime.Apply(items))
	clock.Advance(2 * time.Hour)
	require.Zero(t, source.count(), "不选资金费率时不取数")

	plan.Datasets[0].Frequency = "1h"
	plan.Datasets[0].OutputFields = []string{"close", "funding_rate"}
	require.NoError(t, runtime.Apply(items))
	// Activate(1h) 并行取上一根与当前这一根两个小时槽，两个都取完再停用，否则停用后的计数会变。
	require.Eventually(t, func() bool { return source.count() == 2 }, time.Second, time.Millisecond, "本地拉取的计划项也要启用")

	require.NoError(t, runtime.Apply(nil))
	settled := source.count()
	clock.Advance(3 * time.Hour)
	time.Sleep(20 * time.Millisecond)
	require.Equal(t, settled, source.count(), "计划里不再选资金费率后停用")
}
