package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunSetupBuildLinuxRejectsUnknownModule(t *testing.T) {
	err := runSetupBuildLinux(t.Context(), nil, "./moox.toml", "trade", "")
	require.EqualError(t, err, `unsupported linux CGO module "trade"`)
}

func TestLinuxBuildSourceSelectsWorktreeWithoutCopyingManifest(t *testing.T) {
	configRoot, source := t.TempDir(), t.TempDir()
	for _, path := range []string{"go.work", ".go-version", "scripts/build/build-storage-linux.sh"} {
		path = filepath.Join(source, path)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
		require.NoError(t, os.WriteFile(path, []byte("test"), 0600))
	}
	root, err := linuxBuildSource(configRoot, source)
	require.NoError(t, err)
	require.Equal(t, source, root)
	_, err = os.Stat(filepath.Join(root, "moox.toml"))
	require.True(t, os.IsNotExist(err))
	_, err = linuxBuildSource(configRoot, "")
	require.ErrorContains(t, err, "missing go.work")
}
