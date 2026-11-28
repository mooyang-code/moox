package checks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

func TestMuteSummaryListsOpenAndRecoveredAlerts(t *testing.T) {
	_, repositories := openBusinessStore(t)
	ctx := context.Background()
	alerts := repositories.Checks
	base := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	event := func(id, check, kind string, at time.Time) {
		require.NoError(t, alerts.SaveRound(ctx, nil, nil, []domain.AlertEvent{{EventID: id, SpaceID: "crypto", CheckID: check, Title: "标题 " + check, Severity: domain.SeverityCritical, EventType: kind, CreatedAt: at}}))
	}
	// c_open：期间触发并仍未恢复；c_done：触发后又恢复；c_old：屏蔽前就在触发。
	event("e1", "c_open", domain.AlertEventTriggered, base.Add(10*time.Minute))
	event("e2", "c_done", domain.AlertEventTriggered, base.Add(20*time.Minute))
	event("e3", "c_done", domain.AlertEventResolved, base.Add(40*time.Minute))
	event("e0", "c_before", domain.AlertEventTriggered, base.Add(-time.Hour))
	for _, check := range []string{"c_open", "c_old"} {
		at := base
		require.NoError(t, repositories.Checks.SaveRound(ctx, []domain.Check{{SpaceID: "crypto", CheckID: check, Name: "检查名 " + check, Severity: domain.SeverityCritical,
			Status: domain.CheckStatusDown, CheckedAt: at, AlertStatus: domain.AlertStatusFiring, TriggeredAt: &at, CreatedAt: at}}, nil, nil))
	}
	// 屏蔽期间被删除的检查（对象移除）写的恢复事件同样计入“期间触发后已恢复”，标题取事件里记下的。
	event("e4", "c_removed", domain.AlertEventTriggered, base.Add(15*time.Minute))
	event("e5", "c_removed", domain.AlertEventResolved, base.Add(25*time.Minute))

	body, severity, ok, err := BuildMuteSummary(ctx, repositories, base, base.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, notification.SeverityWarning, severity, "仍有未恢复的告警时用警告级别")
	require.Contains(t, body, "仍未恢复（2 项）")
	require.Contains(t, body, "标题 c_open（触发 1 次")
	require.Contains(t, body, "检查名 c_old（屏蔽前已在触发）", "屏蔽前就在告警、期间没有事件的取检查行的名称")
	require.Contains(t, body, "期间触发后已恢复（2 项）")
	require.Contains(t, body, "标题 c_done（触发 1 次，恢复 1 次")
	require.Contains(t, body, "标题 c_removed（触发 1 次，恢复 1 次")
	require.NotContains(t, body, "c_before", "静默开始之前的事件不属于这次汇总")
}

func TestMuteSummaryIsSkippedWhenNothingHappened(t *testing.T) {
	_, repositories := openBusinessStore(t)
	_, _, ok, err := BuildMuteSummary(context.Background(), repositories, time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.False(t, ok)
}

func TestMuteTrackerKeepsTheEarliestStart(t *testing.T) {
	var tracker MuteTracker
	require.True(t, tracker.Leave().IsZero())
	first := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	tracker.Enter(first)
	tracker.Enter(first.Add(time.Hour))
	require.Equal(t, first, tracker.Leave())
	require.True(t, tracker.Leave().IsZero(), "取走后清空")
}

// 事件或触发态达到上限时，汇总正文写明只统计了最近的部分，不能悄悄少报。
func TestMuteSummaryStatesWhenTruncated(t *testing.T) {
	_, repositories := openBusinessStore(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	for i := 0; i < muteSummaryEventLimit; i++ {
		require.NoError(t, repositories.Checks.SaveRound(ctx, nil, nil, []domain.AlertEvent{{
			EventID: fmt.Sprintf("e%d", i), SpaceID: "crypto", CheckID: fmt.Sprintf("c%d", i%7), Title: "c", Severity: domain.SeverityCritical,
			EventType: domain.AlertEventTriggered, CreatedAt: base.Add(time.Duration(i) * time.Second),
		}}))
	}
	body, _, ok, err := BuildMuteSummary(ctx, repositories, base, base.Add(2*time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	require.Contains(t, body, "太多")
}
