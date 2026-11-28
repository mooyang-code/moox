package bootstrap

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/checks"
	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"trpc.group/trpc-go/trpc-go/log"
)

// Monitor 的生命周期：只有模块级的暂停。暂停 = **告警静默**：入库、检查评估、首页聚合照常，只是告警只记录、不推送
// （发送器返回 nil，和「没有通知渠道」同一条路径）；它没有要追赶的数据。静默结束时补发汇总见 checks/mute_summary.go；
// moox-cli monitor mute 带时长的屏蔽就是这个暂停（rpc/mute.go）。

const monitorLifecycleRefresh = 30 * time.Second

// alertSender 是告警推送的发送器工厂（告警评估、静默结束时的汇总共用）：暂停、恢复追赶期间静默（只记录），否则发给全部通知渠道。
func (r *Runtime) alertSender(ctx context.Context) (notification.Sender, error) {
	if life := r.life.Load(); life != nil && life.Halted() {
		return nil, nil
	}
	return notifier.Sender(ctx, r.Repositories.Notifications)
}

// onLifecycleChange 是 Monitor 生命周期的状态变化回调：进入暂停时记起点（重启后载入暂停时也记），回到运行时补发汇总。
func (r *Runtime) onLifecycleChange(from, to lifecycle.Stage, reason string) {
	log.Infof("monitor_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
	switch {
	case to == lifecycle.StagePaused:
		// 起点取生命周期持久化的暂停时间：Monitor 在静默期间重启时，回调里的「现在」是重启时刻，会丢掉重启前的事件。
		at := time.Now()
		if life := r.life.Load(); life != nil {
			if since := life.State().PausedSince; !since.IsZero() {
				at = since
			}
		}
		r.mutes.Enter(at)
	case from != "" && to == lifecycle.StageRunning:
		since := r.mutes.Leave()
		if since.IsZero() {
			since = time.Now().Add(-checks.MuteSummaryFallbackWindow)
		}
		r.Go(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := checks.SendMuteSummary(ctx, r.Repositories, since, time.Now()); err != nil {
				log.Errorf("monitor mute summary failed: %v", err)
			}
		})
	}
}

func newMonitorLifecycle(dbPath string, onChange func(from, to lifecycle.Stage, reason string)) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     logsink.Audit,
		Module:    "monitor",
		StateFile: lifecycle.StateFileForDB(dbPath),
		Hooks:     lifecycle.Hooks{OnChange: onChange},
	})
}
