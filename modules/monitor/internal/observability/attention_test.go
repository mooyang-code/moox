package observability

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/events"
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
	require.Equal(t, events.SystemSpaceID, failed.SpaceID)
	require.Equal(t, "healthy", checks["event_center_attention:delist_detected"].Status)
	require.Equal(t, "down", checks["event_center_attention:action_failed"].Status)

	require.Equal(t, "down", checks["collector_attention:delist_source_failed"].Status)
	require.Contains(t, checks["collector_attention:delist_source_failed"].Reason, "OKX 永续合约信息")
	require.Equal(t, "healthy", checks["collector_attention:delist_readonly_key_missing"].Status)
	require.NotContains(t, checks, "collector_attention:something_else")
	require.Empty(t, checks["trade_attention:target_rejected"].Kind, "Trade 的关注项不受影响")
}

// 余额对账和三类关注项与空间无关，归在 mooxsys 下：不指定空间或按 mooxsys 查询时带出，按业务空间查询时不带。
func TestBalanceAndAttentionChecksBelongToSystemSpace(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "balance-success", "trade", "trade@control", "moox_trade_balance_sync_last_success_timestamp_seconds", `{}`, float64(now.Unix()), now)
		seedOverviewMetricForInstance(t, db, "tr-rejected", "trade", "trade@control", "moox_trade_attention_active", `{"kind":"target_rejected","detail":"x"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "ec-failed", "callbackdispatcher", "callback-dispatcher@overseas", "moox_callbackdispatcher_attention_active", `{"kind":"execution_failed","detail":"x"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "co-source", "collector", "collector@control", "moox_collector_attention_active", `{"kind":"delist_source_failed","detail":"x"}`, 1, now)
	})
	kinds := func(spaceID string) map[string]string {
		overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).Build(t.Context(), spaceID)
		require.NoError(t, err)
		out := map[string]string{}
		for _, item := range overview.BusinessChecks {
			out[item.Kind] = item.SpaceID
		}
		return out
	}
	want := map[string]string{
		"balance": events.SystemSpaceID, "trade_attention": events.SystemSpaceID,
		"event_center_attention": events.SystemSpaceID, "collector_attention": events.SystemSpaceID,
	}
	for _, spaceID := range []string{"", events.SystemSpaceID} {
		got := kinds(spaceID)
		for kind, space := range want {
			require.Equal(t, space, got[kind], "space %q kind %s", spaceID, kind)
		}
	}
	got := kinds("crypto")
	for kind := range want {
		require.NotContains(t, got, kind)
	}
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
	// 东方财富冷却同样只是提醒：云函数采集不受影响，探测成功后自动恢复。
	require.True(t, AttentionIsWarning("collector_attention", "eastmoney_cooling_down"))
	require.NotEmpty(t, AttentionCheckName("collector_attention", "eastmoney_cooling_down"))
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
		"release_orders_stuck", "orders_instrument_gone", "fill_ingest_failed", "clock_skew", "isolated_position",
	} {
		require.NotEmpty(t, AttentionCheckName("trade_attention", kind), kind)
	}
	require.Equal(t, "清仓结束时有子单没收拾掉，需人工核对", AttentionCheckName("trade_attention", "flatten_orders_unsettled"))
	require.Equal(t, "释放会话时没能撤单，只按本地记录释放", AttentionCheckName("trade_attention", "release_orders_unverified"))
	require.Equal(t, "释放会话持续失败，目标单没撤下", AttentionCheckName("trade_attention", "release_orders_stuck"))
	require.Equal(t, "标的已下架的订单需人工核对成交", AttentionCheckName("trade_attention", "orders_instrument_gone"))
	require.Equal(t, "成交没能记进账本，账户卡在同一笔", AttentionCheckName("trade_attention", "fill_ingest_failed"))
	require.Equal(t, "本机时钟与交易所相差过大，请检查 NTP", AttentionCheckName("trade_attention", "clock_skew"))
	require.Equal(t, "交易所账户里有逐仓持仓，moox 只支持全仓单向", AttentionCheckName("trade_attention", "isolated_position"))
	require.Equal(t, "发现标的即将下架", AttentionCheckName("event_center_attention", "delist_detected"))
	require.Equal(t, "下架检测缺少币安只读 Key", AttentionCheckName("collector_attention", "delist_readonly_key_missing"))
	require.Empty(t, AttentionCheckName("collector_attention", "unknown"))
	require.Empty(t, AttentionCheckName("balance", "x"))
	require.Equal(t, "callback-dispatcher", businessModule("event_center_attention:delist_detected"))
	require.Equal(t, "collector", businessModule("collector_attention:delist_source_failed"))
	require.Equal(t, "event_center_attention", businessKind(domain.Check{CheckID: "event_center_attention:delist_detected"}))
}
