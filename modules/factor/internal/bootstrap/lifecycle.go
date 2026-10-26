package bootstrap

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	factorhealth "github.com/mooyang-code/moox/modules/factor/internal/health"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// 因子管理（FactorMgr）的生命周期：模块级，没有对象。暂停时修改类接口（创建、修改、删除、启停因子与因子任务，提交与取消重算）被拒绝，
// 读接口与引擎的同步、心跳、领取任务照常；它没有要追赶的数据。重置见 reset.go。

const lifecycleRefreshEvery = 30 * time.Second

func newMgrLifecycle(db *store.Store, dbPath string) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     func(event string) { log.Infof("%s", event) },
		Module:    "factor-mgr",
		StateFile: filepath.Join(filepath.Dir(dbPath), "lifecycle.json"),
		Hooks: lifecycle.Hooks{
			CheckModule: func(ctx context.Context) []lifecycle.Check {
				if db == nil || db.Ping(ctx) != nil {
					return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckFail, Message: "因子管理数据库不可用"}}
				}
				return []lifecycle.Check{{Name: "数据库", Status: lifecycle.CheckPass}}
			},
			Reset: mgrResetSpec(db, dbPath),
			OnChange: func(_ lifecycle.Ref, from, to lifecycle.Stage, reason string) {
				log.Infof("factor_mgr_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}

// mutationGate 是修改类接口的门禁：模块被有意停下（暂停、恢复追赶、重置、故障）时拒绝。
func mutationGate(life *lifecycle.Controller) func() error {
	return func() error {
		if life.Halted(lifecycle.Ref{}) {
			return errors.New("因子管理当前不接受修改：模块已暂停、恢复追赶中或正在重置（moox-cli lifecycle status 查看）")
		}
		return nil
	}
}

func lifecycleRoutes(c *lifecycle.Controller) []factorhealth.Route {
	return []factorhealth.Route{
		{Path: lifecycle.StatusPath, Handler: c.StatusHandler()},
		{Path: lifecycle.CommandPath, Handler: c.CommandHandler()},
		{Path: lifecycle.CatchupPath, Handler: c.CatchupHandler()},
		{Path: lifecycle.ResetPrefix, Handler: c.ResetHandler()},
	}
}

// withLifecycle 把生命周期快照放进健康检查响应（Monitor 据此汇总），生命周期的检查本身不改变就绪判定。
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
