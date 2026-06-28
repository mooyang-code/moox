package infraconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempInfra 在临时目录写 infra.yaml(+可选 local)，并设置 MOOX_INFRA_CONFIG。
func writeTempInfra(t *testing.T, base, local string) string {
	t.Helper()
	dir := t.TempDir()
	infraDir := filepath.Join(dir, "infra")
	if err := os.MkdirAll(infraDir, 0o755); err != nil {
		t.Fatal(err)
	}
	basePath := filepath.Join(infraDir, "infra.yaml")
	if err := os.WriteFile(basePath, []byte(base), 0o644); err != nil {
		t.Fatal(err)
	}
	if local != "" {
		if err := os.WriteFile(filepath.Join(infraDir, "infra.local.yaml"), []byte(local), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("MOOX_INFRA_CONFIG", basePath)
	Reset()
	return basePath
}

func TestLoadBaseOnly(t *testing.T) {
	writeTempInfra(t, `
services:
  storage_access: { host: 127.0.0.1, port: 20201 }
  xdata:          { host: 127.0.0.1, port: 20201 }
  admin_gateway:  { host: 127.0.0.1, port: 11000 }
remote:
  host: "<deploy-host>"
  ssh:  "ubuntu@<deploy-host>"
`, "")
	if got := StorageAccessURL(); got != "http://127.0.0.1:20201" {
		t.Fatalf("StorageAccessURL=%s want http://127.0.0.1:20201", got)
	}
	if got := RemoteHost(); got != "<deploy-host>" {
		t.Fatalf("RemoteHost=%s want <deploy-host>", got)
	}
}

func TestLoadLocalOverlay(t *testing.T) {
	writeTempInfra(t, `
services:
  storage_access: { host: 127.0.0.1, port: 20201 }
  admin_gateway:  { host: 127.0.0.1, port: 11000 }
remote:
  host: "<deploy-host>"
  ssh:  "ubuntu@<deploy-host>"
`, `
services:
  storage_access: { host: 203.0.113.10, port: 20201 }
remote:
  host: 203.0.113.99
`)
	// base 默认被 local 覆盖（使用 RFC 5737 TEST-NET-3 保留地址，非真实 infra IP）
	if got := StorageAccessURL(); got != "http://203.0.113.10:20201" {
		t.Fatalf("StorageAccessURL=%s want http://203.0.113.10:20201", got)
	}
	if got := RemoteHost(); got != "203.0.113.99" {
		t.Fatalf("RemoteHost=%s want 203.0.113.99", got)
	}
	// 未覆盖的字段沿用 base
	if got := AdminGateway().Port; got != 11000 {
		t.Fatalf("AdminGateway.Port=%d want 11000", got)
	}
}

func TestEndpointURLAndHostPort(t *testing.T) {
	e := ServiceEndpoint{Host: "h", Port: 9}
	if e.URL() != "http://h:9" {
		t.Fatalf("URL=%s", e.URL())
	}
	if e.HostPort() != "h:9" {
		t.Fatalf("HostPort=%s", e.HostPort())
	}
}
