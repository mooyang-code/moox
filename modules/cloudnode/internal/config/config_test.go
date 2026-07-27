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

func TestValidateRejectsNonPositiveMaxDeliver(t *testing.T) {
	cfg := Default()
	cfg.JetStream.MaxDeliver = 0
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_deliver")
}

func TestValidateRejectsNonPositiveMaxAckPending(t *testing.T) {
	cfg := Default()
	cfg.JetStream.MaxAckPending = 0
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max_ack_pending")
}

func TestDefaultQueueConfigIsValid(t *testing.T) {
	cfg := Default()
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 4, cfg.JetStream.MaxDeliver)
	assert.Equal(t, 32, cfg.JetStream.MaxAckPending)
}

func TestLoadRejectsRemovedLeaseTiming(t *testing.T) {
	path := writeCloudnodeConfig(t, `
jetstream:
  ack_wait_millis: 120000
`)
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ack_wait_millis")
}

func TestLoadAppliesPprofAddrFromEnv(t *testing.T) {
	t.Setenv("MOOX_CLOUDNODE_PPROF_ADDR", "127.0.0.1:16001")

	cfg := Default()
	cfg.applyEnv()

	if cfg.Debug.PprofAddr != "127.0.0.1:16001" {
		t.Fatalf("PprofAddr = %q, want %q", cfg.Debug.PprofAddr, "127.0.0.1:16001")
	}
}

func TestEventBusURLFromEnvReplacesCheckedInEndpoint(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_NATS_URL", "tls://eventbus-a.example:4222, tls://eventbus-b.example:4222")
	cfg := Default()
	cfg.applyEnv()
	require.Equal(t, []string{"tls://eventbus-a.example:4222", "tls://eventbus-b.example:4222"}, cfg.JetStream.URLs)
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

func TestDefaultJobItemActiveKVAndHistoryConfig(t *testing.T) {
	cfg := Default()

	if cfg.JobItem.ActiveKVBucket != "MOOX_CLOUDNODE_JOB_ACTIVE" {
		t.Fatalf("ActiveKVBucket = %q, want %q", cfg.JobItem.ActiveKVBucket, "MOOX_CLOUDNODE_JOB_ACTIVE")
	}
	if cfg.JobItem.ActiveTTLHours != 48 {
		t.Fatalf("ActiveTTLHours = %d, want 48", cfg.JobItem.ActiveTTLHours)
	}
	if cfg.JobItem.HistoryDir != "../data/cloudnode/jobs" {
		t.Fatalf("HistoryDir = %q, want %q", cfg.JobItem.HistoryDir, "../data/cloudnode/jobs")
	}
	if cfg.JobItem.HistoryRetentionDays != 2 {
		t.Fatalf("HistoryRetentionDays = %d, want 2", cfg.JobItem.HistoryRetentionDays)
	}
}

func TestDefaultJetStreamCredentialPath(t *testing.T) {
	cfg := Default()
	if got := cfg.JetStream.CredentialFile; got != "~/.config/moox/eventbus/cloudnode-eventbus.yaml" {
		t.Fatalf("credential file = %q", got)
	}
}

func TestCheckedInConfigUsesCentralEventBusEndpoint(t *testing.T) {
	cfg, err := Load("../../config/app.yaml")
	require.NoError(t, err)
	require.Equal(t, []string{"nats://127.0.0.1:4222"}, cfg.JetStream.URLs)
}

func TestLoadRejectsRemovedJetStreamFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.yaml")
	require.NoError(t, os.WriteFile(path, []byte("jetstream:\n  nats_url: nats://127.0.0.1:4322\n"), 0o600))
	_, err := Load(path)
	require.ErrorContains(t, err, "nats_url")
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
