package observability

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func attentionChecks(t *testing.T, seed func(db *gorm.DB, now time.Time)) map[string]BusinessStatus {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seed(db, now) })
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), "")
	require.NoError(t, err)
	out := map[string]BusinessStatus{}
	for _, item := range overview.BusinessChecks {
		out[item.Kind+":"+item.Module] = item
	}
	return out
}

// 事件中心和 Collector 的关注项：值为 1 的变成失败的业务检查，原因是 detail 标签；值回到 0 的恢复。
func TestEventCenterAndCollectorAttentionBecomeBusinessChecks(t *testing.T) {
	checks := attentionChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "ec-failed", "callbackdispatcher", "callback-dispatcher@overseas", "moox_callbackdispatcher_attention_active", `{"kind":"execution_failed","detail":"币安下架通知：对方返回状态码 500"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "ec-detected", "callbackdispatcher", "callback-dispatcher@overseas", "moox_callbackdispatcher_attention_active", `{"kind":"delist_detected","detail":""}`, 0, now)
		seedOverviewMetricForInstance(t, db, "ec-action", "callbackdispatcher", "callback-dispatcher@overseas", "moox_callbackdispatcher_attention_active", `{"kind":"action_failed","detail":"系统规则：平仓用完重试"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "co-source", "collector", "collector@control", "moox_collector_attention_active", `{"kind":"delist_source_failed","detail":"OKX 永续合约信息 连续 3 次失败：超时"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "co-key", "collector", "collector@control", "moox_collector_attention_active", `{"kind":"delist_readonly_key_missing","detail":""}`, 0, now)
		// 未知种类被忽略。
		seedOverviewMetricForInstance(t, db, "co-unknown", "collector", "collector@control", "moox_collector_attention_active", `{"kind":"something_else","detail":"x"}`, 1, now)
	})

	failed := checks["event_center_attention:execution_failed"]
	require.Equal(t, "down", failed.Status)
	require.Equal(t, "币安下架通知：对方返回状态码 500", failed.Reason)
	require.Equal(t, "crypto", failed.SpaceID)
	require.Equal(t, "healthy", checks["event_center_attention:delist_detected"].Status)
	require.Equal(t, "down", checks["event_center_attention:action_failed"].Status)

	require.Equal(t, "down", checks["collector_attention:delist_source_failed"].Status)
	require.Contains(t, checks["collector_attention:delist_source_failed"].Reason, "OKX 永续合约信息")
	require.Equal(t, "healthy", checks["collector_attention:delist_readonly_key_missing"].Status)
	require.NotContains(t, checks, "collector_attention:something_else")
	require.Empty(t, checks["trade_attention:target_rejected"].Kind, "Trade 的关注项不受影响")
}

// 缺币安只读 Key 触发时是 degraded（需关注），不是 down：安装验收只把 down 当成故障。
func TestReadonlyKeyMissingIsDegradedNotDown(t *testing.T) {
	checks := attentionChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "co-key", "collector", "collector@control", "moox_collector_attention_active", `{"kind":"delist_readonly_key_missing","detail":"币安现货下架计划：没有读到只读 Key"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "co-source", "collector", "collector@control", "moox_collector_attention_active", `{"kind":"delist_source_failed","detail":"超时"}`, 1, now)
	})
	key := checks["collector_attention:delist_readonly_key_missing"]
	require.Equal(t, "degraded", key.Status)
	require.Contains(t, key.Reason, "只读 Key")
	require.Equal(t, "down", checks["collector_attention:delist_source_failed"].Status)
	require.True(t, AttentionIsWarning("collector_attention", "delist_readonly_key_missing"))
	require.False(t, AttentionIsWarning("collector_attention", "delist_source_failed"))
}

func TestAttentionCheckNames(t *testing.T) {
	require.Equal(t, "交易目标被拒绝", AttentionCheckName("trade_attention", "target_rejected"))
	require.Equal(t, "估值降级（有持仓取不到价）", AttentionCheckName("trade_attention", "valuation_degraded"))
	// Trade 上报的每种关注项（modules/trade/internal/telemetry/attention.go 的 attentionKinds）都要有中文名，
	// 没登记的 kind 不会生成检查，告警会被悄悄丢掉。
	for _, kind := range []string{
		"target_rejected", "auto_paused", "target_blocked", "account_not_ready", "target_expired", "target_reversal",
		"rate_limited", "turnover_high", "unknown_results", "margin_ratio_low", "inspection_blocked", "inspection_failing",
		"transfer_shortfall", "transfer_failing", "valuation_degraded", "flatten_orders_unsettled", "release_orders_unverified",
	} {
		require.NotEmpty(t, AttentionCheckName("trade_attention", kind), kind)
	}
	require.Equal(t, "清仓结束时有子单没收拾掉，需人工核对", AttentionCheckName("trade_attention", "flatten_orders_unsettled"))
	require.Equal(t, "释放会话时没能撤单，只按本地记录释放", AttentionCheckName("trade_attention", "release_orders_unverified"))
	require.Equal(t, "发现标的即将下架", AttentionCheckName("event_center_attention", "delist_detected"))
	require.Equal(t, "下架检测缺少币安只读 Key", AttentionCheckName("collector_attention", "delist_readonly_key_missing"))
	require.Empty(t, AttentionCheckName("collector_attention", "unknown"))
	require.Empty(t, AttentionCheckName("balance", "x"))
	require.Equal(t, "callback-dispatcher", businessModule("event_center_attention:delist_detected"))
	require.Equal(t, "collector", businessModule("collector_attention:delist_source_failed"))
	require.Equal(t, "event_center_attention", businessKind(domain.Check{CheckID: "event_center_attention:delist_detected"}))
}
