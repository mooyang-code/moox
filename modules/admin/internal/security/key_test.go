package security

import "testing"

func TestGetEncryptionKey_DefaultAndEnvironment(t *testing.T) {
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "")
	if _, err := GetEncryptionKey(); err == nil {
		t.Fatal("missing key was accepted")
	}
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY", "custom-admin-key")
	got, err := GetEncryptionKey()
	if err != nil || got != "custom-admin-key" {
		t.Fatalf("environment key=%q", got)
	}
}
