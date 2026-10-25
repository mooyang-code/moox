package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	monitorrpc "github.com/mooyang-code/moox/modules/monitor/internal/rpc"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
)

// 告警屏蔽：MuteAlerts 复用生命周期暂停，期间发送器为 nil（只记录、不推送），UnmuteAlerts 恢复。
func TestMuteAlertsSilencesPushesUntilUnmuted(t *testing.T) {
	_, _, runtime := newMonitorResetEnv(t)
	ctx := context.Background()
	_, err := runtime.Repositories.Notifications.Add(ctx, "wecom", "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=abcd1234")
	require.NoError(t, err)
	service := monitorrpc.New(runtime.Repositories, monitorrpc.Options{Lifecycle: runtime.life.Load})

	for _, seconds := range []uint32{0, 24*3600 + 1} {
		rsp, err := service.MuteAlerts(ctx, &monitorpb.MuteAlertsReq{DurationSeconds: seconds})
		require.NoError(t, err)
		require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "时长 %d 秒应拒绝", seconds)
	}
	sender, err := runtime.alertSender(ctx)
	require.NoError(t, err)
	require.NotNil(t, sender)

	rsp, err := service.MuteAlerts(ctx, &monitorpb.MuteAlertsReq{DurationSeconds: 3600, Reason: "初始数据导入"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
	require.True(t, rsp.GetState().GetMuted())
	require.Equal(t, "初始数据导入", rsp.GetState().GetReason())
	expires, err := time.Parse(time.RFC3339, rsp.GetState().GetExpiresAt())
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(time.Hour), expires, 5*time.Second)
	sender, err = runtime.alertSender(ctx)
	require.NoError(t, err)
	require.Nil(t, sender, "屏蔽期间告警静默")

	again, err := service.MuteAlerts(ctx, &monitorpb.MuteAlertsReq{DurationSeconds: 60})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_INVALID_PARAM, again.GetRetInfo().GetCode(), "已在屏蔽中不能重复设置")
	got, err := service.GetAlertMute(ctx, &monitorpb.GetAlertMuteReq{})
	require.NoError(t, err)
	require.True(t, got.GetState().GetMuted())

	un, err := service.UnmuteAlerts(ctx, &monitorpb.UnmuteAlertsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, un.GetRetInfo().GetCode(), un.GetRetInfo().GetMsg())
	require.False(t, un.GetState().GetMuted())
	sender, err = runtime.alertSender(ctx)
	require.NoError(t, err)
	require.NotNil(t, sender, "解除后恢复推送")

	idle, err := service.UnmuteAlerts(ctx, &monitorpb.UnmuteAlertsReq{})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, idle.GetRetInfo().GetCode(), "没有屏蔽时解除是空操作")
}

// 不是屏蔽设置的暂停（例如系统暂停）不算屏蔽，也不能被 UnmuteAlerts 解除。
func TestMuteStateIgnoresOtherPauses(t *testing.T) {
	life, _, runtime := newMonitorResetEnv(t)
	ctx := context.Background()
	service := monitorrpc.New(runtime.Repositories, monitorrpc.Options{Lifecycle: runtime.life.Load})
	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem, Reason: "系统维护"})
	require.NoError(t, err)

	got, err := service.GetAlertMute(ctx, &monitorpb.GetAlertMuteReq{})
	require.NoError(t, err)
	require.False(t, got.GetState().GetMuted())
}

func TestMuteSummaryListsOpenAndRecoveredAlerts(t *testing.T) {
	_, _, runtime := newMonitorResetEnv(t)
	ctx := context.Background()
	alerts := runtime.Repositories.Alerts
	base := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	event := func(id, check, kind string, at time.Time) {
		require.NoError(t, alerts.CreateEvent(ctx, &domain.AlertEvent{EventID: id, SpaceID: "crypto", RuleID: "default:" + check, CheckID: check, EventType: kind, Status: kind, CreatedAt: at}))
	}
	// c_open：期间触发并仍未恢复；c_done：触发后又恢复；c_old：屏蔽前就在触发。
	event("e1", "c_open", domain.AlertEventTriggered, base.Add(10*time.Minute))
	event("e2", "c_done", domain.AlertEventTriggered, base.Add(20*time.Minute))
	event("e3", "c_done", domain.AlertEventResolved, base.Add(40*time.Minute))
	event("e0", "c_before", domain.AlertEventTriggered, base.Add(-time.Hour))
	for _, check := range []string{"c_open", "c_old"} {
		require.NoError(t, alerts.UpsertState(ctx, &domain.AlertState{SpaceID: "crypto", RuleID: "default:" + check, CheckID: check, Status: domain.AlertStatusFiring, DedupeKey: check}))
	}

	body, severity, ok, err := runtime.buildMuteSummary(ctx, base, base.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, notification.SeverityWarning, severity, "仍有未恢复的告警时用警告级别")
	require.Contains(t, body, "仍未恢复（2 项）")
	require.Contains(t, body, "c_open（触发 1 次")
	require.Contains(t, body, "c_old（屏蔽前已在触发）")
	require.Contains(t, body, "期间触发后已恢复（1 项）")
	require.Contains(t, body, "c_done（触发 1 次，恢复 1 次")
	require.NotContains(t, body, "c_before", "静默开始之前的事件不属于这次汇总")
}

func TestMuteSummaryIsSkippedWhenNothingHappened(t *testing.T) {
	_, _, runtime := newMonitorResetEnv(t)
	_, _, ok, err := runtime.buildMuteSummary(context.Background(), time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.False(t, ok)
}

func TestMuteTrackerKeepsTheEarliestStart(t *testing.T) {
	var tracker muteTracker
	require.True(t, tracker.leave().IsZero())
	first := time.Date(2026, 10, 25, 4, 0, 0, 0, time.UTC)
	tracker.enter(first)
	tracker.enter(first.Add(time.Hour))
	require.Equal(t, first, tracker.leave())
	require.True(t, tracker.leave().IsZero(), "取走后清空")
}

// 阶段回调：进入暂停时记下静默起点，回到放行阶段时取走（并异步汇总）；模块对象的阶段变化不处理。
func TestLifecycleChangeTracksMuteWindow(t *testing.T) {
	_, _, runtime := newMonitorResetEnv(t)
	runtime.onLifecycleChange(lifecycle.Ref{Kind: "x", ID: "y"}, lifecycle.StageRunning, lifecycle.StagePaused, "")
	require.True(t, runtime.mutes.leave().IsZero(), "对象的阶段变化不算模块静默")

	runtime.onLifecycleChange(lifecycle.Ref{}, lifecycle.StageRunning, lifecycle.StagePaused, "mute")
	runtime.onLifecycleChange(lifecycle.Ref{}, lifecycle.StagePaused, lifecycle.StageResuming, "")
	require.False(t, runtime.mutes.since.Load() == 0, "恢复追赶仍在静默中，起点保留")
	runtime.onLifecycleChange(lifecycle.Ref{}, lifecycle.StageResuming, lifecycle.StageRunning, "")
	require.Zero(t, runtime.mutes.since.Load(), "回到放行阶段后起点被取走")
	runtime.workers.Wait()
}
