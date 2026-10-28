package marketfetch

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

type capturingPeriodStorage struct {
	noopStorage
	expectations []*storagepb.DatasetPeriodExpectation
	rows         [][]*storagepb.TimeSeriesBatchRow
	upserts      [][]*storagepb.RowFieldUpsert
}

func (s *capturingPeriodStorage) CommitTimeSeriesBatch(_ context.Context, expectation *storagepb.DatasetPeriodExpectation, rows []*storagepb.TimeSeriesBatchRow) error {
	s.expectations = append(s.expectations, expectation)
	s.rows = append(s.rows, rows)
	return nil
}

func (s *capturingPeriodStorage) UpsertFields(_ context.Context, rows []*storagepb.RowFieldUpsert) error {
	s.upserts = append(s.upserts, rows)
	return nil
}

func swapTestPlan(period time.Time) *Plan {
	return &Plan{V: 1,
		Market: PlanMarket{MarketID: "crypto", InstrumentType: "swap", Provider: "binance", SourceID: "swap_http", MarketType: "swap"},
		Fetch:  PlanFetch{Concurrency: 4},
		Datasets: []PlanDataset{{DatasetID: "bars_swap_1m", Frequency: "1m", LatenessS: 5, OutputFields: []string{"open", "close", "volume", "quote_volume", "trade_num", "provider_symbol"},
			Copies: []PlanCopy{{From: period.Add(-time.Hour).Unix(), Hash: "h", Count: 3, Subjects: []PlanSubject{{ID: "BTC-USDT", Index: 0}, {ID: "ETH-USDT", Index: 1}, {ID: "XRP-USDT", Index: 2}}}}}},
		SymbolExceptions: map[string]string{"XRP-USDT": "XRPUSDT"},
	}
}

func testSwapBar(subject, symbol string, period time.Time, close float64) marketdata.NormalizedKline {
	return marketdata.NormalizedKline{SubjectID: subject, ProviderID: "binance", SourceID: "swap_http", ProviderSymbol: symbol, Frequency: "1m",
		BarStart: period, BarEnd: period.Add(time.Minute), Open: 1, High: close + 1, Low: 0.5, Close: close, VolumeShares: 10, AmountCNY: 100, TradeCount: 7, ProviderTimestamp: period, FetchedAt: period.Add(time.Minute), RequestID: "r"}
}

// 同一组 K 线经 SCF 的 Execute 与经 KlineCommitter.CommitPeriod 写出的周期提交逐字节相同。
func TestKlineCommitterWritesTheSameRowsAsTheSCFPipeline(t *testing.T) {
	period := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	plan := swapTestPlan(period)
	bars := map[string]marketdata.NormalizedKline{
		"BTC-USDT": testSwapBar("BTC-USDT", "BTCUSDT", period, 101),
		"ETH-USDT": testSwapBar("ETH-USDT", "ETHUSDT", period, 11),
		"XRP-USDT": testSwapBar("XRP-USDT", "XRPUSDT", period, 0.5),
	}
	registry := marketdata.NewRegistry()
	require.NoError(t, registry.Register(pipelineProvider{id: "binance", sourceID: "swap_http", rowsFor: func(req marketdata.KlineRequest) []marketdata.NormalizedKline {
		return []marketdata.NormalizedKline{bars[req.SubjectID]}
	}}))
	router, err := marketdata.NewRouter(registry, 2, pipelineClock{period.Add(10 * time.Second)}, nil)
	require.NoError(t, err)
	scf := &capturingPeriodStorage{}
	pipeline := &KlinePipeline{Router: router, Storage: scf, CandidateChain: []string{"binance"}, SpaceID: "crypto", MarketID: "crypto", InstrumentType: marketdata.InstrumentSwap, SourceID: "swap_http"}
	requests, err := plan.BuildRequests("crypto", "fn", "req", period.Add(time.Minute), nil)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	pipeline.DatasetID = requests[0].Request.DatasetID
	payload, err := pipeline.Execute(context.Background(), requests[0].Request)
	require.NoError(t, err)
	require.Equal(t, "succeeded", payload.GetStatus(), "%v", payload.GetItems())

	stream := &capturingPeriodStorage{}
	committer := NewKlineCommitter(stream, plan.Market)
	streamBars := []marketdata.NormalizedKline{bars["XRP-USDT"], bars["BTC-USDT"], bars["ETH-USDT"]}
	for i := range streamBars {
		streamBars[i].ProviderSymbol = "" // 推送侧按计划的映射填 provider_symbol
	}
	result, err := committer.CommitPeriod(context.Background(), KlineBatch{SpaceID: "crypto", BatchID: "stream|swap|1m", Market: plan.Market, Dataset: plan.Datasets[0], Period: period, Symbols: plan.ExchangeSymbol, Bars: streamBars, Origin: KlineOriginStreaming})
	require.NoError(t, err)
	require.Equal(t, 3, result.Rows)
	require.Empty(t, result.Missing)

	require.Len(t, scf.rows, 1)
	require.Len(t, stream.rows, 1)
	require.True(t, proto.Equal(scf.expectations[0], stream.expectations[0]), "周期期望相同")
	require.Len(t, stream.rows[0], len(scf.rows[0]))
	for i := range scf.rows[0] {
		left, _ := proto.MarshalOptions{Deterministic: true}.Marshal(scf.rows[0][i])
		right, _ := proto.MarshalOptions{Deterministic: true}.Marshal(stream.rows[0][i])
		require.Equal(t, left, right, "row %d", i)
	}
}

// 推送侧：不在本期期望标的里的 K 线丢弃计数，缺的标的列出；没有 K 线就不提交。
func TestKlineCommitterReportsMissingAndUnplanned(t *testing.T) {
	period := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	plan := swapTestPlan(period)
	storage := &capturingPeriodStorage{}
	committer := NewKlineCommitter(storage, plan.Market)
	batch := KlineBatch{SpaceID: "crypto", BatchID: "b", Market: plan.Market, Dataset: plan.Datasets[0], Period: period, Symbols: plan.ExchangeSymbol,
		Bars: []marketdata.NormalizedKline{testSwapBar("BTC-USDT", "", period, 1), testSwapBar("DOGE-USDT", "", period, 1), testSwapBar("ETH-USDT", "", period.Add(time.Minute), 1)}}
	result, err := committer.CommitPeriod(context.Background(), batch)
	require.NoError(t, err)
	require.Equal(t, 1, result.Rows)
	require.Equal(t, 2, result.Unplanned)
	require.Equal(t, []string{"ETH-USDT", "XRP-USDT"}, result.Missing)
	require.Len(t, storage.rows, 1)
	batch.Bars = nil
	_, err = committer.CommitPeriod(context.Background(), batch)
	require.NoError(t, err)
	require.Len(t, storage.rows, 1, "零到达不提交")

	require.NoError(t, committer.Upsert(context.Background(), KlineBatch{SpaceID: "crypto", BatchID: "bf", Market: plan.Market, Dataset: plan.Datasets[0], Symbols: plan.ExchangeSymbol, Bars: []marketdata.NormalizedKline{testSwapBar("BTC-USDT", "", period, 1)}}))
	require.Len(t, storage.upserts, 1)
}
