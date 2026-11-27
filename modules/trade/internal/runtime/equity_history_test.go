package runtime

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

func TestEquityHistoryDownsamplerRunAll(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	require.NoError(t, st.Transaction(ctx, func(tx *store.Tx) error {
		if err := tx.CreateTradingAccount(store.TradingAccountRecord{
			SpaceID: "s1", TradingAccountID: "acc-1", Name: "acc-1",
			Exchange: "binance", Markets: []string{"spot"}, ExecutionMode: "PAPER",
			SettlementAsset: "USDT", Status: "ENABLED",
		}); err != nil {
			return err
		}
		// 10 天前（7~30 天档位，15 分钟粒度）同一 15 分钟桶内写 3 个分钟点：应被降采样为保留该 15 分钟桶最后一个点。
		bucketBase := now.Add(-10 * 24 * time.Hour).Truncate(15 * time.Minute)
		for idx := range 3 {
			ts := bucketBase.Add(time.Duration(idx) * time.Minute).UnixMilli()
			if err := tx.UpsertAccountEquityPoint(store.EquityPointRecord{
				SpaceID: "s1", TradingAccountID: "acc-1", BucketTime: ts,
				Equity: "1000", AvailableFunds: "1000", UsedMargin: "0", SourceTime: ts,
			}); err != nil {
				return err
			}
		}
		// 近 1 天内写 3 个分钟点：保持分钟级原样不降采样。
		recentBase := now.Add(-2 * time.Hour).Truncate(time.Hour)
		for idx := range 3 {
			ts := recentBase.Add(time.Duration(idx) * time.Minute).UnixMilli()
			if err := tx.UpsertAccountEquityPoint(store.EquityPointRecord{
				SpaceID: "s1", TradingAccountID: "acc-1", BucketTime: ts,
				Equity: "1050", AvailableFunds: "1050", UsedMargin: "0", SourceTime: ts,
			}); err != nil {
				return err
			}
		}
		return nil
	}))

	worker := &EquityHistoryDownsampler{
		Store: st,
		Now:   func() time.Time { return now },
	}
	require.NoError(t, worker.RunAll(ctx))

	points, err := st.ListAccountEquityPoints(ctx, "s1", "acc-1", 0, now.UnixMilli()+time.Minute.Milliseconds())
	require.NoError(t, err)
	require.Len(t, points, 4, "10 天前的 3 个点应合并为 1 个，近 1 天的 3 个分钟点完整保留")
}
