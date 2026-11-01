package command

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/stretchr/testify/require"
)

func TestSetupBootstrapCoreUsesOneSnapshotAndExplicitStage(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called := 0, 0
	command := newSetupCommand(setupDeps{
		load: func(name string) (*setupconfig.Snapshot, error) {
			loaded++
			require.Equal(t, defaultSetupFile, name)
			return snapshot, nil
		},
		bootstrapCore: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.CoreOptions) (unitdeploy.CoreResult, error) {
			called++
			require.Same(t, snapshot, got)
			require.True(t, filepath.IsAbs(options.RepositoryRoot))
			require.True(t, filepath.IsAbs(options.StateDirectory))
			require.True(t, filepath.IsAbs(options.OperatorDirectory))
			require.NotNil(t, options.Log)
			return unitdeploy.CoreResult{Stage: "core-ready", OperatorConfig: filepath.Join(options.OperatorDirectory, "gateway-client.yaml")}, nil
		},
	})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"bootstrap"})
	require.ErrorContains(t, command.Execute(), "--stage core")
	require.Zero(t, loaded)
	state, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	command.SetArgs([]string{"bootstrap", "--stage", "core", "--state-dir", state, "--operator-dir", t.TempDir()})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, loaded)
	require.Equal(t, 1, called)
	require.Contains(t, output.String(), `"stage":"core-ready"`)
	require.NotContains(t, output.String(), "admin-test-password")
}

func TestSetupBootstrapControlCarriesOriginalAuthorization(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called := 0, 0
	command := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) {
		loaded++
		return snapshot, nil
	}, bootstrapControl: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.ControlOptions) (unitdeploy.CoreResult, error) {
		called++
		require.Same(t, snapshot, got)
		require.True(t, options.ProxyCA.Create)
		require.True(t, filepath.IsAbs(options.StateDirectory))
		return unitdeploy.CoreResult{Stage: "control-ready"}, nil
	}})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"bootstrap", "--stage", "core", "--create-proxy-ca"})
	require.ErrorContains(t, command.Execute(), "belongs to --stage control")
	require.Zero(t, loaded)
	state, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	command.SetArgs([]string{"bootstrap", "--stage", "control", "--create-proxy-ca", "--state-dir", state, "--operator-dir", t.TempDir()})
	require.ErrorContains(t, command.Execute(), "init_storage_required")
	require.Zero(t, called, "元数据没激活时不能动手")
	require.NoError(t, unitdeploy.MarkStep(state, unitdeploy.StepInitStorage))
	require.NoError(t, command.Execute())
	require.Equal(t, 2, loaded)
	require.Equal(t, 1, called)
	require.Contains(t, output.String(), `"stage":"control-ready"`)
	require.NotContains(t, output.String(), "admin-test-password")
}

func TestSetupBootstrapStopsBeforeUploadWhenPrerequisitesFail(t *testing.T) {
	snapshot := setupSnapshot(t)
	called := 0
	deps := setupDeps{load: func(string) (*setupconfig.Snapshot, error) { return snapshot, nil }, bootstrapCore: func(context.Context, *setupconfig.Snapshot, unitdeploy.CoreOptions) (unitdeploy.CoreResult, error) {
		called++
		return unitdeploy.CoreResult{}, nil
	}, scanDeployment: func(context.Context, *setupconfig.Snapshot, deployscan.Options) ([]deployscan.HostReport, error) {
		return []deployscan.HostReport{{Host: "control", Address: "198.51.100.10", OwnerOK: true, ModeOK: true, ParentWritable: true, DiskFreeKB: 3 << 20}}, nil
	}}
	command := newSetupCommand(deps)
	command.SetOut(&bytes.Buffer{})
	command.SetArgs([]string{"bootstrap", "--stage", "core", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
	require.ErrorContains(t, command.Execute(), "disk_low")
	require.Zero(t, called)
	require.Empty(t, diskWarnings([]deployscan.HostReport{{Host: "control", DiskFreeKB: 6 << 20, ReclaimableKB: 3 << 20}}, "control"), "失败残留下一次会自动清掉，不能挡住重试")

	control := snapshot.Manifest.ControlHost().Name
	snapshot.Manifest.Deployments[control] = slices.DeleteFunc(snapshot.Manifest.Deployments[control], func(id string) bool { return id == "archive-mgr" })
	require.ErrorContains(t, command.Execute(), "deployment_incomplete")
	require.Zero(t, called)
}
