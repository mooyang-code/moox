package command

import (
	"bytes"
	"context"
	"testing"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/stretchr/testify/require"
)

func TestSetupDeployUnitUsesOriginalSnapshotAndPreservesPausedStatus(t *testing.T) {
	snapshot := setupSnapshot(t)
	loaded, called := 0, 0
	command := newSetupCommand(setupDeps{load: func(string) (*setupconfig.Snapshot, error) { loaded++; return snapshot, nil }, deployUnit: func(_ context.Context, got *setupconfig.Snapshot, options unitdeploy.UnitOptions) (unitdeploy.UnitResult, error) {
		called++
		require.Same(t, snapshot, got)
		require.Equal(t, "compute1", options.HostID)
		require.Equal(t, "trade", options.Profile)
		return unitdeploy.UnitResult{Stage: "trade-paused"}, nil
	}})
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetArgs([]string{"deploy-unit", "--host", "compute1"})
	require.Error(t, command.Execute())
	require.Zero(t, loaded)
	command.SetArgs([]string{"deploy-unit", "--host", "compute1", "--profile", "trade", "--state-dir", t.TempDir(), "--operator-dir", t.TempDir()})
	require.NoError(t, command.Execute())
	require.Equal(t, 1, loaded)
	require.Equal(t, 1, called)
	require.Contains(t, output.String(), `"stage":"trade-paused"`)
	require.NotContains(t, output.String(), "admin-test-password")
}
