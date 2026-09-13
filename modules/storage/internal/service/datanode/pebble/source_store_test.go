package pebble

import (
	"path/filepath"
	"testing"

	cpebble "github.com/cockroachdb/pebble"
	"github.com/stretchr/testify/require"
)

func TestSourceStoreRejectsUnidentifiedExistingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	require.NoError(t, ensureLayout(path))
	db, err := cpebble.Open(path, &cpebble.Options{})
	require.NoError(t, err)
	require.NoError(t, db.Set([]byte("existing"), []byte("data"), cpebble.Sync))
	require.NoError(t, db.Close())
	_, err = Open(Options{Path: path, NodeID: "node"})
	require.ErrorContains(t, err, "no source identity")
}

func TestSourceStoreIncarnationAndNodeBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	first, err := Open(Options{Path: path, NodeID: "node"})
	require.NoError(t, err)
	identity := first.sourceStoreID
	require.NotEmpty(t, identity)
	require.NoError(t, first.Close())
	reopened, err := Open(Options{Path: path, NodeID: "node"})
	require.NoError(t, err)
	require.Equal(t, identity, reopened.sourceStoreID)
	require.NoError(t, reopened.Close())
	_, err = Open(Options{Path: path, NodeID: "other"})
	require.ErrorContains(t, err, "identity")
	_, err = Open(Options{Path: path, NodeID: " node "})
	require.ErrorContains(t, err, "node_id")
	fresh, err := Open(Options{Path: filepath.Join(t.TempDir(), "fresh"), NodeID: "node"})
	require.NoError(t, err)
	require.NotEqual(t, identity, fresh.sourceStoreID)
	require.NoError(t, fresh.Close())
}
