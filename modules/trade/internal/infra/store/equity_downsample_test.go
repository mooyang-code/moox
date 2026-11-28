package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/require"
)

func TestDownsampleEquityPointsRetentionTiers(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	require.NoError(t, st.Transaction(ctx, func(tx *Tx) error {
		if err := tx.CreateTradingAccount(TradingAccountRecord{
			SpaceID: "s1", TradingAccountID: "acc-1", Name: "acc-1",
			Exchange: string(exchange.ExchangeBinance), Markets: []string{"spot"},
			ExecutionMode: string(exchange.ExecutionModePaper), SettlementAsset: "USDT",
			Status: "ENABLED",
		}); err != nil {
			return err
		}
		if err := tx.CreateLogicalAccount(LogicalAccountRecord{
			SpaceID: "s1", LogicalAccountID: "log-1", Name: "log-1",
			Environment: string(exchange.AccountEnvironmentPaper), SettlementAsset: "USDT",
			PauseReason: "init",
		}); err != nil {
			return err
		}

		addBoth := func(at time.Time, equity string) error {
			ms := at.UnixMilli()
			if err := tx.UpsertAccountEquityPoint(EquityPointRecord{
				SpaceID: "s1", TradingAccountID: "acc-1", BucketTime: ms,
				Equity: equity, AvailableFunds: equity, UsedMargin: "0", SourceTime: ms,
			}); err != nil {
				return err
			}
			return tx.UpsertLogicalAccountEquityPoint(EquityPointRecord{
				SpaceID: "s1", LogicalAccountID: "log-1", BucketTime: ms,
				Equity: equity, AvailableFunds: equity, UsedMargin: "0", SourceTime: ms,
			})
		}

		// 1) 7 天以内：每分钟点全部保留（3 个点）
		for i := 0; i < 3; i++ {
			if err := addBoth(now.Add(-2*time.Hour).Add(time.Duration(i)*time.Minute), "1000"); err != nil {
				return err
			}
		}
		// 2) 7~30 天内（10 天前）：同一 15 分钟桶内 3 个点，只保留最后一个（10:14）
		base10d := now.Add(-10 * 24 * time.Hour).Truncate(15 * time.Minute)
		for _, min := range []int{1, 5, 14} {
			if err := addBoth(base10d.Add(time.Duration(min)*time.Minute), "900"); err != nil {
				return err
			}
		}
		// 3) 30~365 天内（60 天前）：同一 1 小时桶内 3 个点，只保留最后一个（:50）
		base60d := now.Add(-60 * 24 * time.Hour).Truncate(time.Hour)
		for _, min := range []int{10, 30, 50} {
			if err := addBoth(base60d.Add(time.Duration(min)*time.Minute), "800"); err != nil {
				return err
			}
		}
		// 4) 超过 365 天（400 天前）：同一自然日桶内 3 个点，只保留最后一个（23:00）
		base400d := now.Add(-400 * 24 * time.Hour).Truncate(24 * time.Hour)
		for _, hr := range []int{1, 12, 23} {
			if err := addBoth(base400d.Add(time.Duration(hr)*time.Hour), "700"); err != nil {
				return err
			}
		}
		return nil
	}))

	// 降采样前每张表各 12 条；降采样后每张表保留 3 + 1 + 1 + 1 = 6 条（每张表删除 6 条，共删除 12 条）。
	deleted, err := st.DownsampleEquityPoints(ctx, now, 2)
	require.NoError(t, err)
	require.Equal(t, int64(12), deleted)

	// 再次执行幂等：不再删除任何点。
	deletedAgain, err := st.DownsampleEquityPoints(ctx, now, 2)
	require.NoError(t, err)
	require.Zero(t, deletedAgain)

	accPoints, err := st.ListAccountEquityPoints(ctx, "s1", "acc-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, accPoints, 6)
	logPoints, err := st.ListLogicalAccountEquityPoints(ctx, "s1", "log-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, logPoints, 6)

	// 确认每段保留的均是桶内最后一个点。
	require.Equal(t, now.Add(-400*24*time.Hour).Truncate(24*time.Hour).Add(23*time.Hour).UnixMilli(), accPoints[0].BucketTime)
	require.Equal(t, now.Add(-60*24*time.Hour).Truncate(time.Hour).Add(50*time.Minute).UnixMilli(), accPoints[1].BucketTime)
	require.Equal(t, now.Add(-10*24*time.Hour).Truncate(15*time.Minute).Add(14*time.Minute).UnixMilli(), accPoints[2].BucketTime)
}
