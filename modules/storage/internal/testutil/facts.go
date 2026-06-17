package testutil

import (
	"path/filepath"
	"testing"

	devicepebble "github.com/mooyang-code/moox/modules/storage/internal/infra/device/pebble"
)

func OpenPebbleFactStore(t *testing.T, root string) *devicepebble.Store {
	t.Helper()
	store, err := devicepebble.Open(devicepebble.Options{Path: filepath.Join(root, "pebble", "main")})
	if err != nil {
		t.Fatalf("open pebble fact store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
