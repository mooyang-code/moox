package command

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitpackage"
	"github.com/stretchr/testify/require"
)

// release build：必须给版本 ID；显式状态目录时不读取 moox.toml；参数转成绝对路径传给构建。
func TestSetupReleaseBuildCommand(t *testing.T) {
	var got unitdeploy.ReleaseOptions
	deps := setupDeps{buildRelease: func(_ context.Context, options unitdeploy.ReleaseOptions) (unitdeploy.Release, error) {
		got = options
		return unitdeploy.Release{ID: options.ReleaseID, SourceCommit: "c0ffee", Architecture: options.Architecture, Packages: map[string]unitpackage.Result{"host": {SHA256: "sha256:abc"}}}, nil
	}}
	run := func(args ...string) (string, error) {
		cmd := newSetupReleaseCommand(deps)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetArgs(args)
		err := cmd.Execute()
		return out.String(), err
	}
	_, err := run("build", "--state-dir", "/tmp/state")
	require.ErrorContains(t, err, "--release")

	output, err := run("build", "--release", "r1", "--state-dir", "state", "--source-dir", ".", "--storage-binary-dir", "storage-bin")
	require.NoError(t, err)
	require.Equal(t, "r1", got.ReleaseID)
	require.Equal(t, "amd64", got.Architecture)
	for _, path := range []string{got.StateDirectory, got.RepositoryRoot, got.StorageBinaryDirectory} {
		require.True(t, filepath.IsAbs(path), path)
	}
	require.Empty(t, got.BinaryDirectory)
	require.Contains(t, output, `"release":"r1"`)
	require.Contains(t, output, `"source_commit":"c0ffee"`)
}
