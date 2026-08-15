package command

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRunFactorMaintenanceBinaryDelegatesClearQueue(t *testing.T) {
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	script := filepath.Join(dir, "factor-cli")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \""+argsFile+"\"\n"), 0o755))
	t.Setenv("MOOX_FACTOR_CLI", script)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runFactorMaintenanceBinary(context.Background(), nil, &stdout, &stderr, []string{"--yes", "--restart=false"}))
	raw, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Equal(t, "clear-queue\n--yes\n--restart=false\n", string(raw))
}
