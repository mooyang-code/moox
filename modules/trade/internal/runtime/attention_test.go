package runtime

import (
	"context"
	"testing"
	"time"

	targetapp "github.com/mooyang-code/moox/modules/trade/internal/application/target"
	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/modules/trade/internal/telemetry"
	"github.com/stretchr/testify/require"
)

func TestStableReasonDropsSignedURL(t *testing.T) {
	first := notReadyReason(`Get "https://fapi.binance.com/x?signature=aaa&timestamp=1": timeout`)
	second := notReadyReason(`Get "https://fapi.binance.com/x?signature=bbb&timestamp=2": timeout`)
	require.Equal(t, first, second)
	require.NotContains(t, first, "signature")
	require.Contains(t, first, "timeout")
	require.Equal(t, "账户未就绪", notReadyReason(""))
}

// 策略每根 K 线都发新目标，阻塞时长要跨目标累计。
func TestAttentionBlockedSpansNewTargets(t *testing.T) {
	worker := &AttentionWorker{blockedSince: map[string]time.Time{}}
	start := time.UnixMilli(1_000_000)
	logical := store.LogicalAccountRecord{LogicalAccountID: "logical-1", Name: "组合"}
	worker.blockedSince["logical-1"] = start
	target := store.LogicalAccountTargetRecord{
		Status: targetapp.StatusBlocked, EffectiveAt: 0, ValidUntil: 120_000,
		AcceptedAt:     start.Add(5 * time.Minute).UnixMilli(),
		BlockedTargets: []store.BlockedTarget{{InstrumentID: "BTC-USDT-SWAP", Reason: "余额不足"}},
	}
	items := map[string][]string{}
	blocked := map[string]time.Time{}
	require.NoError(t, worker.noteTarget(context.Background(), logical, target, start.Add(5*time.Minute+time.Second), items, blocked))
	require.Equal(t, start, blocked["logical-1"])
	require.Len(t, items[telemetry.AttentionTargetBlocked], 1)
}

// 每小时上限顺延的目标是已收敛（blocked_targets 里写着顺延原因）：不上报“目标持续阻塞”，也不开始计阻塞时长。
func TestAttentionIgnoresHourlyCapDeferral(t *testing.T) {
	worker := &AttentionWorker{blockedSince: map[string]time.Time{}}
	now := time.UnixMilli(1_000_000)
	logical := store.LogicalAccountRecord{LogicalAccountID: "logical-1", Name: "组合"}
	target := store.LogicalAccountTargetRecord{
		Status: targetapp.StatusConverged, EffectiveAt: 0, ValidUntil: 120_000,
		BlockedTargets: []store.BlockedTarget{{InstrumentID: "BTC-USDT-SWAP", Reason: "单币每小时上限已用完，顺延到下一个整点继续"}},
	}
	items := map[string][]string{}
	blocked := map[string]time.Time{}
	worker.blockedSince["logical-1"] = now.Add(-time.Hour)
	require.NoError(t, worker.noteTarget(context.Background(), logical, target, now, items, blocked))
	require.Empty(t, items[telemetry.AttentionTargetBlocked])
	require.Empty(t, blocked)
}

// 合约钱包可划转资金不足同时有 transfer_shortfall 在报：同一个缺口不再发 target_blocked；别的原因的阻塞照常报。
func TestAttentionFundsShortageIsReportedOnlyAsTransferShortfall(t *testing.T) {
	worker := &AttentionWorker{blockedSince: map[string]time.Time{}}
	start := time.UnixMilli(1_000_000)
	now := start.Add(5*time.Minute + time.Second)
	logical := store.LogicalAccountRecord{LogicalAccountID: "logical-1", Name: "组合"}
	worker.blockedSince["logical-1"] = start
	target := store.LogicalAccountTargetRecord{
		Status: targetapp.StatusBlocked, EffectiveAt: 0, ValidUntil: 120_000,
		BlockedTargets: []store.BlockedTarget{{InstrumentID: "BTC-USDT-SPOT", Reason: "合约钱包可划转的资金不足，现货买入按现有资金缩小"}},
	}
	items := map[string][]string{}
	require.NoError(t, worker.noteTarget(context.Background(), logical, target, now, items, map[string]time.Time{}))
	require.Empty(t, items[telemetry.AttentionTargetBlocked])

	target.BlockedTargets = append(target.BlockedTargets, store.BlockedTarget{InstrumentID: "ETH-USDT-SPOT", Reason: "交易所拒单"})
	require.NoError(t, worker.noteTarget(context.Background(), logical, target, now, items, map[string]time.Time{}))
	require.Len(t, items[telemetry.AttentionTargetBlocked], 1)
	require.Contains(t, items[telemetry.AttentionTargetBlocked][0], "ETH-USDT-SPOT")
}

// 资金缺口是持续状态：目标 BLOCKED 且带着资金不足的项，每次扫描都上报（不等阻塞超过一根 K 线，也不依赖事件窗口）；
// 目标不再阻塞就不报。
func TestAttentionFundsShortageIsDerivedFromCurrentTargetState(t *testing.T) {
	worker := &AttentionWorker{blockedSince: map[string]time.Time{}}
	now := time.UnixMilli(1_000_000)
	logical := store.LogicalAccountRecord{LogicalAccountID: "logical-1", Name: "组合"}
	target := store.LogicalAccountTargetRecord{
		Status: targetapp.StatusBlocked, EffectiveAt: 0, ValidUntil: 3_600_000,
		BlockedTargets: []store.BlockedTarget{{InstrumentID: "BTC-USDT-SPOT", Quantity: "0.005", Reason: "合约钱包可划转的资金不足，现货买入按现有资金缩小"}},
	}
	for range 2 {
		items := map[string][]string{}
		require.NoError(t, worker.noteTarget(context.Background(), logical, target, now, items, map[string]time.Time{}))
		require.Equal(t, []string{"组合：合约钱包可划转的资金不足，现货买入被缩小或挡下"}, items[telemetry.AttentionTransferShortfall])
		require.Empty(t, items[telemetry.AttentionTargetBlocked], "同一个缺口不再发 target_blocked")
		now = now.Add(time.Hour)
	}
	target.Status = targetapp.StatusConverged
	items := map[string][]string{}
	require.NoError(t, worker.noteTarget(context.Background(), logical, target, now, items, map[string]time.Time{}))
	require.Empty(t, items[telemetry.AttentionTransferShortfall])
}
