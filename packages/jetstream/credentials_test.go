package jetstream

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExpandCredentialPathExpandsEnvironment(t *testing.T) {
	t.Setenv("MOOX_CREDENTIAL_ROOT", "/tmp/moox-credentials")
	if got := ExpandCredentialPath("$MOOX_CREDENTIAL_ROOT/monitor.yaml"); got != "/tmp/moox-credentials/monitor.yaml" {
		t.Fatalf("ExpandCredentialPath() = %q", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got := ExpandCredentialPath("~/monitor.yaml"); got != filepath.Join(home, "monitor.yaml") {
		t.Fatalf("home expansion = %q", got)
	}
}

func TestLoadCredentialFileRequiresPrivateRegularFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eventbus.yaml")
	if err := os.WriteFile(path, []byte("username: monitor\ntoken: secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCredentialFile(path); err == nil {
		t.Fatal("world-readable credential file accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := LoadCredentialFile(path)
	if err != nil || file.Username != "monitor" || file.Password != "secret" {
		t.Fatalf("LoadCredentialFile() = %+v, err=%v", file, err)
	}
}
