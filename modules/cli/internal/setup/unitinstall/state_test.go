package unitinstall

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnitStateCopiesIndependentFilesAndRefusesSymlinks(t *testing.T) {
	source := Prepared{Directory: privateParent(t), Components: []string{"web-host"}}
	destination := Prepared{Directory: privateParent(t), Components: source.Components}
	require.NoError(t, os.MkdirAll(filepath.Join(source.Directory, "web-host/data"), 0o700))
	original := filepath.Join(source.Directory, "web-host/data/database")
	require.NoError(t, os.WriteFile(original, []byte("stopped-snapshot"), 0o600))
	require.NoError(t, copyState(t.Context(), source, destination))
	copied := filepath.Join(destination.Directory, "web-host/data/database")
	before, err := os.Stat(original)
	require.NoError(t, err)
	after, err := os.Stat(copied)
	require.NoError(t, err)
	require.False(t, os.SameFile(before, after))
	require.NoError(t, os.WriteFile(copied, []byte("new-write"), 0o600))
	raw, err := os.ReadFile(original)
	require.NoError(t, err)
	require.Equal(t, "stopped-snapshot", string(raw))
	require.NoError(t, os.Symlink(original, filepath.Join(source.Directory, "web-host/data/escape")))
	destination.Directory = privateParent(t)
	require.Error(t, copyState(t.Context(), source, destination))
}

func TestProxyStateOutputCannotBypassItsTransferBound(t *testing.T) {
	var output boundedOutput
	_, err := io.Copy(&output, strings.NewReader(strings.Repeat("x", 8192)))
	require.Error(t, err)
	require.LessOrEqual(t, len(output.Bytes()), 4096)
	_, err = io.Copy(&output, io.LimitReader(strings.NewReader(strings.Repeat("x", 8192)), 8192))
	require.Error(t, err, "ReaderFrom optimization must not bypass the private output limit")
	require.LessOrEqual(t, len(output.Bytes()), 4096)
}
