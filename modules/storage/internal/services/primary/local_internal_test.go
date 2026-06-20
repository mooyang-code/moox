package primary

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLocalClientCloseKeepsSharedPebbleStoreOpenForOtherClients(t *testing.T) {
	root := t.TempDir()
	client1 := NewLocalClient(LocalClientOptions{Root: root})
	client2 := NewLocalClient(LocalClientOptions{Root: root})
	t.Cleanup(func() { _ = client2.Close() })

	store1, err := client1.factStore()
	require.NoError(t, err)
	store2, err := client2.factStore()
	require.NoError(t, err)
	require.Same(t, store1, store2)

	require.NoError(t, client1.Close())
	storeAfterClose, err := client2.factStore()
	require.NoError(t, err)
	require.Same(t, store2, storeAfterClose)
}
