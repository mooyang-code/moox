package bootstrap

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/stretchr/testify/require"
)

type staleFixture struct {
	t            *testing.T
	repositories *store.Repositories
	now          time.Time
	results      int
}

func newStaleFixture(t *testing.T) *staleFixture {
	return &staleFixture{t: t, repositories: openMonitorTestRepositories(t), now: time.Now().UTC().Truncate(time.Second)}
}

func (f *staleFixture) addCheck(id string, enabled bool, source, group string) {
	f.t.Helper()
	check := domain.Check{SpaceID: "crypto", CheckID: id, Name: id, GroupName: group, Kind: domain.CheckKindExternal, Source: source, Enabled: enabled}
	require.NoError(f.t, f.repositories.Checks.Create(f.t.Context(), &check))
	require.NoError(f.t, f.repositories.Alerts.CreateRule(f.t.Context(), &domain.AlertRule{
		SpaceID: "crypto", RuleID: "default:" + id, CheckID: id, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true}))
}

func (f *staleFixture) addResult(id, message string, ago time.Duration) {
	f.t.Helper()
	f.results++
	require.NoError(f.t, f.repositories.Results.Insert(f.t.Context(), &domain.CheckResult{
		ResultID: id + "-" + time.Duration(f.results).String(), SpaceID: "crypto", CheckID: id, Success: true, Status: "healthy",
		ErrorMessage: message, CheckedAt: f.now.Add(-ago)}))
}

func (f *staleFixture) prune(policy staleCheckPolicy) []string {
	f.t.Helper()
	pruned, err := pruneStaleChecks(f.t.Context(), f.repositories, f.now, policy)
	require.NoError(f.t, err)
	return pruned
}

func (f *staleFixture) exists(id string) bool {
	_, err := f.repositories.Checks.Get(f.t.Context(), "crypto", id)
	return err == nil
}

// 一直处于“不再检查”超过宽限期的业务检查连同告警规则一起清掉；宽限期内、仍在正常评估的、还在告警的、
// 不是自动生成的业务检查都不动。
func TestPruneStaleChecksRemovesOnlyLongRetiredBusinessChecks(t *testing.T) {
	f := newStaleFixture(t)
	const dataset = "dataset:storage_view:view_gone:1m"
	f.addCheck(dataset, true, domain.CheckSourceObservability, "business")
	f.addResult(dataset, "数据按时更新", 3*time.Hour)
	f.addResult(dataset, domain.CheckReasonRetired, 2*time.Hour)
	f.addResult(dataset, domain.CheckReasonRetired, time.Minute)

	f.addCheck("dataset:strategy:recent:1m", true, domain.CheckSourceObservability, "business")
	f.addResult("dataset:strategy:recent:1m", "数据按时更新", 10*time.Minute)
	f.addResult("dataset:strategy:recent:1m", domain.CheckReasonRetired, time.Minute)

	f.addCheck("dataset:factor:live:1m", true, domain.CheckSourceObservability, "business")
	f.addResult("dataset:factor:live:1m", "数据按时更新", time.Minute)

	f.addCheck("dataset:factor:firing:1m", true, domain.CheckSourceObservability, "business")
	f.addResult("dataset:factor:firing:1m", domain.CheckReasonRetired, time.Minute)
	f.addResult("dataset:factor:firing:1m", "数据按时更新", 5*time.Hour)
	require.NoError(t, f.repositories.Alerts.UpsertState(t.Context(), &domain.AlertState{
		SpaceID: "crypto", RuleID: "default:dataset:factor:firing:1m", CheckID: "dataset:factor:firing:1m", Status: domain.AlertStatusFiring}))

	f.addCheck("placement:control:admin", true, domain.CheckSourcePlacement, "components")
	f.addResult("placement:control:admin", domain.CheckReasonRetired, 5*time.Hour)

	f.addCheck("console_page:other", true, domain.CheckSourceObservability, "console_page")
	f.addResult("console_page:other", domain.CheckReasonRetired, 5*time.Hour)

	require.Equal(t, []string{dataset}, f.prune(staleCheckPolicy{}))

	require.False(t, f.exists(dataset))
	rules, err := f.repositories.Alerts.ListRulesForCheck(t.Context(), "crypto", dataset)
	require.NoError(t, err)
	require.Empty(t, rules, "告警规则随检查一起清掉")
	for _, kept := range []string{"dataset:strategy:recent:1m", "dataset:factor:live:1m", "dataset:factor:firing:1m", "placement:control:admin", "console_page:other"} {
		require.True(t, f.exists(kept), kept)
	}

	// 清掉之后再跑一轮没有新的清理。
	require.Empty(t, f.prune(staleCheckPolicy{}))
}

