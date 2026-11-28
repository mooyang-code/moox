package bootstrap

import (
	"errors"
	"time"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/mooyang-code/moox/packages/report/logsink"
	"trpc.group/trpc-go/trpc-go/log"
)

// 因子管理（FactorMgr）的生命周期：只有模块级的暂停，不参与系统暂停（moox-cli lifecycle --module factor-mgr 经 FactorMgr 的
// GetLifecycle、ControlLifecycle 操作）。暂停时修改类接口（创建、修改、删除、启停因子与因子任务，提交与取消重算）被拒绝，
// 读接口与引擎的同步、心跳、领取任务照常；它没有要追赶的数据，恢复即放行。

const lifecycleRefreshEvery = 30 * time.Second

func newMgrLifecycle(dbPath string) (*lifecycle.Controller, error) {
	return lifecycle.New(lifecycle.Options{
		Audit:     logsink.Audit,
		Module:    "factor-mgr",
		StateFile: lifecycle.StateFileForDB(dbPath),
		Hooks: lifecycle.Hooks{
			OnChange: func(from, to lifecycle.Stage, reason string) {
				log.Infof("factor_mgr_lifecycle_changed from=%s to=%s reason=%s", from, to, reason)
			},
		},
	})
}

// mutationGate 是修改类接口的门禁：模块被有意停下（暂停、恢复追赶）时拒绝。
func mutationGate(life *lifecycle.Controller) func() error {
	return func() error {
		if life.Halted() {
			return errors.New("因子管理当前不接受修改：模块已暂停或恢复追赶中（moox-cli lifecycle status --module factor-mgr 查看）")
		}
		return nil
	}
}
