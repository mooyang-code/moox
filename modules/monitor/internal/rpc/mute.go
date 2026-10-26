package rpc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 告警屏蔽复用 Monitor 的生命周期暂停：暂停期间探测、入库、告警评估与记录照常，只是不推送到通知渠道
// （见 bootstrap/lifecycle.go 的 alertSender）；到期由生命周期自动恢复。屏蔽结束时补发汇总由 bootstrap 的
// 生命周期回调完成。这里只是给运维（moox-cli）一个带时长、最长 24 小时的入口，免得忘了恢复。

// MaxMuteDuration 是一次屏蔽的最长时长。
const MaxMuteDuration = 24 * time.Hour

var errLifecycleUnavailable = errors.New("monitor lifecycle is not ready")

// muteReason 在用户写的原因前加上固定前缀，区分屏蔽与别的暂停。
const muteReasonPrefix = "告警屏蔽"

// MuteAlerts 屏蔽告警推送 duration_seconds 秒（1～86400）。
func (s *Service) MuteAlerts(ctx context.Context, req *monitorpb.MuteAlertsReq) (*monitorpb.MuteAlertsRsp, error) {
	life := s.controller()
	if life == nil {
		return &monitorpb.MuteAlertsRsp{RetInfo: inner(errLifecycleUnavailable)}, nil
	}
	seconds := req.GetDurationSeconds()
	if seconds == 0 || time.Duration(seconds)*time.Second > MaxMuteDuration {
		return &monitorpb.MuteAlertsRsp{RetInfo: invalid(fmt.Errorf("duration_seconds must be within [1, %d]", int(MaxMuteDuration/time.Second)))}, nil
	}
	self := life.Snapshot().Self
	if current := muteState(self); current.GetMuted() {
		return &monitorpb.MuteAlertsRsp{RetInfo: invalid(errors.New("alerts are already muted; unmute first")), State: current}, nil
	}
	// 模块已经因为别的原因暂停（系统暂停、手动暂停、恢复追赶、重置、故障）或带着控制意图时不能再设置屏蔽：
	// 手动暂停会覆盖已有的暂停信息，解除或到期时还会连别的暂停一起放行。
	switch self.Stage {
	case lifecycle.StageReady, lifecycle.StageWarming, lifecycle.StageRunning:
	default:
		return &monitorpb.MuteAlertsRsp{RetInfo: invalid(fmt.Errorf("monitor is %s (%s); alerts can only be muted while it is ready or running", self.Stage, self.Reason))}, nil
	}
	if self.Hold != lifecycle.HoldNone {
		return &monitorpb.MuteAlertsRsp{RetInfo: invalid(fmt.Errorf("monitor is under %s; alerts cannot be muted", self.Hold))}, nil
	}
	reason := muteReasonPrefix
	if text := strings.TrimSpace(req.GetReason()); text != "" {
		reason += "：" + text
	}
	obj, err := life.Execute(ctx, lifecycle.Request{
		Command:   lifecycle.CmdPause,
		PauseKind: lifecycle.PauseManual,
		Reason:    reason,
		By:        "monitor:mute",
		ExpiresAt: time.Now().Add(time.Duration(seconds) * time.Second),
	})
	if err != nil {
		return &monitorpb.MuteAlertsRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.MuteAlertsRsp{RetInfo: success(), State: muteState(obj)}, nil
}

// UnmuteAlerts 提前结束屏蔽。
func (s *Service) UnmuteAlerts(ctx context.Context, _ *monitorpb.UnmuteAlertsReq) (*monitorpb.UnmuteAlertsRsp, error) {
	life := s.controller()
	if life == nil {
		return &monitorpb.UnmuteAlertsRsp{RetInfo: inner(errLifecycleUnavailable)}, nil
	}
	if !muteState(life.Snapshot().Self).GetMuted() {
		return &monitorpb.UnmuteAlertsRsp{RetInfo: success(), State: &monitorpb.AlertMuteState{}}, nil
	}
	obj, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseManual, By: "monitor:unmute"})
	if err != nil {
		return &monitorpb.UnmuteAlertsRsp{RetInfo: inner(err)}, nil
	}
	return &monitorpb.UnmuteAlertsRsp{RetInfo: success(), State: muteState(obj)}, nil
}

// GetAlertMute 返回当前的屏蔽状态。
func (s *Service) GetAlertMute(_ context.Context, _ *monitorpb.GetAlertMuteReq) (*monitorpb.GetAlertMuteRsp, error) {
	life := s.controller()
	if life == nil {
		return &monitorpb.GetAlertMuteRsp{RetInfo: inner(errLifecycleUnavailable)}, nil
	}
	return &monitorpb.GetAlertMuteRsp{RetInfo: success(), State: muteState(life.Snapshot().Self)}, nil
}

func (s *Service) controller() *lifecycle.Controller {
	if s.lifecycle == nil {
		return nil
	}
	return s.lifecycle()
}

// muteState 把模块自身的生命周期对象转成屏蔽状态：处于暂停且是我们设置的屏蔽才算屏蔽。
func muteState(self lifecycle.Object) *monitorpb.AlertMuteState {
	if self.Stage != lifecycle.StagePaused || self.Pause == nil || !strings.HasPrefix(self.Pause.Reason, muteReasonPrefix) {
		return &monitorpb.AlertMuteState{}
	}
	state := &monitorpb.AlertMuteState{
		Muted:  true,
		Since:  self.Pause.Since.UTC().Format(time.RFC3339),
		Reason: strings.TrimPrefix(strings.TrimPrefix(self.Pause.Reason, muteReasonPrefix), "："),
		By:     self.Pause.By,
	}
	if !self.Pause.ExpiresAt.IsZero() {
		state.ExpiresAt = self.Pause.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return state
}
