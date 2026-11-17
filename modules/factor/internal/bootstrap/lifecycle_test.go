package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// newLifecycleTestEnv 在因子管理库所在目录建好生命周期控制器。
func newLifecycleTestEnv(t *testing.T) *lifecycle.Controller {
	t.Helper()
	life, err := newMgrLifecycle(filepath.Join(t.TempDir(), "factor.db"))
	require.NoError(t, err)
	return life
}

// 修改类接口的门禁看真实的生命周期：模块暂停时拒绝修改，恢复后放行。
func TestMgrMutationGateFollowsModulePause(t *testing.T) {
	life := newLifecycleTestEnv(t)
	ctx := context.Background()
	gate := mutationGate(life)
	require.NoError(t, gate(), "正常时放行")

	_, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.ErrorContains(t, gate(), "不接受修改", "暂停时拒绝修改")

	_, err = life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.NoError(t, gate(), "恢复后放行")
}
