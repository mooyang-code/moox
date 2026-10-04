package marketwiring

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestHandlerStorageFactoryPropagatesManagedAuthError(t *testing.T) {
	setWiringStorageAuthConfig(t)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "")
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "wiring-test-secret")
	storage, err := NewHandler().NewStorage("ip://127.0.0.1:11003", "spot", "test")
	require.Error(t, err)
	require.Nil(t, storage)
	require.NotContains(t, err.Error(), "wiring-test-secret")
}

func TestHandlerStorageFactoryPreservesHostCredentials(t *testing.T) {
	setWiringStorageAuthConfig(t)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON", "")
	require.NoError(t, os.Unsetenv("MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON"))
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "wiring-test-secret")
	storage, err := NewHandler().NewStorage("ip://127.0.0.1:11003", "spot", "test")
	require.NoError(t, err)
	require.NotNil(t, storage)
}

func setWiringStorageAuthConfig(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "binance.yaml")
	require.NoError(t, os.WriteFile(path, []byte("storage:\n  bindings:\n    spot:\n      auth_info:\n        app_id: moox-collector\n        app_key: host-config-key\n"), 0600))
	t.Setenv("MOOX_STORAGE_MARKET_CONFIG", path)
}

type timerHandlerStorage struct{}

func (timerHandlerStorage) UpsertFields(context.Context, []*storagepb.RowFieldUpsert) error {
	return nil
}
