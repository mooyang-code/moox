package bootstrap

import (
	"context"
	"path/filepath"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/notifier"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

// Monitor 的生命周期：模块级，没有对象。暂停 = **告警静默**：探测、入库、首页聚合照常，只是告警只记录、不推送
// （发送器返回 nil，和「没有通知渠道」同一条路径）；它没有要追赶的数据。静默结束时补发汇总见 mute_summary.go；
// moox-cli monitor mute 带时长的屏蔽就是这个暂停（rpc/mute.go）。重置见 reset.go。

const monitorLifecycleRefresh = 30 * time.Second

// alertSender 是三处告警推送共用的发送器工厂：暂停、恢复追赶、重置期间静默（只记录），否则发给全部通知渠道。
func (r *Runtime) alertSender(ctx context.Context) (notification.Sender, error) {
	if life := r.life.Load(); life != nil && life.Halted(lifecycle.Ref{}) {
		return nil, nil
	}
	return notifier.Sender(ctx, r.Repositories.Notifications)
}

func newMonitorLifecycle(db *store.Store, dbPath string, onChange func(ref lifecycle.Ref, from, to lifecycle.Stage, reason string)) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     func(event string) { log.Infof("%s", event) },
		Module:    "monitor",
		StateFile: filepath.Join(filepath.Dir(dbPath), "lifecycle.json"),
		Hooks: lifecycle.Hooks{
			CheckModule: func(ctx context.Context) []lifecycle.Check {
				if db == nil || db.Ping(ctx) != nil {
					return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckFail, Message: "Monitor 数据库不可用"}}
				}
				return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckPass}}
			},
			Reset:    monitorResetSpec(db, dbPath),
			OnChange: onChange,
		},
	})
}

// withMonitorLifecycle 把生命周期快照放进健康检查响应，生命周期的检查本身不改变就绪判定。
func withMonitorLifecycle(snapshot healthz.SnapshotFunc, life *lifecycle.Controller) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		rsp := snapshot(ctx)
		if rsp.Details == nil {
			rsp.Details = map[string]any{}
		}
		rsp.Details["lifecycle"] = life.Compact()
		return rsp
	}
}
