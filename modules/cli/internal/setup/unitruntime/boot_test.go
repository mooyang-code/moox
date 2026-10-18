package unitruntime

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func bootFixture(t *testing.T, bootID string, bootedAt time.Time) *runtimeState {
	t.Helper()
	previousID, previousTime := kernelBootID, systemBootTime
	t.Cleanup(func() { kernelBootID, systemBootTime = previousID, previousTime })
	kernelBootID = func() (string, error) { return bootID, nil }
	systemBootTime = func() (time.Time, error) { return bootedAt, nil }
	root := t.TempDir()
	require.NoError(t, os.Chmod(root, 0o700))
	state, err := openRuntime(Plan{HostID: "control", DeploymentRoot: root})
	require.NoError(t, err)
	t.Cleanup(state.close)
	return state
}

func writePID(t *testing.T, state *runtimeState, id string, startedAt time.Time) {
	t.Helper()
	require.NoError(t, writeState(state.pids, id+".json", pidRecord{ComponentID: id, StartedAt: startedAt}))
}

func writeDrain(t *testing.T, state *runtimeState, id string, startedAt time.Time) {
	t.Helper()
	require.NoError(t, writeState(state.draining, id, drainRecord{PID: 4242, StartedAt: startedAt}))
}

func exists(t *testing.T, root *os.Root, name string) bool {
	t.Helper()
	ok, err := stateExists(root, name)
	require.NoError(t, err)
	return ok
}

// 新版本第一次运行时没有 boot.json：只清理明显早于本次开机（留一分钟余量）的记录，之后记下 boot ID。
func TestReconcileBootWithoutStateUsesBootTimeWithSkew(t *testing.T) {
	bootedAt := time.Date(2026, 10, 18, 10, 58, 39, 0, time.UTC)
	state := bootFixture(t, "boot-b", bootedAt)
	writePID(t, state, "admin", bootedAt.Add(-48*time.Hour))
	writePID(t, state, "monitor", bootedAt.Add(-30*time.Second)) // 时钟回拨也可能出现：在余量之内，不当成残留
	writePID(t, state, "strategy", bootedAt.Add(time.Hour))
	writeDrain(t, state, "collector", bootedAt.Add(-2*time.Hour))
	writeDrain(t, state, "factor-mgr", bootedAt.Add(10*time.Minute))
	require.NoError(t, os.WriteFile(filepath.Join(state.pids.Name(), "broken.json"), []byte("{"), 0o600))

	recovered, err := state.reconcileBoot()
	require.NoError(t, err)
	require.Equal(t, []BootRecovery{{Component: "admin", Kind: "pid"}, {Component: "collector", Kind: "draining"}}, recovered)
	require.False(t, exists(t, state.pids, "admin.json"))
	require.True(t, exists(t, state.pids, "monitor.json"))
	require.True(t, exists(t, state.pids, "strategy.json"))
	require.True(t, exists(t, state.pids, "broken.json"), "读不懂的记录在不确定重启时不擅自删除")
	require.False(t, exists(t, state.draining, "collector"))
	require.True(t, exists(t, state.draining, "factor-mgr"), "本次开机内留下的停止中标记要由显式停止接续")

	var saved bootState
	require.NoError(t, privateJSON(filepath.Join(state.run.Name(), bootStateName), &saved))
	require.Equal(t, "boot-b", saved.KernelBootID)
}

// boot ID 没变：什么也不动，不重写 boot.json。
func TestReconcileBootSameBootIsNoOp(t *testing.T) {
	bootedAt := time.Date(2026, 10, 18, 10, 58, 39, 0, time.UTC)
	state := bootFixture(t, "boot-b", bootedAt)
	require.NoError(t, writeState(state.run, bootStateName, bootState{KernelBootID: "boot-b", RecordedAt: bootedAt}))
	writePID(t, state, "admin", bootedAt.Add(-48*time.Hour))
	writeDrain(t, state, "collector", bootedAt.Add(-48*time.Hour))

	recovered, err := state.reconcileBoot()
	require.NoError(t, err)
	require.Empty(t, recovered)
	require.True(t, exists(t, state.pids, "admin.json"))
	require.True(t, exists(t, state.draining, "collector"))
	var saved bootState
	require.NoError(t, privateJSON(filepath.Join(state.run.Name(), bootStateName), &saved))
	require.True(t, saved.RecordedAt.Equal(bootedAt))
}

// boot ID 变了：确定重启过，全部进程记录与停止中标记都是上次开机的，包括看起来很新、读不懂的记录；
// 暂停标记是用户的意图，保持不动。写入中的临时文件跳过。
func TestReconcileBootAfterRebootClearsEveryRecordButKeepsPause(t *testing.T) {
	bootedAt := time.Date(2026, 10, 18, 10, 58, 39, 0, time.UTC)
	state := bootFixture(t, "boot-c", bootedAt)
	require.NoError(t, writeState(state.run, bootStateName, bootState{KernelBootID: "boot-b", RecordedAt: bootedAt.Add(-time.Hour)}))
	writePID(t, state, "admin", bootedAt.Add(time.Hour)) // 回拨的时钟让旧记录看起来比开机还晚
	require.NoError(t, os.WriteFile(filepath.Join(state.pids.Name(), "broken.json"), []byte("{"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(state.pids.Name(), ".state-temp"), []byte("{}"), 0o600))
	writeDrain(t, state, "collector", bootedAt.Add(time.Hour))
	require.NoError(t, writeState(state.paused, "strategy", map[string]string{"host_id": "control"}))

	recovered, err := state.reconcileBoot()
	require.NoError(t, err)
	require.Equal(t, []BootRecovery{
		{Component: "admin", Kind: "pid"},
		{Component: "broken", Kind: "pid"},
		{Component: "collector", Kind: "draining"},
	}, recovered)
	require.False(t, exists(t, state.pids, "admin.json"))
	require.False(t, exists(t, state.pids, "broken.json"))
	require.False(t, exists(t, state.draining, "collector"))
	require.True(t, exists(t, state.paused, "strategy"))
	_, err = os.Stat(filepath.Join(state.pids.Name(), ".state-temp"))
	require.NoError(t, err)

	again, err := state.reconcileBoot()
	require.NoError(t, err)
	require.Empty(t, again, "同一次开机只清理一次")
}
