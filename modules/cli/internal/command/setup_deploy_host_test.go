package command

import (
	"bytes"
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/stretchr/testify/require"
)

func TestSetupDeployHostUsesOneSnapshotAndPreservesPausedResult(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called := 0, 0
	command := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { loaded++; return snapshot, nil }, deployHost: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.HostOptions) (unitdeploy.HostResult, error) {
		called++
		require.Same(t, snapshot, got)
		require.Equal(t, "compute1", options.HostID)
		require.NotEmpty(t, options.StateDirectory)
		require.NotEmpty(t, options.OperatorDirectory)
		return unitdeploy.HostResult{Stage: "host-paused"}, nil
	}})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"deploy-host"})
	require.ErrorContains(t, command.Execute(), "--host")
	require.Zero(t, loaded)
	command.SetArgs([]string{"deploy-host", "--host", "compute1", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, loaded)
	require.Equal(t, 1, called)
	require.Contains(t, output.String(), `"stage":"host-paused"`)
	require.NotContains(t, output.String(), "admin-test-password")
}
