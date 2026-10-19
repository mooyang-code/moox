package command

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunSetupBuildLinuxRejectsUnknownModule(t *testing.T) {
	err := runSetupBuildLinux(t.Context(), "trade", "")
	require.EqualError(t, err, `unsupported linux CGO module "trade"`)
}

func TestRunSetupBuildLinuxRejectsPureGoFactor(t *testing.T) {
	err := runSetupBuildLinux(t.Context(), "factor-mgr", "")
	require.EqualError(t, err, `unsupported linux CGO module "factor-mgr"`)
}

func TestLinuxBuildSourceSelectsWorktreeWithoutCopyingManifest(t *testing.T) {
	configRoot, source := t.TempDir(), t.TempDir()
	for _, path := range []string{"go.work", ".go-version", "scripts/build/build-linux-local.sh"} {
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

func TestRunSetupBuildLinuxRunsLocalBuildScriptIntoSourceBin(t *testing.T) {
	source := t.TempDir()
	record := filepath.Join(source, "args.txt")
	for _, path := range []string{"go.work", ".go-version"} {
		require.NoError(t, os.WriteFile(filepath.Join(source, path), []byte("test"), 0600))
	}
	script := filepath.Join(source, "scripts", "build", "build-linux-local.sh")
	require.NoError(t, os.MkdirAll(filepath.Dir(script), 0700))
	require.NoError(t, os.WriteFile(script, []byte("#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >"+record+"\n"), 0700))

	require.NoError(t, runSetupBuildLinux(t.Context(), "storage-primary", source))
	got, err := os.ReadFile(record)
	require.NoError(t, err)
	require.Equal(t, "--out "+filepath.Join(source, "bin")+" storage-primary\n", string(got))
}
