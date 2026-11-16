package unitinstall

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUnitStateCopiesIndependentFilesAndRefusesSymlinks(t *testing.T) {
	source := Prepared{Directory: privateParent(t), Components: []string{"monitor"}}
	destination := Prepared{Directory: privateParent(t), Components: source.Components}
	require.NoError(t, os.MkdirAll(filepath.Join(source.Directory, "monitor/data"), 0o700))
	original := filepath.Join(source.Directory, "monitor/data/database")
	require.NoError(t, os.WriteFile(original, []byte("stopped-snapshot"), 0o600))
	require.NoError(t, copyState(t.Context(), source, destination, nil))
	copied := filepath.Join(destination.Directory, "monitor/data/database")
	before, err := os.Stat(original)
	require.NoError(t, err)
	after, err := os.Stat(copied)
	require.NoError(t, err)
	require.False(t, os.SameFile(before, after))
	require.NoError(t, os.WriteFile(copied, []byte("new-write"), 0o600))
	raw, err := os.ReadFile(original)
	require.NoError(t, err)
	require.Equal(t, "stopped-snapshot", string(raw))
	require.NoError(t, os.Symlink(original, filepath.Join(source.Directory, "monitor/data/escape")))
	destination.Directory = privateParent(t)
	require.Error(t, copyState(t.Context(), source, destination, nil))
}
