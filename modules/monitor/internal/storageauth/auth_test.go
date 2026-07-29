package storageauth

import (
	"testing"

	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

func TestPrimaryUsesRuntimeSecret(t *testing.T) {
	t.Setenv(primarySecretEnv, "primary-secret")
	auth := Primary(" monitor ")
	if auth.GetAppId() != "monitor" {
		t.Fatalf("app id = %q", auth.GetAppId())
	}
	want := mooxsecurity.HMACSHA256Hex("primary-secret", []byte("monitor"))
	if auth.GetAppKey() != want {
		t.Fatalf("app key = %q, want derived key", auth.GetAppKey())
	}
}
