package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/gateway/internal/store"
	"github.com/mooyang-code/moox/packages/gatewayproxy"
)

func TestCLICommandsAndFailureModes(t *testing.T) {
	configPath, storePath := cliConfig(t)
	if code := run([]string{"check-config", "--config", configPath}, os.Stdout); code != 0 {
		t.Fatalf("check-config exit = %d", code)
	}

	snapshot, _ := gatewayproxy.NormalizeAndHash("gateway-test", []gatewayproxy.Route{{ServiceID: "monitor", Address: "127.0.0.1:1234", ServicePath: "trpc.moox.monitor.Monitor", AllowedMethods: []string{"*"}, AllowedCallers: []string{"*"}}})
	if err := store.NewRoutes(storePath).Save(snapshot); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"routes", "--config", configPath}, os.Stdout); code != 0 {
		t.Fatalf("routes exit = %d", code)
	}

	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer ready.Close()
	if code := run([]string{"health", "--url", ready.URL}, os.Stdout); code != 0 {
		t.Fatalf("health exit = %d", code)
	}
	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusServiceUnavailable) }))
	defer notReady.Close()
	if code := run([]string{"health", "--url", notReady.URL}, os.Stdout); code == 0 {
		t.Fatal("non-ready health succeeded")
	}

	if err := os.WriteFile(filepath.Join(storePath, "routes.json"), []byte(`{"node_id":"gateway-test","route_hash":"bad"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"routes", "--config", configPath}, os.Stdout); code == 0 {
		t.Fatal("hash mismatch succeeded")
	}
	if code := run([]string{"check-config", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, os.Stdout); code == 0 {
		t.Fatal("invalid config succeeded")
	}
	if code := run([]string{"routes", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, os.Stdout); code == 0 {
		t.Fatal("unreadable cache/config succeeded")
	}
}

func cliConfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	control, service := filepath.Join(dir, "control.key"), filepath.Join(dir, "service.key")
	for _, path := range []string{control, service} {
		if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	storePath := filepath.Join(dir, "data")
	path := filepath.Join(dir, "app.yaml")
	contents := "node:\n  id: gateway-test\nserver:\n  service_addr: 127.0.0.1:11002\n  health_addr: 127.0.0.1:11012\ncontrol_plane:\n  base_url: https://admin.example.com\n  hmac_key_file: " + control + "\nauth:\n  hmac_key_file: " + service + "\n  caller: service\nstore:\n  path: " + storePath + "\nproxy:\n  max_body_bytes: 4194304\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, storePath
}
