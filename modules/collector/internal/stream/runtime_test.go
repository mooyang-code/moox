package stream_test

import (
	"context"
	"sync"
	"testing"
	"time"

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
	return &marketfetch.Plan{V: 1, Executor: marketfetch.PlanExecutorStream, Fetch: marketfetch.PlanFetch{Concurrency: 1},
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
	require.NoError(t, runtime.Apply([]stream.PlanItem{{Name: "stream-crypto-spot", Executor: marketfetch.PlanExecutorStream, Market: "spot", Plan: spotPlan(period.Add(-time.Hour), "BTC-USDT", "ETH-USDT")}}))
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
