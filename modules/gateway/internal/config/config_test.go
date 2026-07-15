package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAppliesDefaultsAndValidatesRequiredConfiguration(t *testing.T) {
	dir := t.TempDir()
	control := writeKey(t, dir, "control.key", 0o600)
	service := writeKey(t, dir, "service.key", 0o600)
	path := filepath.Join(dir, "app.yaml")
	yaml := validYAML(control, service, filepath.Join(dir, "data"), "")
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.ControlPlane.RefreshInterval != 15*time.Second {
		t.Fatalf("refresh = %v", cfg.ControlPlane.RefreshInterval)
	}
	if cfg.Server.ServiceAddr != "127.0.0.1:11002" || cfg.Server.HealthAddr != "127.0.0.1:11012" {
		t.Fatalf("addresses = %+v", cfg.Server)
	}

	for name, mutate := range map[string]func(string) string{
		"node ID": func(value string) string { return strings.Replace(value, "id: gateway-test", "id:", 1) },
		"Admin URL": func(value string) string {
			return strings.Replace(value, "base_url: https://admin.example", "base_url:", 1)
		},
		"control key": func(value string) string {
			return strings.Replace(value, "hmac_key_file: "+control, "hmac_key_file:", 1)
		},
		"service key": func(value string) string {
			return strings.Replace(value, "hmac_key_file: "+service, "hmac_key_file:", 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			broken := mutate(yaml)
			if err := os.WriteFile(path, []byte(broken), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() succeeded with missing required value")
			}
		})
	}
}

func TestLoadRejectsWildcardListenersAndInsecureKeys(t *testing.T) {
	dir := t.TempDir()
	control := writeKey(t, dir, "control.key", 0o600)
	service := writeKey(t, dir, "service.key", 0o600)
	insecure := writeKey(t, dir, "insecure.key", 0o640)
	path := filepath.Join(dir, "app.yaml")
	for name, extra := range map[string]string{
		"wildcard service":   "service_addr: 0.0.0.0:11002",
		"wildcard health":    "health_addr: :11012",
		"wrong service port": "service_addr: 127.0.0.1:12002",
		"insecure key":       "control_key: " + insecure,
	} {
		t.Run(name, func(t *testing.T) {
			controlKey := control
			if strings.HasPrefix(extra, "control_key:") {
				controlKey = strings.TrimSpace(strings.TrimPrefix(extra, "control_key:"))
				extra = ""
			}
			yaml := validYAML(controlKey, service, filepath.Join(dir, "data"), extra)
			if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load() succeeded")
			}
		})
	}
}

func TestLoadRejectsNonLoopbackPlaintextControlPlane(t *testing.T) {
	dir := t.TempDir()
	control := writeKey(t, dir, "control.key", 0o600)
	service := writeKey(t, dir, "service.key", 0o600)
	path := filepath.Join(dir, "app.yaml")
	yaml := strings.Replace(validYAML(control, service, filepath.Join(dir, "data"), ""), "https://admin.example", "http://admin.example", 1)
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load() accepted non-loopback plaintext Admin URL")
	}
}

func writeKey(t *testing.T, dir, name string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("secret-value\n"), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func validYAML(control, service, store, override string) string {
	serviceAddr, healthAddr := "127.0.0.1:11002", "127.0.0.1:11012"
	if strings.HasPrefix(override, "service_addr:") {
		serviceAddr = strings.TrimSpace(strings.TrimPrefix(override, "service_addr:"))
	}
	if strings.HasPrefix(override, "health_addr:") {
		healthAddr = strings.TrimSpace(strings.TrimPrefix(override, "health_addr:"))
	}
	return "node:\n  id: gateway-test\nserver:\n  service_addr: " + serviceAddr + "\n  health_addr: " + healthAddr + "\ncontrol_plane:\n  base_url: https://admin.example\n  hmac_key_file: " + control + "\nauth:\n  hmac_key_file: " + service + "\nstore:\n  path: " + store + "\nproxy:\n  max_body_bytes: 4194304\n"
}
