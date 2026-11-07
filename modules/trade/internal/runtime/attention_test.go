package runtime

import (
	"context"
	"path/filepath"
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

// 保证金率风控暂停的组合：只要暂停原因还在（用户没确认恢复）就持续上报 margin_ratio_low，详情是固定文字；
// 不依赖带时间窗口的记录，过了窗口告警也不会丢。别的自动暂停原因不算。
func TestAttentionReportsMarginPauseUntilTheUserResumes(t *testing.T) {
	tradeStore, err := store.Open(filepath.Join(t.TempDir(), "trade.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = tradeStore.Close() })
	worker := &AttentionWorker{Store: tradeStore, blockedSince: map[string]time.Time{}}
	scan := func(reason string) map[string][]string {
		items := map[string][]string{}
		logical := store.LogicalAccountRecord{SpaceID: "space-1", LogicalAccountID: "logical-1", Name: "实盘组合", PauseReason: reason}
		require.NoError(t, worker.scanLogicalAccount(context.Background(), logical, time.Now(), items, map[string]time.Time{}))
		return items
	}

	items := scan(store.MarginPauseReason())
	require.Equal(t, []string{"实盘组合：保证金率风控已暂停，等待确认后恢复"}, items[telemetry.AttentionMarginRatioLow])
	require.Len(t, items[telemetry.AttentionAutoPaused], 1, "同时仍是自动暂停")
	require.NotContains(t, items[telemetry.AttentionAutoPaused][0], "0.")

	require.Empty(t, scan(store.AutoPauseReason("发现外部订单"))[telemetry.AttentionMarginRatioLow])
	require.Empty(t, scan("人工暂停")[telemetry.AttentionMarginRatioLow])
	require.Empty(t, scan("")[telemetry.AttentionMarginRatioLow], "恢复之后告警消失")
}
