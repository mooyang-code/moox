package runtime

import (
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaultJobWorkerConfig(t *testing.T) {
	cfg := DefaultConfig().JobWorker
	if cfg == nil {
		t.Fatal("default job_worker config is nil")
	}
	if cfg.BatchSize != 10 || cfg.Timeout != 20*time.Second {
		t.Fatalf("default job_worker = %+v", cfg)
	}
	want := []string{"collect.binance.kline", "collect.binance.symbol"}
	if len(cfg.JobTypes) != len(want) || cfg.JobTypes[0] != want[0] || cfg.JobTypes[1] != want[1] {
		t.Fatalf("default job types = %v, want %v", cfg.JobTypes, want)
	}
}

func TestJobWorkerConfigParsesDuration(t *testing.T) {
	cfg := DefaultConfig()
	err := yaml.Unmarshal([]byte(`
job_worker:
  batch_size: 10
  timeout: 20s
  job_types: [collect.binance.kline]
`), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.JobWorker.Timeout != 20*time.Second {
		t.Fatalf("timeout = %v, want 20s", cfg.JobWorker.Timeout)
	}
}

func TestGetJobWorkerConfigNormalizesEnvironmentOverride(t *testing.T) {
	resetLocalAppConfigForTest(t, &AppConfig{JobWorker: &JobWorkerConfig{
		BatchSize: 4,
		Timeout:   12 * time.Second,
		JobTypes:  []string{"collect.binance.kline"},
	}})
	t.Setenv(
		"MOOX_COLLECTOR_JOB_TYPES",
		" collect.binance.symbol,collect.binance.kline,collect.binance.symbol ",
	)

	cfg, err := GetJobWorkerConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BatchSize != 4 || cfg.Timeout != 12*time.Second {
		t.Fatalf("job worker = %+v", cfg)
	}
	want := []string{"collect.binance.symbol", "collect.binance.kline"}
	if len(cfg.JobTypes) != len(want) || cfg.JobTypes[0] != want[0] || cfg.JobTypes[1] != want[1] {
		t.Fatalf("job types = %v, want %v", cfg.JobTypes, want)
	}
}

func TestGetJobWorkerConfigRejectsInvalidValues(t *testing.T) {
	for _, test := range []struct {
		name string
		cfg  JobWorkerConfig
	}{
		{name: "batch size", cfg: JobWorkerConfig{Timeout: time.Second, JobTypes: []string{"collect.binance.kline"}}},
		{name: "timeout", cfg: JobWorkerConfig{BatchSize: 1, JobTypes: []string{"collect.binance.kline"}}},
		{name: "job types", cfg: JobWorkerConfig{BatchSize: 1, Timeout: time.Second}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetLocalAppConfigForTest(t, &AppConfig{JobWorker: &test.cfg})
			_, err := GetJobWorkerConfig()
			if err == nil {
				t.Fatal("GetJobWorkerConfig() accepted invalid config")
			}
		})
	}
}

func resetLocalAppConfigForTest(t *testing.T, cfg *AppConfig) {
	t.Helper()
	localAppConfigMu.Lock()
	old := LocalAppConfig
	LocalAppConfig = cfg
	localAppConfigMu.Unlock()
	t.Cleanup(func() {
		localAppConfigMu.Lock()
		LocalAppConfig = old
		localAppConfigMu.Unlock()
	})
}
