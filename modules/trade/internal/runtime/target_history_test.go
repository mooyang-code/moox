package runtime

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/stretchr/testify/require"
)

// 有效期截止早于“现在 − 7 天”的回执和影子目标分批删掉，7 天内（含正好 7 天）的保留。
func TestTargetHistoryPrunerDeletesOnlyRowsExpiredMoreThanSevenDays(t *testing.T) {
	ctx := context.Background()
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tradeStore.Close()) })
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		return tx.CreateLogicalAccount(store.LogicalAccountRecord{
			SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "logical",
			OwnerInstanceID: "runner-1", OwnerSessionID: "session-1", ExecutionMode: "PAPER",
			SettlementAsset: "USDT", AutomationState: "PAUSED", PauseReason: "configure",
		})
	}))
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	cutoff := now.Add(-targetHistoryRetention)
	// 5 条早于截止线（要删），1 条正好在截止线上、1 条 6 天前（都保留）。
	validUntils := []time.Time{
		cutoff.Add(-30 * 24 * time.Hour), cutoff.Add(-24 * time.Hour), cutoff.Add(-time.Hour),
		cutoff.Add(-time.Minute), cutoff.Add(-time.Millisecond),
		cutoff, now.Add(-6 * 24 * time.Hour),
	}
	require.NoError(t, tradeStore.Transaction(ctx, func(tx *store.Tx) error {
		for i, validUntil := range validUntils {
			barEnd := validUntil.Add(-time.Minute).UnixMilli()
			if err := tx.InsertTargetReceipt(store.TargetReceiptRecord{
				SpaceID: "space-1", TargetID: fmt.Sprintf("target-%d", i), RunnerID: "runner-1@session-1",
				LogicalAccountID: "logical-1", CommandSequence: uint64(barEnd),
				InstanceID: "runner-1", SessionID: "session-1", StrategyID: "strategy-1",
				BarEndTime: barEnd, ValidUntil: validUntil.UnixMilli(),
				RequestHash: "hash", WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]",
				AcceptedAt: barEnd,
			}); err != nil {
				return err
			}
		}
		return nil
	}))
	for i, validUntil := range validUntils {
		_, err := tradeStore.RecordShadowTarget(ctx, store.ShadowTargetRecord{
			SpaceID: "space-1", TargetID: fmt.Sprintf("shadow-%d", i), LogicalAccountID: "logical-1",
			InstanceID: "runner-2", SessionID: "session-1", StrategyID: "strategy-1",
			BarEndTime: validUntil.Add(-time.Minute).UnixMilli(), ValidUntil: validUntil.UnixMilli(),
			WeightsJSON: "[]", ReferencePricesJSON: "{}", QuantityTargetsJSON: "[]", RecordedAt: validUntil.UnixMilli(),
		})
		require.NoError(t, err)
	}

	// 一批只删 limit 行。
	deleted, err := tradeStore.DeleteTargetReceiptsBefore(ctx, cutoff.UnixMilli(), 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)
	deleted, err = tradeStore.DeleteShadowTargetsBefore(ctx, cutoff.UnixMilli(), 2)
	require.NoError(t, err)
	require.Equal(t, int64(2), deleted)

	// 分批跑到删不满一批为止，剩下的过期行都删掉。
	pruner := &TargetHistoryPruner{Store: tradeStore, Now: func() time.Time { return now }, batch: 2}
	require.NoError(t, pruner.RunAll(ctx))
	for i := range validUntils {
		_, receiptErr := tradeStore.GetTargetReceipt(ctx, "space-1", fmt.Sprintf("target-%d", i))
		if i < 5 {
			require.Error(t, receiptErr, "回执 %d 已过期超过 7 天，应删掉", i)
		} else {
			require.NoError(t, receiptErr, "回执 %d 在 7 天内，应保留", i)
		}
	}
	shadows, err := tradeStore.ListShadowTargets(ctx, "space-1", "runner-2", 100)
	require.NoError(t, err)
	kept := make([]string, 0, len(shadows))
	for _, shadow := range shadows {
		kept = append(kept, shadow.TargetID)
	}
	require.ElementsMatch(t, []string{"shadow-5", "shadow-6"}, kept)
}

// 批删满就接着删下一批，删不满一批就停；每批都按同一个上限。
func TestDeleteInBatchesStopsAfterAShortBatch(t *testing.T) {
	remaining := int64(5)
	var limits []int
	total, err := deleteInBatches(context.Background(), 2, func(limit int) (int64, error) {
		limits = append(limits, limit)
		deleted := min(remaining, int64(limit))
		remaining -= deleted
		return deleted, nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(5), total)
	require.Equal(t, []int{2, 2, 2}, limits)

	// 正好删满时多跑一批确认已经删完。
	remaining, limits = 4, nil
	total, err = deleteInBatches(context.Background(), 2, func(limit int) (int64, error) {
		limits = append(limits, limit)
		deleted := min(remaining, int64(limit))
		remaining -= deleted
		return deleted, nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(4), total)
	require.Equal(t, []int{2, 2, 2}, limits)
}
