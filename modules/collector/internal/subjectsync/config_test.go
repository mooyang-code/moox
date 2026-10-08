package subjectsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testGatewayClientYAML = "gateway_client:\n  mode: local\n  caller: collector\n  key_file: caller-collector.key\n  ca_file: moox-ca.crt\n  cache_dir: ./data/gatewayclient\n"

func writeSubjectConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "subject.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(writeSubjectConfig(t, testGatewayClientYAML+"attributes:\n  - space_id: crypto\n    sources: [binance]\n    cron: \"0 0 * * *\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != time.Minute || cfg.FetchTimeout != 2*time.Minute || cfg.Attributes[0].Timezone != "UTC" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.HealthAddr != "127.0.0.1:11413" {
		t.Fatalf("health addr = %q", cfg.HealthAddr)
	}
	if cfg.GatewayClient.Caller != "collector" {
		t.Fatalf("gateway client = %+v", cfg.GatewayClient)
	}
}

func TestLoadConfigRejectsBadCron(t *testing.T) {
	if _, err := LoadConfig(writeSubjectConfig(t, testGatewayClientYAML+"attributes: [{space_id: crypto, sources: [binance], cron: 'bad'}]\n")); err == nil {
		t.Fatal("want error")
	}
}

func TestLoadConfigRequiresGatewayClient(t *testing.T) {
	_, err := LoadConfig(writeSubjectConfig(t, "attributes: []\n"))
	if err == nil || !strings.Contains(err.Error(), "gateway_client") {
		t.Fatalf("缺少 gateway_client 应当报错: %v", err)
	}
}
