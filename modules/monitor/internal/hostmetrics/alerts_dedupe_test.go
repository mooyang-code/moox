package hostmetrics

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/stretchr/testify/require"
)

// 连续三条异常样本才触发告警。第二条样本计数落库后进程重启，EventBus 重投同一条消息：
// 重建的评估器（内存去重集合为空）不能把重复投递当成第三条样本。
func TestAlertDedupeSurvivesEvaluatorRestart(t *testing.T) {
	ctx := context.Background()
	db := openRegistryTestDB(t)
	repo := store.NewAlertRepository(db)
	rule := domain.AlertRule{RuleID: "rule-1", CheckID: "check-1", FailureThreshold: 3, SuccessThreshold: 1}
	sample := hostSample{agentID: "agent-a", hostname: "host-a", metric: HostMetricFilesystemUsage, value: 95, threshold: 80}
	now := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)

	first := &AlertEvaluator{Repository: repo}
	require.NoError(t, first.transition(ctx, rule, sample, "msg-1", true, 80, now))
	require.NoError(t, first.transition(ctx, rule, sample, "msg-2", true, 80, now))

	// 重启：全新的评估器，重投 msg-2
	restarted := &AlertEvaluator{Repository: repo}
	require.NoError(t, restarted.transition(ctx, rule, sample, "msg-2", true, 80, now))
	state, err := repo.GetState(ctx, SpaceID, rule.RuleID, rule.CheckID)
	require.NoError(t, err)
	require.Equal(t, 2, state.FailureCount, "重投的同一条样本不能再次累计")
	require.Equal(t, domain.AlertStatusOK, state.Status)

	// 真正的第三条样本才触发
	require.NoError(t, restarted.transition(ctx, rule, sample, "msg-3", true, 80, now))
	state, err = repo.GetState(ctx, SpaceID, rule.RuleID, rule.CheckID)
	require.NoError(t, err)
	require.Equal(t, 3, state.FailureCount)
	require.Equal(t, domain.AlertStatusFiring, state.Status)
}
