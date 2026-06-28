package infraconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTempInfra(t *testing.T, base, local string) string {
	t.Helper()
	dir := t.TempDir()
	infraDir := filepath.Join(dir, "infra")
	if err := os.Mkdir(infraDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(infraDir, "infra.yaml"), []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if local != "" {
		if err := os.WriteFile(filepath.Join(infraDir, "infra.local.yaml"), []byte(local), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MOOX_INFRA_CONFIG", infraDir)
	return infraDir
}

func TestLoadBaseOnly(t *testing.T) {
	Reset()
	writeTempInfra(t, `
services:
  storage_access: { host: 127.0.0.1, port: 20201 }
  xdata:          { host: 127.0.0.1, port: 20201 }
  admin_gateway:  { host: 127.0.0.1, port: 11000 }
  web_host:       { host: 127.0.0.1, port: 10080 }
  trade:          { host: 127.0.0.1, port: 11200 }
remote: { host: "", ssh: "" }
`, "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Services.StorageAccess.URL(); got != "http://127.0.0.1:20201" {
		t.Fatalf("storage url=%s", got)
	}
	if got := StorageAccessURL(); got != "http://127.0.0.1:20201" {
		t.Fatalf("StorageAccessURL=%s", got)
	}
	if RemoteSSH() != "" {
		t.Fatalf("RemoteSSH=%q want empty", RemoteSSH())
	}
}

func TestLoadOverlayOverrides(t *testing.T) {
	Reset()
	writeTempInfra(t, `
services:
  storage_access: { host: 127.0.0.1, port: 20201 }
  xdata:          { host: 127.0.0.1, port: 20201 }
  admin_gateway:  { host: 127.0.0.1, port: 11000 }
  web_host:       { host: 127.0.0.1, port: 10080 }
  trade:          { host: 127.0.0.1, port: 11200 }
remote: { host: "", ssh: "" }
`, `
services:
  storage_access: { host: 10.0.0.2, port: 20202 }
  admin_gateway:  { host: 10.0.0.3, port: 11000 }
remote: { host: deploy.example, ssh: ubuntu@deploy.example }
`)
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Services.StorageAccess.URL(); got != "http://10.0.0.2:20202" {
		t.Fatalf("overlay storage url=%s", got)
	}
	if got := c.Services.XData.URL(); got != "http://127.0.0.1:20201" {
		t.Fatalf("xdata should remain base, got %s", got)
	}
	if got := AdminGatewayHost(); got != "10.0.0.3" {
		t.Fatalf("admin gateway host=%s", got)
	}
	if got := RemoteSSH(); got != "ubuntu@deploy.example" {
		t.Fatalf("RemoteSSH=%s", got)
	}
}

func TestLoadMissingInfra(t *testing.T) {
	Reset()
	t.Setenv("MOOX_INFRA_CONFIG", filepath.Join(t.TempDir(), "nope"))
	if _, err := Load(); err == nil {
		t.Fatal("want error for missing infra")
	}
}
