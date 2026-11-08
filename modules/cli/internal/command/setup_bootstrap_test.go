package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/stretchr/testify/require"
)

func TestSetupBootstrapAdminUsesOneSnapshotAndExplicitStage(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called := 0, 0
	command := newSetupCommand(setupDeps{
		load: func(name string) (*setupconfig.Snapshot, error) {
			loaded++
			require.Equal(t, defaultSetupFile, name)
			return snapshot, nil
		},
		bootstrapAdmin: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.DeployOptions) (unitdeploy.BootstrapResult, error) {
			called++
			require.Same(t, snapshot, got)
			require.True(t, filepath.IsAbs(options.RepositoryRoot))
			require.True(t, filepath.IsAbs(options.StateDirectory))
			require.True(t, filepath.IsAbs(options.OperatorDirectory))
			require.NotNil(t, options.Log)
			return unitdeploy.BootstrapResult{Stage: "admin-ready", OperatorConfig: filepath.Join(options.OperatorDirectory, "gateway-client.yaml")}, nil
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"bootstrap"})
	require.ErrorContains(t, command.Execute(), "--stage admin")
	require.Zero(t, loaded)
	state, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	command.SetArgs([]string{"bootstrap", "--stage", "admin", "--state-dir", state, "--operator-dir", t.TempDir()})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, loaded)
	require.Equal(t, 1, called)
	require.Contains(t, output.String(), `"stage":"admin-ready"`)
	require.NotContains(t, output.String(), "admin-test-password")
}

func TestSetupBootstrapControlCarriesOriginalAuthorization(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called, watched := 0, 0, 0
	watchdogResult, watchdogErr := json.RawMessage(`{}`), errors.New("crontab failed")
	command := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) {
		loaded++
		return snapshot, nil
	}, installWatchdog: func(context.Context, *setupconfig.Snapshot, setupconfig.Host, bool) (json.RawMessage, error) {
		watched++
		return watchdogResult, watchdogErr
	}, bootstrapControl: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.ControlOptions) (unitdeploy.BootstrapResult, error) {
		called++
		require.Same(t, snapshot, got)
		require.True(t, filepath.IsAbs(options.StateDirectory))
		return unitdeploy.BootstrapResult{Stage: "control-ready"}, nil
	}})
	var output bytes.Buffer
	command.SetOut(&output)
	state, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	command.SetArgs([]string{"bootstrap", "--stage", "control", "--state-dir", state, "--operator-dir", t.TempDir()})
	require.ErrorContains(t, command.Execute(), "init_storage_required")
	require.Zero(t, called, "元数据没激活时不能动手")
	require.NoError(t, unitdeploy.MarkStep(state, unitdeploy.StepInitStorage))
	require.ErrorContains(t, command.Execute(), "deployment_config_changed", "首次 control 要核对配置")
	require.Zero(t, called)
	require.NoError(t, unitdeploy.WriteBinding(state, setupBinding(snapshot)))
	password := snapshot.Manifest.Admin.Password
	snapshot.Manifest.Admin.Password = "another-admin-password"
	require.ErrorContains(t, command.Execute(), "deployment_config_changed", "改管理员密码后不能接着装")
	snapshot.Manifest.Admin.Password = password
	snapshot.Manifest.Notification.WebhookURL = "https://example.invalid/changed"
	require.ErrorContains(t, command.Execute(), "crontab failed", "改通知不影响部署配置")
	require.NoFileExists(t, filepath.Join(state, "control-ready.json"), "看护没装好不能写完成记录")
	watchdogErr = nil
	require.NoError(t, command.Execute(), "重跑能补上看护")
	require.FileExists(t, filepath.Join(state, "control-ready.json"))
	require.Equal(t, 2, watched)
	require.Equal(t, 5, loaded)
	require.Equal(t, 2, called)
	require.Contains(t, output.String(), `"stage":"control-ready"`)
	require.NotContains(t, output.String(), "admin-test-password")
}

func TestSetupBootstrapStopsBeforeUploadWhenPrerequisitesFail(t *testing.T) {
	snapshot := setupSnapshot(t)
	called := 0
	deps := setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }, bootstrapAdmin: func(context.Context, *setupconfig.Snapshot, unitdeploy.DeployOptions) (unitdeploy.BootstrapResult, error) {
		called++
		return unitdeploy.BootstrapResult{}, nil
	}, scanDeployment: func(context.Context, *setupconfig.Snapshot, deployscan.Options) ([]deployscan.HostReport, error) {
		return []deployscan.HostReport{{Host: "control", Address: "198.51.100.10", OwnerOK: true, ModeOK: true, ParentWritable: true, DiskFreeKB: 3 << 20}}, nil
	}}
	command := newSetupCommand(deps)
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{"bootstrap", "--stage", "admin", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
	require.ErrorContains(t, command.Execute(), "disk_insufficient")
	require.Zero(t, called)
	require.Empty(t, diskWarnings([]deployscan.HostReport{{Host: "control", DiskFreeKB: 6 << 20, ReclaimableKB: 3 << 20}}, "control"), "失败残留下一次会自动清掉，不能挡住重试")
	require.NotEmpty(t, diskWarnings([]deployscan.HostReport{{Host: "control", DiskFreeKB: 1 << 18, ReclaimableKB: 9 << 20}}, "control"), "上传本身要真实空间")
	require.NotEmpty(t, diskWarnings([]deployscan.HostReport{{Host: "control"}}, "control"), "磁盘写满时可用空间就是 0")
	require.Empty(t, diskWarnings([]deployscan.HostReport{{Host: "control", DiskFreeKB: -1}}, "control"), "没查到不告警")

	control := snapshot.Manifest.ControlHost().Name
	snapshot.Manifest.Deployments[control] = slices.DeleteFunc(snapshot.Manifest.Deployments[control], func(id string) bool { return id == "archive-mgr" })
	require.ErrorContains(t, command.Execute(), "deployment_incomplete")
	require.Zero(t, called)
}
