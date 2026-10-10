package command

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
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
	command.SetArgs([]string{"bootstrap", "--stage", "core", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, loaded)
	require.Equal(t, 1, called)
	require.Contains(t, output.String(), `"stage":"core-ready"`)
	require.NotContains(t, output.String(), "admin-test-password")
}
