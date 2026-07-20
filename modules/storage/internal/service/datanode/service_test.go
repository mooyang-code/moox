package datanode

import (
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
)

func TestNewServiceRequiresAuthSecret(t *testing.T) {
	_, err := NewService(Options{
		NodeID: "node-a",
		Pebble: pebble.Options{
			NodeID: "node-a",
			Path:   filepath.Join(t.TempDir(), "node"),
		},
	})
	if err == nil {
		t.Fatal("expected missing auth secret to be rejected")
	}
}
