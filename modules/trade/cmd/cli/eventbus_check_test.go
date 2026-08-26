package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

func TestRunEventBusCheckCommandRequiresAppConfig(t *testing.T) {
	var stderr bytes.Buffer
	err := runEventBusCheckCommand([]string{"eventbus-check", "--config", "/does/not/exist/app.yaml"}, nil, &stderr)
	if err == nil || !strings.Contains(err.Error(), "read app config") {
		t.Fatalf("err=%v, stderr=%s", err, stderr.String())
	}
}

func TestIsEventBusCheckCommandOnlyDetectsSubcommand(t *testing.T) {
	if !isEventBusCheckCommand([]string{"moox-trade-cli", "eventbus-check"}) {
		t.Fatal("expected eventbus-check to be detected")
	}
	if isEventBusCheckCommand([]string{"moox-trade-cli", "init"}) {
		t.Fatal("init must not be detected as eventbus-check")
	}
}

func TestRunEventBusCheckCommandConnectsWithConfiguredCredential(t *testing.T) {
	natsServer, err := server.NewServer(&server.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoLog:  true,
		NoSigs: true,
		Users:  []*server.User{{Username: "trade-eventbus", Password: "secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	go natsServer.Start()
	defer natsServer.Shutdown()
	if !natsServer.ReadyForConnections(5 * time.Second) {
		t.Fatal("embedded NATS did not become ready")
	}

	dir := t.TempDir()
	credentialPath := filepath.Join(dir, "trade-eventbus.yaml")
	if err := os.WriteFile(credentialPath, []byte(fmt.Sprintf("version: 1\nurls:\n  - %s\nusername: trade-eventbus\ntoken: secret\n", natsServer.ClientURL())), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "app.yaml")
	config := fmt.Sprintf("eventbus:\n  enabled: true\n  urls: [%q]\n  credential_file: %q\n", natsServer.ClientURL(), credentialPath)
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := runEventBusCheckCommand([]string{"eventbus-check", "--config", configPath}, &stdout, nil); err != nil {
		t.Fatalf("runEventBusCheckCommand() error = %v", err)
	}
	var result eventBusCheckResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || !result.Connected || result.URL != natsServer.ClientURL() {
		t.Fatalf("result=%+v", result)
	}
}
