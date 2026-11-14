package pebble

import (
	"path/filepath"
	"testing"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
)

func TestSourceStoreMigratesUnidentifiedExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	require.NoError(t, ensureLayout(path))
	db, err := cpebble.Open(path, &cpebble.Options{})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("existing"), []byte("data"), cpebble.Sync))
	require.NoError(t, db.Close())
	store, err := Open(Options{Path: path})
	require.NoError(t, err)
	require.NotEmpty(t, store.sourceStoreID)
	require.NoError(t, store.Close())
	reopened, err := Open(Options{Path: path})
	require.NoError(t, err)
	require.Equal(t, store.sourceStoreID, reopened.sourceStoreID)
	require.NoError(t, reopened.Close())
}

// 实例标识重新打开时沿用，新建的库换一个新标识。
func TestSourceStoreIncarnation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	first, err := Open(Options{Path: path})
	require.NoError(t, err)
	identity := first.sourceStoreID
	require.NotEmpty(t, identity)
	require.NoError(t, first.Close())
	reopened, err := Open(Options{Path: path})
	require.NoError(t, err)
	require.Equal(t, identity, reopened.sourceStoreID)
	require.NoError(t, reopened.Close())
	fresh, err := Open(Options{Path: filepath.Join(t.TempDir(), "fresh")})
	require.NoError(t, err)
	require.NotEqual(t, identity, fresh.sourceStoreID)
	require.NoError(t, fresh.Close())
}
