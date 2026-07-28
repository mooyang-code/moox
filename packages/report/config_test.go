package report

import (
	"strings"
	"testing"
)

func TestDefaultConfigRequiresExplicitRuntimeIdentity(t *testing.T) {
	t.Setenv("HOSTNAME", "unstable-container-name")
	t.Setenv("MOOX_INSTANCE_ID", "")
	t.Setenv("MOOX_NODE_ID", "")
	t.Setenv("MOOX_BOOT_ID", "")

	_, err := NewHandler(DefaultConfig("test", "moox_test"))
	if err == nil || !strings.Contains(err.Error(), "metrics reporter identity requires MOOX_") {
		t.Fatalf("NewHandler() error = %v, want explicit identity failure", err)
	}
}

func TestDefaultConfigAcceptsExplicitLocalIdentity(t *testing.T) {
	t.Setenv("MOOX_INSTANCE_ID", "moox_test@local")
	t.Setenv("MOOX_NODE_ID", "local")
	t.Setenv("MOOX_BOOT_ID", "boot-test")

	if _, err := NewHandler(DefaultConfig("test", "moox_test")); err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
}
