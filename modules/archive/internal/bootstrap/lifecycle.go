package bootstrap

import (
	"context"
	"path/filepath"
	"time"

	archivehealth "github.com/mooyang-code/moox/modules/archive/internal/health"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Archive 管理端的生命周期：模块级，没有对象。暂停 = 不再调度新的归档运行（定时触发与定时维护跳过），
// 已经在执行的运行由 worker 跑完；它没有要追赶的数据。重置见 reset.go。

const lifecycleRefreshEvery = 30 * time.Second

func newArchiveLifecycle(db *store.Store, dbPath string) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     func(event string) { log.Infof("%s", event) },
		Module:    "archive-mgr",
		StateFile: filepath.Join(filepath.Dir(dbPath), "lifecycle.json"),
		Hooks: lifecycle.Hooks{
			CheckModule: func(ctx context.Context) []lifecycle.Check {
				if db == nil || db.Ping(ctx) != nil {
					return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckFail, Message: "归档管理数据库不可用"}}
				}
				return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckPass}}
			},
			Reset: archiveResetSpec(db, dbPath),
			OnChange: func(_ lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("archive_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}

// gated 包装定时任务：模块被有意停下（暂停、恢复追赶、重置、故障）时本次触发直接跳过。
func gated(life *lifecycle.Controller, name string, run func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		if life.Halted(lifecycle.Ref{}) {
			log.Infof("archive_timer_skipped timer=%s reason=lifecycle_halted", name)
			return nil
		}
		return run(ctx)
	}
}

func lifecycleRoutes(c *lifecycle.Controller) []archivehealth.Route {
	return []archivehealth.Route{
		{Path: lifecycle.StatusPath, Handler: c.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: c.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: c.CatchupHandler()},
		{Path: lifecycle.ResetPrefix, Handler: c.ResetHandler()},
	}
}

// withLifecycle 把生命周期快照放进健康检查响应，生命周期的检查本身不改变就绪判定。
func withLifecycle(snapshot healthz.SnapshotFunc, c *lifecycle.Controller) healthz.SnapshotFunc {
	return func(ctx context.Context) healthz.Response {
		rsp := snapshot(ctx)
		if rsp.Details == nil {
			rsp.Details = map[string]any{}
		}
		rsp.Details["lifecycle"] = c.Compact()
		return rsp
	}
}
