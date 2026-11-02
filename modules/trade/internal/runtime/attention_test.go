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
