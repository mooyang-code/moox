package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateRejectsAckWaitShorterThanRecoveryWindow(t *testing.T) {
	cfg := Default()
	cfg.JetStream.AckWaitMillis = int64(2 * time.Minute / time.Millisecond)
	cfg.JobItem.RecoverAfterMillis = int64(10 * time.Minute / time.Millisecond)

	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ack_wait_millis")
	assert.Contains(t, err.Error(), "recover_after_millis")
}

func TestDefaultLeaseTimingIsValid(t *testing.T) {
	cfg := Default()

	require.NoError(t, cfg.Validate())
	assert.Equal(t, int64(12*time.Minute/time.Millisecond), cfg.JetStream.AckWaitMillis)
}

func TestLoadRejectsInvalidLeaseTiming(t *testing.T) {
	path := writeCloudnodeConfig(t, `
jetstream:
  ack_wait_millis: 120000
job_item:
  recover_after_millis: 600000
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
