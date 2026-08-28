package bootstrap

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestFactorStorageHealthLatchesMalformedWrite(t *testing.T) {
	health := &factorStorageHealth{}
	if !health.Ready() {
		t.Fatal("new storage health must be ready")
	}

	health.ObserveStorageWriteFailure(errors.New("database is locked"))
	if !health.Ready() {
		t.Fatal("transient write failure must not mark storage malformed")
	}

	health.ObserveStorageWriteFailure(errors.New("persist factor output: database disk image is malformed (11)"))
	if health.Ready() {
		t.Fatal("malformed write must fail storage readiness")
	}
	message, at := health.Error()
	if !strings.Contains(message, "database disk image is malformed") {
		t.Fatalf("storage error = %q", message)
	}
	if at.IsZero() || time.Since(at) < 0 {
		t.Fatalf("storage error time = %v", at)
	}
}
