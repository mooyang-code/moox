package accessproxy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenSQLiteNoncesSecuresStorePath(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "data")
	path := filepath.Join(dir, "nonces.db")

	store, err := OpenSQLiteNonces(path)
	require.NoError(t, err)
	require.NoError(t, store.Close())

	dirInfo, err := os.Stat(dir)
	require.NoError(t, err)
	assertPerm := func(info os.FileInfo, mode os.FileMode) {
		require.Equal(t, mode, info.Mode().Perm())
	}
	assertPerm(dirInfo, 0o700)

	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	assertPerm(fileInfo, 0o600)
}
