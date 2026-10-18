package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/stretchr/testify/require"
)

// writeLifecycleScript 写一个记录自己收到的参数的 start.sh / stop.sh，模拟原生部署里转给 moox-runtime 的脚本。
func writeLifecycleScript(t *testing.T, root, name string) string {
	t.Helper()
	record := filepath.Join(root, name+".args")
	script := "#!/bin/sh\nprintf '%s' \"$*\" > '" + record + "'\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, name+".sh"), []byte(script), 0o755))
	return record
}

func readArgs(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	return strings.TrimSpace(string(raw))
}

// 原生部署的 start.sh / stop.sh 转给 moox-runtime，后者拒绝位置参数（“no positional arguments”）：
// 整个 Storage 单元不带参数，单个组件用 --components。以前把组件名当位置参数传，所有靠它停启服务的维护命令
// （reset-view-consumers、repair-view、force-rebuild-view）在“停止服务”这一步就失败。
func TestLifecycleScriptsAreCalledWithTheNativeRuntimeInterface(t *testing.T) {
	root := t.TempDir()
	stopArgs := writeLifecycleScript(t, root, "stop")
	startArgs := writeLifecycleScript(t, root, "start")

	require.NoError(t, runStorageComponentLifecycle(context.Background(), root, "stop", "storage", io.Discard))
	require.Equal(t, "", readArgs(t, stopArgs), "整个 Storage 单元不带参数")

	require.NoError(t, runStorageViewLifecycle(context.Background(), root, "stop", "new", io.Discard))
	require.Equal(t, "--components storage-view", readArgs(t, stopArgs), "只停 View 用 --components")

	require.NoError(t, runStorageViewLifecycle(context.Background(), root, "start", "new", io.Discard))
	require.Equal(t, "--components storage-view", readArgs(t, startArgs))

	require.Nil(t, lifecycleScriptArgs(""))
	require.Nil(t, lifecycleScriptArgs(" storage "))
}

// retain-views 要求 storage-view 已停止：status.sh 输出的是 JSON，按组件 ID 取状态。
func TestStorageViewStateReadsRuntimeStatusJSON(t *testing.T) {
	status := func(state string) []byte {
		return []byte(`{"host_id":"storage","operation":"status","components":[{"id":"storage-primary","state":"running","ready":true},{"id":"storage-view","state":"` + state + `","ready":false}]}`)
	}
	state, err := storageViewState(status("stopped"))
	require.NoError(t, err)
	require.Equal(t, "stopped", state)
	state, err = storageViewState(status("running"))
	require.NoError(t, err)
	require.Equal(t, "running", state)

	_, err = storageViewState([]byte(`{"components":[{"id":"storage-node","state":"stopped"}]}`))
	require.ErrorContains(t, err, "no storage-view")
	_, err = storageViewState([]byte("storage-view: stopped"))
	require.ErrorContains(t, err, "decode status output", "旧的文本格式不再识别")
}

func TestEnsureStorageViewStoppedUsesComponentsFlagAndJSON(t *testing.T) {
	root := t.TempDir()
	write := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "status.sh"), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	// 带位置参数时像 moox-runtime 一样拒绝；只有 --components storage-view 才返回状态。
	write(`[ "$1" = "--components" ] && [ "$2" = "storage-view" ] && [ $# -eq 2 ] || { echo "runtime requires --plan and no positional arguments" >&2; exit 1; }
printf '{"components":[{"id":"storage-view","state":"stopped"}]}'`)
	require.NoError(t, ensureStorageViewStopped(root))

	write(`printf '{"components":[{"id":"storage-view","state":"running"}]}'`)
	require.ErrorContains(t, ensureStorageViewStopped(root), "must be stopped")
	write(`printf '{"components":[]}'`)
	require.ErrorContains(t, ensureStorageViewStopped(root), "indeterminate")
}

// 原生部署里三个角色各有目录，配置里的数据路径相对于所属组件：元数据库在 storage-primary、Pebble 在 storage-node、
// View 索引在 storage-view——不管读的是哪一份配置。
func TestNativeLayoutResolvesEachDataPathAgainstItsOwningComponent(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"storage-primary", "storage-node", "storage-view"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir, "config"), 0o755))
	}
	storage := storageconfig.StorageConfig{Root: "./var/storage"}
	storage.Metadata.Path = "./var/storage/metadata/storage_metadata.db"
	storage.Devices.PebblePath = "./var/storage/pebble"
	storage.Devices.ViewIndexRoot = "./var/storage/view-indexes"

	for _, component := range []string{"storage-view", "storage-primary", "storage-node"} {
		got := resolveRepairStoragePaths(storage, root, filepath.Join(root, component, "config", "storage.yaml"))
		require.Equal(t, filepath.Join(root, "storage-primary", "var/storage/metadata/storage_metadata.db"), got.Metadata.Path, "读 %s 的配置", component)
		require.Equal(t, filepath.Join(root, "storage-node", "var/storage/pebble"), got.Devices.PebblePath)
		require.Equal(t, filepath.Join(root, "storage-view", "var/storage/view-indexes"), got.Devices.ViewIndexRoot)
	}

	absolute := storage
	absolute.Devices.PebblePath = "/data/pebble"
	require.Equal(t, "/data/pebble", resolveRepairStoragePaths(absolute, root, filepath.Join(root, "storage-view", "config", "storage.yaml")).Devices.PebblePath, "绝对路径不改")

	// 没有三个组件目录（单目录布局）：沿用按配置所在目录解析。
	single := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(single, "config"), 0o755))
	got := resolveRepairStoragePaths(storage, single, filepath.Join(single, "config", "storage.yaml"))
	require.Equal(t, filepath.Join(single, "var/storage/pebble"), got.Devices.PebblePath)
}

// 就绪检查用 healthcheck.sh 的 ready 字段（凭据由运行助手持有）；脚本不存在时退回 HTTP 探测；未就绪时脚本可能非零退出。
func TestNativeStorageViewReadyReadsHealthcheckOutput(t *testing.T) {
	root := t.TempDir()
	_, available := nativeStorageViewReady(context.Background(), root)
	require.False(t, available, "没有 healthcheck.sh 时不可用")

	write := func(body string) {
		require.NoError(t, os.WriteFile(filepath.Join(root, "healthcheck.sh"), []byte("#!/bin/sh\n"+body+"\n"), 0o755))
	}
	write(`[ "$1" = "--components" ] && [ "$2" = "storage-view" ] || exit 2
printf '{"components":[{"id":"storage-view","state":"running","ready":true}]}'`)
	ready, available := nativeStorageViewReady(context.Background(), root)
	require.True(t, available)
	require.True(t, ready)

	write(`printf '{"components":[{"id":"storage-view","state":"running","ready":false}]}'; exit 1`)
	ready, available = nativeStorageViewReady(context.Background(), root)
	require.True(t, available)
	require.False(t, ready, "未就绪时即使非零退出也能读到 ready=false")

	write(`echo garbage; exit 1`)
	ready, available = nativeStorageViewReady(context.Background(), root)
	require.True(t, available)
	require.False(t, ready)
}
