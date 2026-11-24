package observability

import (
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func attentionChecks(t *testing.T, seed func(db *gorm.DB, now time.Time)) map[string]BusinessStatus {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) { seed(db, now) })
	overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).buildForTest(t.Context(), "")
	require.NoError(t, err)
	out := map[string]BusinessStatus{}
	for _, item := range overview.BusinessChecks {
		if item.Kind == AttentionCheckKind {
			out[item.CheckID] = item
		}
	}
	return out
}

// 各服务的关注项：检查 ID 是 attention:<服务名>:<kind>，名称取 name 标签；值为 1 的变成失败的业务检查，原因是 detail 标签；值回到 0 的恢复。
func TestAttentionBecomesBusinessChecks(t *testing.T) {
	checks := attentionChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "ec-failed", "callback-dispatcher", "callback-dispatcher@overseas", "moox_attention_active", `{"kind":"execution_failed","name":"事件通知失败","severity":"critical","detail":"币安下架通知：对方返回状态码 500"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "ec-detected", "callback-dispatcher", "callback-dispatcher@overseas", "moox_attention_active", `{"kind":"delist_detected","name":"发现标的即将下架","severity":"critical","detail":""}`, 0, now)
		seedOverviewMetricForInstance(t, db, "co-source", "collector", "collector@control", "moox_attention_active", `{"kind":"delist_source_failed","name":"下架检测来源连续失败","severity":"critical","detail":"OKX 永续合约信息 连续 3 次失败：超时"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "tr-rejected", "trade", "trade@control", "moox_attention_active", `{"kind":"target_rejected","name":"交易目标被拒绝","severity":"critical","detail":"x"}`, 1, now)
	})

	failed := checks["attention:callback-dispatcher:execution_failed"]
	require.Equal(t, "down", failed.Status)
	require.Equal(t, "事件通知失败", failed.Name)
	require.Equal(t, "callback-dispatcher", failed.Module)
	require.Equal(t, "币安下架通知：对方返回状态码 500", failed.Reason)
	require.Equal(t, events.SystemSpaceID, failed.SpaceID)
	require.Equal(t, "healthy", checks["attention:callback-dispatcher:delist_detected"].Status)
	require.Equal(t, "down", checks["attention:collector:delist_source_failed"].Status)
	require.Contains(t, checks["attention:collector:delist_source_failed"].Reason, "OKX 永续合约信息")
	require.Equal(t, "交易目标被拒绝", checks["attention:trade:target_rejected"].Name)
}

// 关注项与空间无关，归在 mooxsys 下：不指定空间或按 mooxsys 查询时带出，按业务空间查询时不带。
func TestAttentionChecksBelongToSystemSpace(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		seedOverviewMetricForInstance(t, db, "tr-rejected", "trade", "trade@control", "moox_attention_active", `{"kind":"target_rejected","name":"交易目标被拒绝","severity":"critical","detail":"x"}`, 1, now)
	})
	has := func(spaceID string) bool {
		overview, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).buildForTest(t.Context(), spaceID)
		require.NoError(t, err)
		for _, item := range overview.BusinessChecks {
			if item.CheckID == "attention:trade:target_rejected" {
				require.Equal(t, events.SystemSpaceID, item.SpaceID)
				return true
			}
		}
		return false
	}
	require.True(t, has(""))
	require.True(t, has(events.SystemSpaceID))
	require.False(t, has("crypto"))
}

// 级别为 warning 的关注项触发时是 degraded（需关注），不是 down：安装验收只把 down 当成故障；告警级别按上报的 warning。
func TestWarningAttentionIsDegraded(t *testing.T) {
	checks := attentionChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "co-key", "collector", "collector@control", "moox_attention_active", `{"kind":"delist_readonly_key_missing","name":"下架检测缺少币安只读 Key","severity":"warning","detail":"币安现货下架计划：没有读到只读 Key"}`, 1, now)
		seedOverviewMetricForInstance(t, db, "co-source", "collector", "collector@control", "moox_attention_active", `{"kind":"delist_source_failed","name":"下架检测来源连续失败","severity":"critical","detail":"超时"}`, 1, now)
	})
	key := checks["attention:collector:delist_readonly_key_missing"]
	require.Equal(t, "degraded", key.Status)
	require.Contains(t, key.Reason, "只读 Key")
	require.Equal(t, "warning", key.Severity)
	require.Equal(t, "down", checks["attention:collector:delist_source_failed"].Status)
	require.Equal(t, "critical", checks["attention:collector:delist_source_failed"].Severity)
}

