package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

// newLifecycleTestEnv 打开一个空的因子管理库并建好生命周期控制器。
func newLifecycleTestEnv(t *testing.T) *lifecycle.Controller {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "factor.db")
	db, err := store.Open(&store.Options{Path: dbPath})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	life, err := newMgrLifecycle(db, dbPath)
	require.NoError(t, err)
	life.Refresh(context.Background())
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
