package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestRunStorageMaintenanceBinaryDelegatesForceRebuildView(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "storage-cli")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\n"), 0o755))
	t.Setenv("MOOX_STORAGE_CLI", script)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runStorageMaintenanceBinaryAction(context.Background(), nil, &stdout, &stderr, "force-rebuild-view", []string{"--lookback", "72h", "--yes"}))
	raw, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Equal(t, "force-rebuild-view\n--lookback\n72h\n--yes\n", string(raw))
}

func TestRunStorageMaintenanceBinaryDelegatesResetViewConsumers(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "storage-cli")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\n"), 0o755))
	t.Setenv("MOOX_STORAGE_CLI", script)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runStorageMaintenanceBinaryAction(context.Background(), nil, &stdout, &stderr, "reset-view-consumers", []string{"--lookback", "72h", "--dry-run"}))
	raw, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Equal(t, "reset-view-consumers\n--lookback\n72h\n--dry-run\n", string(raw))
}

func TestStorageRemoteArgsAddPathDefaultsAndDefaultToPreview(t *testing.T) {
	repair := storageMaintenanceActions[0]
	require.Equal(t, "repair-view", repair.Action)
	args := storageRemoteArgs(repair, "/data/moox/storage/current", []string{"--space-id", "crypto", "--view-id", "v1", "--consumer", "view_0123456789ab"})
	require.Equal(t, []string{
		"--space-id", "crypto", "--view-id", "v1", "--consumer", "view_0123456789ab",
		"--package-root", "/data/moox/storage/current",
		"--storage-conf", "/data/moox/storage/current/storage-view/config/storage.yaml",
		"--dry-run",
	}, args, "没有 --yes 时默认预览")

	args = storageRemoteArgs(repair, "/data/moox/storage/current", []string{"--storage-conf=/custom.yaml", "-package-root", "/custom", "--yes"})
	require.Equal(t, []string{"--storage-conf=/custom.yaml", "-package-root", "/custom", "--yes"}, args, "用户给了的路径与 --yes 不改动")

	args = storageRemoteArgs(repair, "/r", []string{"--dry-run"})
	require.NotContains(t, args[1:], "--dry-run", "已经有 --dry-run 不重复加")
}

func TestStorageDeleteViewHasNoImplicitDryRun(t *testing.T) {
	var deleteView storageMaintenanceAction
	for _, action := range storageMaintenanceActions {
		if action.Action == "delete-view" {
			deleteView = action
		}
	}
	require.Equal(t, "delete-view", deleteView.Action)
	args := storageRemoteArgs(deleteView, "/data/moox/storage/current", []string{"--view", "crypto/v1"})
	require.NotContains(t, args, "--dry-run", "delete-view 没有 --dry-run，不带 --yes 由存储机上的程序拒绝")
	require.Contains(t, args, "/data/moox/storage/current/storage-primary/var/storage/metadata/storage_metadata.db")
}

func TestStorageMaintenanceRunsOnStorageHostWithLogScript(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CLI", "")
	fake := &hostFakeSSH{result: setupssh.Result{Stdout: "{\"status\":\"dry_run\"}\n"}}
	useHostFake(t, fake)
	var repair *cobra.Command
	for _, action := range storageMaintenanceActions {
		if action.Action == "repair-view" {
			repair = action.command()
		}
	}
	var out, errOut bytes.Buffer
	repair.SetOut(&out)
	repair.SetErr(&errOut)
	repair.SetContext(context.Background())
	require.NoError(t, repair.RunE(repair, []string{"--host", "storage", "--space-id", "crypto", "--view-id", "v1", "--consumer", "view_0123456789ab"}))
	require.Len(t, fake.ran, 1)
	argv := fake.ran[0]
	require.Equal(t, []string{"sh", "-c"}, argv[:2])
	require.Equal(t, storageRemoteScript, argv[2])
	require.Equal(t, []string{"sh", "/data/moox/storage/maintenance", "repair-view", "/data/moox/storage/current/bin/moox-storage-cli", "repair-view"}, argv[3:8])
	require.Contains(t, argv, "--dry-run")
	require.NotContains(t, argv, "--host", "--host 由 moox-cli 取走，不传给存储机")
	require.Equal(t, "{\"status\":\"dry_run\"}\n", out.String())

	fake.result, fake.runErr = setupssh.Result{Stderr: "need --yes", ExitCode: 2}, errors.New("ssh_command_failed")
	err := repair.RunE(repair, []string{"--host", "storage", "--yes"})
	var exit *hostExitError
	require.ErrorAs(t, err, &exit)
	require.Equal(t, 2, exit.ExitCode())
	require.Contains(t, errOut.String(), "need --yes")
}

func TestStorageMaintenanceNeedsStorageHostWhenNoneDeployed(t *testing.T) {
	t.Setenv("MOOX_STORAGE_CLI", "")
	useHostFake(t, &hostFakeSSH{})
	period := storageMaintenanceActions[len(storageMaintenanceActions)-1].command()
	period.SetContext(context.Background())
	period.SetOut(&bytes.Buffer{})
	err := period.RunE(period, []string{"--path", "/x"})
	require.ErrorContains(t, err, "没有部署 storage-primary")
}

func TestStorageMaintenanceHelpDoesNotNeedRemote(t *testing.T) {
	previous := openHostRemoteForCommand
	openHostRemoteForCommand = func(string) (*hostRemote, error) {
		t.Fatal("--help 不能连接主机")
		return nil, nil
	}
	t.Cleanup(func() { openHostRemoteForCommand = previous })
	cmd := storageMaintenanceActions[0].command()
	var out bytes.Buffer
	cmd.SetOut(&out)
	require.NoError(t, cmd.RunE(cmd, []string{"--help"}))
	require.Contains(t, out.String(), "经 SSH 在存储机上执行")
}

func TestHasCLIFlag(t *testing.T) {
	require.True(t, hasCLIFlag([]string{"--yes"}, "--yes"))
	require.True(t, hasCLIFlag([]string{"-yes"}, "--yes"))
	require.True(t, hasCLIFlag([]string{"--storage-conf=/x"}, "--storage-conf"))
	require.False(t, hasCLIFlag([]string{"--yesterday"}, "--yes"))
	require.False(t, hasCLIFlag([]string{"--", "--yes"}, "--yes"))
}