func TestParseAttentionCheckID(t *testing.T) {
	service, kind, ok := ParseAttentionCheckID("attention:collector:delist_source_failed")
	require.True(t, ok)
	require.Equal(t, "collector", service)
	require.Equal(t, "delist_source_failed", kind)
	for _, id := range []string{"attention:collector", "module:delist_source_failed", "attention::x", "attention:a:b:c"} {
		_, _, ok := ParseAttentionCheckID(id)
		require.False(t, ok, id)
	}
}

// 只取未过期的序列：超过 500 条已过期的历史序列（series_id 排在前面）也挤不掉新鲜序列；detail 变化时取最新那条。
func TestAttentionReadsOnlyFreshSeries(t *testing.T) {
	checks := attentionChecks(t, func(db *gorm.DB, now time.Time) {
		for i := range 600 {
			seedOverviewMetricForInstance(t, db, fmt.Sprintf("a-old-%04d", i), "trade", "trade@control", "moox_attention_active",
				fmt.Sprintf(`{"kind":"auto_paused","name":"组合账户自动暂停","severity":"critical","detail":"旧 %d"}`, i), 1, now.Add(-time.Hour))
		}
		seedOverviewMetricForInstance(t, db, "z-older", "trade", "trade@control", "moox_attention_active", `{"kind":"auto_paused","name":"组合账户自动暂停","severity":"critical","detail":"上一条"}`, 1, now.Add(-20*time.Second))
		seedOverviewMetricForInstance(t, db, "z-new", "trade", "trade@control", "moox_attention_active", `{"kind":"auto_paused","name":"组合账户自动暂停","severity":"critical","detail":""}`, 0, now)
	})
	require.Len(t, checks, 1)
	require.Equal(t, "healthy", checks["attention:trade:auto_paused"].Status, "取最近一次上报的那条")
}

// 未过期的序列超过上限时报错：业务域本轮整体不推进，不能把截掉的关注项当成“不再产出”删掉。
func TestAttentionTooManyFreshSeriesFails(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	query := openOverviewMetrics(t, func(db *gorm.DB) {
		for i := range attentionSeriesLimit + 1 {
			seedOverviewMetricForInstance(t, db, fmt.Sprintf("s-%05d", i), "trade", "trade@control", "moox_attention_active",
				fmt.Sprintf(`{"kind":"k%d","name":"n","severity":"critical","detail":""}`, i), 0, now)
		}
	})
	_, err := (Builder{Metrics: query, Now: func() time.Time { return now }}).BusinessChecks(t.Context(), now)
	require.Error(t, err)
}

// 序列过期截止比服务判定多放一轮：生产者刚停报（序列 2 分 10 秒没更新）时仍按最后的值产出，等组件停止上报接手冻结。
func TestAttentionFreshCutoffHasOneExtraRound(t *testing.T) {
	checks := attentionChecks(t, func(db *gorm.DB, now time.Time) {
		seedOverviewMetricForInstance(t, db, "tr", "trade", "trade@control", "moox_attention_active", `{"kind":"auto_paused","name":"组合账户自动暂停","severity":"critical","detail":"x"}`, 1, now.Add(-130*time.Second))
		seedOverviewMetricForInstance(t, db, "tr-gone", "trade", "trade@control", "moox_attention_active", `{"kind":"target_blocked","name":"交易目标持续阻塞","severity":"critical","detail":"x"}`, 1, now.Add(-160*time.Second))
	})
	require.Equal(t, "down", checks["attention:trade:auto_paused"].Status)
	require.NotContains(t, checks, "attention:trade:target_blocked")
}
