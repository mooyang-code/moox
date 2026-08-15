package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

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
