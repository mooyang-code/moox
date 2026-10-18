package unitruntime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func bootFixture(t *testing.T, bootID string) *runtimeState {
	t.Helper()
	previous := kernelBootID
	t.Cleanup(func() { kernelBootID = previous })
	kernelBootID = func() (string, error) { return bootID, nil }
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	state, err := openRuntime(Plan{HostID: "control", DeploymentRoot: root})
	require.NoError(t, err)
	t.Cleanup(state.close)
	return state
}

func writePID(t *testing.T, state *runtimeState, id, bootID string) {
	t.Helper()
	require.NoError(t, writeState(state.pids, id+".json", pidRecord{ComponentID: id, KernelBootID: bootID}))
}

func writeDrain(t *testing.T, state *runtimeState, id, bootID string) {
	t.Helper()
	require.NoError(t, writeState(state.draining, id, drainRecord{PID: 4242, KernelBootID: bootID}))
}

func exists(t *testing.T, root *os.Root, name string) bool {
	t.Helper()
	ok, err := stateExists(root, name)
	require.NoError(t, err)
	return ok
}

// 记录里的 boot ID 与当前不同才是上次开机的残留：清理进程记录与停止中标记，本次开机的保持不动；
// 读不懂的记录交给身份核对报错；暂停标记是用户的意图，保持不动；写入中的临时文件跳过。
func TestReconcileBootClearsOnlyRecordsFromPreviousBoot(t *testing.T) {
	state := bootFixture(t, "boot-c")
	writePID(t, state, "admin", "boot-b")
	writePID(t, state, "monitor", "boot-c")
	writeDrain(t, state, "collector", "boot-b")
	writeDrain(t, state, "factor-mgr", "boot-c")
	require.NoError(t, os.WriteFile(filepath.Join(state.pids.Name(), "broken.json"), []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(state.pids.Name(), ".state-temp"), []byte("{}"), 0o600))
	require.NoError(t, writeState(state.paused, "strategy", map[string]string{"host_id": "control"}))

	recovered, err := state.reconcileBoot()
	require.NoError(t, err)
	require.Equal(t, []BootRecovery{{Component: "admin", Kind: "pid"}, {Component: "collector", Kind: "draining"}}, recovered)
	require.False(t, exists(t, state.pids, "admin.json"))
	require.True(t, exists(t, state.pids, "monitor.json"))
	require.False(t, exists(t, state.draining, "collector"))
	require.True(t, exists(t, state.draining, "factor-mgr"), "本次开机内留下的停止中标记要由显式停止接续")
	require.True(t, exists(t, state.pids, "broken.json"))
	require.True(t, exists(t, state.paused, "strategy"))
	_, err = os.Stat(filepath.Join(state.pids.Name(), ".state-temp"))
	require.NoError(t, err)

	again, err := state.reconcileBoot()
	require.NoError(t, err)
	require.Empty(t, again, "同一次开机只清理一次")
}
