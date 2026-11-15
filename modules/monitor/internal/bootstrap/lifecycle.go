package bootstrap

import (
	"context"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/report/logsink"
)

// Monitor 的生命周期：只有模块级的暂停。暂停 = **告警静默**：入库、检查评估、首页聚合照常，只是告警只记录、不推送
// （发送器返回 nil，和「没有通知渠道」同一条路径）；它没有要追赶的数据。静默结束时补发汇总见 mute_summary.go；
// moox-cli monitor mute 带时长的屏蔽就是这个暂停（rpc/mute.go）。

const monitorLifecycleRefresh = 30 * time.Second

// alertSender 是告警推送的发送器工厂（告警评估、静默结束时的汇总共用）：暂停、恢复追赶期间静默（只记录），否则发给全部通知渠道。
func (r *Runtime) alertSender(ctx context.Context) (notification.Sender, error) {
	if life := r.life.Load(); life != nil && life.Halted() {
		return nil, nil
	}
	return notifier.Sender(ctx, r.Repositories.Notifications)
}

func newMonitorLifecycle(dbPath string, onChange func(from, to lifecycle.Stage, reason string)) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     logsink.Audit,
		Module:    "monitor",
		StateFile: filepath.Join(filepath.Dir(dbPath), "lifecycle.json"),
		Hooks:     lifecycle.Hooks{OnChange: onChange},
	})
}
