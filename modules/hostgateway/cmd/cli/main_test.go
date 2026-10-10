package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/hostgateway/internal/store"
	"github.com/mooyang-code/moox/modules/hostgateway/internal/testsnapshot"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/healthz"
)

func cliConfig(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	storePath := filepath.Join(dir, "data")
	if err := os.Mkdir(storePath, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.yaml")
	content := `host:
  id: storage
tls:
  cert_file: server.crt
  key_file: server.key
  ca_file: ca.crt
control:
  target: 106.53.107.122:11003
  caller: host-gateway@storage
  key_file: caller-host-gateway.key
store:
  path: ` + storePath + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, storePath
}

func TestCLICommandsAndFailureModes(t *testing.T) {
	t.Setenv("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1")
	t.Setenv("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor")
	t.Setenv("MOOX_HEALTH_AUTH_SECRET_KEY", "health-secret")
	configPath, storePath := cliConfig(t)
	var output bytes.Buffer

	built, err := testsnapshot.Build("storage", false, []gatewayroute.Route{{
		ServiceID: "storage-view", Address: "127.0.0.1:20103", ServicePath: "trpc.moox.storage.DataView",
		AllowedMethods: []string{"QueryTimeSeriesRows"}, AllowedCallers: []string{"strategy"},
	}}, []gatewayroute.VerificationKey{{KeyID: "strategy-1", Caller: "strategy", Secret: "top-secret"}}, testsnapshot.Directory("storage", "trpc.moox.storage.DataView"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.NewSnapshots(storePath).Save(built); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if code := run([]string{"snapshot", "--config", configPath}, &output); code != 0 {
		t.Fatalf("snapshot exit = %d: %s", code, output.String())
	}
	if !strings.Contains(output.String(), "trpc.moox.storage.DataView") || !strings.Contains(output.String(), "strategy/strategy-1") {
		t.Fatalf("snapshot 输出不完整: %s", output.String())
	}
	if strings.Contains(output.String(), "top-secret") {
		t.Fatal("snapshot 不能输出校验密钥")
	}

	authenticator, err := healthz.NewAuthenticator(healthz.AuthConfig{Version: "moox-health-v1", AccessKey: "monitor", SecretKey: "health-secret"})
	if err != nil {
		t.Fatal(err)
	}
	ready := httptest.NewServer(authenticator.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })))
	defer ready.Close()
	if code := run([]string{"health", "--url", ready.URL + "/readyz"}, &output); code != 0 {
		t.Fatalf("health exit = %d", code)
	}
	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "no", http.StatusServiceUnavailable) }))
	defer notReady.Close()
	if code := run([]string{"health", "--url", notReady.URL + "/readyz"}, &output); code == 0 {
		t.Fatal("未就绪时 health 应当失败")
	}

	built.Hash = "tampered"
	if err := store.NewSnapshots(storePath).Save(built); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"snapshot", "--config", configPath}, &output); code == 0 {
		t.Fatal("哈希不一致的缓存应当报错")
	}
	if code := run([]string{"check-config", "--config", filepath.Join(t.TempDir(), "missing.yaml")}, &output); code == 0 {
		t.Fatal("缺少配置文件应当报错")
	}
	if code := run([]string{"routes"}, &output); code == 0 {
		t.Fatal("旧命令 routes 应当不再可用")
	}
}
