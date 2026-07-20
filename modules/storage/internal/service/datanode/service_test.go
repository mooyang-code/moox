package datanode

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode/pebble"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
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

func TestErrorCodeUsesTypedValidationErrors(t *testing.T) {
	if got := errorCode(errors.New("required backend is unavailable")); got != pb.ErrorCode_INNER_ERR {
		t.Fatalf("plain error classified as %s", got)
	}
	_, validationErr := pebble.NormalizeRowKey(nil)
	if got := errorCode(validationErr); got != pb.ErrorCode_INVALID_PARAM {
		t.Fatalf("validation error classified as %s", got)
	}
}
