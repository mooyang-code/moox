package config

import "testing"

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
