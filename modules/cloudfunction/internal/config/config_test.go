package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDefaultNodeBatchConfig(t *testing.T) {
	cfg := Default()
	assert.Equal(t, 3, cfg.NodeBatch.BatchSize)
	assert.Equal(t, 500*time.Millisecond, cfg.NodeBatch.PollInterval)
	require.NoError(t, cfg.Validate())
}

func TestValidateRejectsInvalidNodeBatchConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		field  string
	}{
		{name: "batch too small", mutate: func(cfg *Config) { cfg.NodeBatch.BatchSize = 0 }, field: "node_batch.batch_size"},
		{name: "batch too large", mutate: func(cfg *Config) { cfg.NodeBatch.BatchSize = 11 }, field: "node_batch.batch_size"},
		{name: "poll too fast", mutate: func(cfg *Config) { cfg.NodeBatch.PollInterval = 99 * time.Millisecond }, field: "node_batch.poll_interval"},
		{name: "poll too slow", mutate: func(cfg *Config) { cfg.NodeBatch.PollInterval = 11 * time.Second }, field: "node_batch.poll_interval"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			require.ErrorContains(t, cfg.Validate(), tt.field)
		})
	}
}

func TestLoadAppliesPprofAddrFromEnv(t *testing.T) {
	t.Setenv("MOOX_CLOUDNODE_PPROF_ADDR", "127.0.0.1:16001")

	cfg := Default()
	cfg.applyEnv()

	if cfg.Debug.PprofAddr != "127.0.0.1:16001" {
		t.Fatalf("PprofAddr = %q, want %q", cfg.Debug.PprofAddr, "127.0.0.1:16001")
	}
}

func TestDefaultHealthConfigAndEnvOverride(t *testing.T) {
	t.Setenv("MOOX_CLOUDNODE_HEALTH_ADDR", "127.0.0.1:16011")

	cfg := Default()
	if cfg.Health.Addr != ":11411" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, ":11411")
	}

	cfg.applyEnv()
	if cfg.Health.Addr != "127.0.0.1:16011" {
		t.Fatalf("Health.Addr = %q, want %q", cfg.Health.Addr, "127.0.0.1:16011")
	}
}

func TestLoadReadsYAMLAndAppliesEnvOverrides(t *testing.T) {
	t.Setenv("MOOX_CLOUDNODE_DB_PATH", "./override/cloudnode.db")
	t.Setenv("MOOX_CLOUDNODE_HEALTH_ADDR", "127.0.0.1:16011")

	path := writeCloudnodeConfig(t, `
database:
  path: ./original/cloudnode.db
health:
  addr: :9999
`)

	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, "./override/cloudnode.db", cfg.Database.Path)
	assert.Equal(t, "127.0.0.1:16011", cfg.Health.Addr)
}

func TestLoadRejectsMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read config")
}

func TestLoadRejectsInvalidYAML(t *testing.T) {
	path := writeCloudnodeConfig(t, "database:\n  max_idle_conns: not-a-number\n")
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse config")
}

func writeCloudnodeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func TestGatewayIdentityAndStrictConfiguration(t *testing.T) {
	for _, body := range []string{
		"gateway_client:\n  caller: admin\n",
		"gateway_client:\n  caller: cloudnode\n  caller: admin\n",
		"gateway_client:\n  unknown: true\n",
		"{}\n---\n{}\n",
		"service_auth:\n  caller: old\n",
	} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "app.yaml")
			require.NoError(t, os.WriteFile(path, []byte(body), 0600))
			_, err := Load(path)
			require.Error(t, err)
		})
	}
	cfg := Default()
	_, err := cfg.OpenGateway(nil)
	require.Error(t, err)
}

func TestTencentSCFAPIRateLimitsFromYAMLAndValidation(t *testing.T) {
	path := writeCloudnodeConfig(t, `
tencent_scf:
  api_rate_limits:
    ListTriggers: 10
    UpdateTrigger: 12.5
`)
	cfg, err := Load(path)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"ListTriggers": 10, "UpdateTrigger": 12.5}, cfg.TencentSCF.APIRateLimits)

	// YAML 的 .nan 能解析成 NaN，与 0 或负数的比较都为假，必须在加载时被拒绝。
	_, err = Load(writeCloudnodeConfig(t, "tencent_scf:\n  api_rate_limits:\n    ListTriggers: .nan\n"))
	require.ErrorContains(t, err, "tencent_scf.api_rate_limits")

	assert.Empty(t, Default().TencentSCF.APIRateLimits, "默认不覆盖，使用官方限频的 75%")
	require.NoError(t, Default().Validate())

	for name, limits := range map[string]map[string]float64{
		"unknown action":         {"NoSuchAction": 5},
		"zero":                   {"ListTriggers": 0},
		"negative":               {"ListTriggers": -3},
		"above the official cap": {"ListTriggers": 21},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := Default()
			cfg.TencentSCF.APIRateLimits = limits
			require.ErrorContains(t, cfg.Validate(), "tencent_scf.api_rate_limits")
		})
	}
}