// 检查没有任何有效结果（保留期已把旧结果清掉）时，从最早一条“不再检查”的结果起算宽限期。
func TestPruneStaleChecksUsesEarliestResultWhenNoValidResultRemains(t *testing.T) {
	f := newStaleFixture(t)
	f.addCheck("dataset:a:1m", true, domain.CheckSourceObservability, "business")
	f.addResult("dataset:a:1m", domain.CheckReasonRetired, 30*time.Minute)
	f.addCheck("dataset:b:1m", true, domain.CheckSourceObservability, "business")
	f.addResult("dataset:b:1m", domain.CheckReasonRetired, 3*time.Hour)
	f.addResult("dataset:b:1m", domain.CheckReasonRetired, time.Minute)

	require.Equal(t, []string{"dataset:b:1m"}, f.prune(staleCheckPolicy{}))
	require.True(t, f.exists("dataset:a:1m"))
}

// 已停用的业务检查超过保留期才清；刚停用的保留。没有结果的新检查不动。
func TestPruneStaleChecksRemovesLongDisabledChecksOnly(t *testing.T) {
	f := newStaleFixture(t)
	f.addCheck("gateway:old:heartbeat", false, domain.CheckSourceObservability, "business")
	f.addCheck("dataset:new:1m", true, domain.CheckSourceObservability, "business")

	require.Empty(t, f.prune(staleCheckPolicy{}), "刚创建、刚停用的检查不清")

	f.now = f.now.Add(8 * 24 * time.Hour)
	require.Equal(t, []string{"gateway:old:heartbeat"}, f.prune(staleCheckPolicy{}))
	require.False(t, f.exists("gateway:old:heartbeat"))
	require.True(t, f.exists("dataset:new:1m"), "启用着、没有结果的检查不清")
}

// 每轮最多清理 MaxPerRun 条，其余下一轮再清。
func TestPruneStaleChecksHonorsBatchLimit(t *testing.T) {
	f := newStaleFixture(t)
	for _, id := range []string{"dataset:x1:1m", "dataset:x2:1m", "dataset:x3:1m"} {
		f.addCheck(id, true, domain.CheckSourceObservability, "business")
		f.addResult(id, domain.CheckReasonRetired, 3*time.Hour)
		f.addResult(id, domain.CheckReasonRetired, time.Minute)
	}
	require.Len(t, f.prune(staleCheckPolicy{MaxPerRun: 2}), 2)
	require.Len(t, f.prune(staleCheckPolicy{MaxPerRun: 2}), 1)
	require.Empty(t, f.prune(staleCheckPolicy{MaxPerRun: 2}))
}

// 检查已不存在、存在超过最小时间的默认规则（清理与补齐并发时留下的孤儿）一并清掉；
// 新建的规则、有检查的规则、非默认描述的规则（如主机阈值规则）、仍在告警的规则不动。
func TestPruneStaleChecksRemovesOrphanDefaultRules(t *testing.T) {
	f := newStaleFixture(t)
	f.addCheck("dataset:live:1m", true, domain.CheckSourceObservability, "business")

	addRule := func(id, checkID, description string, age time.Duration) {
		require.NoError(t, f.repositories.Alerts.CreateRule(t.Context(), &domain.AlertRule{
			SpaceID: "crypto", RuleID: id, CheckID: checkID, FailureThreshold: 1, SuccessThreshold: 1, Enabled: true,
			Description: description, CreatedAt: f.now.Add(-age)}))
	}
	addRule("default:dataset:orphan:1m", "dataset:orphan:1m", defaultRuleDescription, time.Hour)
	addRule("default:dataset:fresh:1m", "dataset:fresh:1m", defaultRuleDescription, time.Minute)
	addRule("default:host:abcd:cpu", "host:abcd:cpu", `{"threshold":80}`, time.Hour)
	addRule("default:dataset:firing:1m", "dataset:firing:1m", defaultRuleDescription, time.Hour)
	require.NoError(t, f.repositories.Alerts.UpsertState(t.Context(), &domain.AlertState{
		SpaceID: "crypto", RuleID: "default:dataset:firing:1m", CheckID: "dataset:firing:1m", Status: domain.AlertStatusFiring}))

	f.prune(staleCheckPolicy{})

	rules, err := f.repositories.Alerts.ListRules(t.Context(), "crypto")
	require.NoError(t, err)
	var ids []string
	for _, rule := range rules {
		ids = append(ids, rule.RuleID)
	}
	require.ElementsMatch(t, []string{"default:dataset:live:1m", "default:dataset:fresh:1m", "default:host:abcd:cpu", "default:dataset:firing:1m"}, ids)
}
