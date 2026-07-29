package report

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestLoadPipelineAllowlistValidatesAndHashes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipelines.yaml")
	raw := "version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n  overrides:\n    - space_id: crypto\n      dataset_id: market_kline\n      freq: 1m\n      canary_subject_id: BTC-USDT\n      watermark_lag: 5m\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPipelineAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RealtimeTimeSeries.Overrides) != 1 || cfg.Checksum == "" {
		t.Fatalf("config = %+v", cfg)
	}
}

func TestPipelineAllowlistAcceptsCanonicalStorageFrequency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pipelines.yaml")
	raw := "version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n  overrides:\n    - space_id: crypto\n      dataset_id: market_kline\n      freq: 1H\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPipelineAllowlist(path); err != nil {
		t.Fatalf("canonical Storage frequency rejected: %v", err)
	}
}

func TestPipelineAllowlistRejectsInvalidDefinitions(t *testing.T) {
	for _, raw := range []string{
		"version: 1\npipelines: []\n",
		"version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 0\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n",
		"version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n  overrides:\n    - space_id: s\n      dataset_id: d\n      freq: bad\n",
		"version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n  overrides:\n    - space_id: s\n      dataset_id: d\n      freq: 1h\n    - space_id: s\n      dataset_id: d\n      freq: 1H\n",
	} {
		path := filepath.Join(t.TempDir(), "pipelines.yaml")
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadPipelineAllowlist(path); err == nil {
			t.Fatalf("invalid config accepted: %s", raw)
		}
	}
}

func TestRealV2ConfigEnablesBuiltInModuleMetrics(t *testing.T) {
	cfg, err := LoadPipelineAllowlist(filepath.Join("..", "..", "examples", "monitor-pipelines.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		module, pipeline, stage string
		freshness               bool
		watermark               bool
	}{
		{"archive", "archive-materialize", "materialize", false, false},
		{"cloudnode", "cloudnode-jobs", "dispatch", false, false},
		{"collector", "collector-market-data", "collect", false, false},
		{"factor", "factor-calculation", "calculate", false, false},
		{"monitor", "monitor-metrics", "ingest", true, true},
		{"strategy", "strategy-targets", "target_commit", false, false},
		{"trade", "trade-rebalance", "rebalance", false, false},
	}
	if len(cfg.Pipelines) != len(cases) {
		t.Fatalf("built-in pipelines = %+v", cfg.Pipelines)
	}
	now := time.Now().UTC()
	for _, test := range cases {
		t.Run(test.module, func(t *testing.T) {
			ids := cfg.IDsForModule(test.module)
			if len(ids) != 1 || ids[0] != test.pipeline {
				t.Fatalf("pipeline IDs = %v", ids)
			}
			registry := prometheus.NewRegistry()
			metrics, err := NewModuleMetrics(registry, test.module, ids)
			if err != nil {
				t.Fatal(err)
			}
			if err := metrics.ObserveRun(test.stage, "success", test.pipeline, now); err != nil {
				t.Fatal(err)
			}
			if test.watermark {
				if err := metrics.AdvanceInputWatermark(test.stage, test.pipeline, now); err != nil {
					t.Fatal(err)
				}
				if err := metrics.AdvanceWatermark(test.stage, test.pipeline, now); err != nil {
					t.Fatal(err)
				}
			}
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			requireMetricFamily(t, families, ModuleMetricName(test.module, ModuleMetricRuns))
			if test.watermark {
				requireMetricFamily(t, families, ModuleMetricName(test.module, ModuleMetricBusinessWatermark))
			}
			found := false
			for _, pipeline := range cfg.Pipelines {
				if pipeline.ID == test.pipeline {
					found = true
					if pipeline.FreshnessMonitoring != test.freshness {
						t.Fatalf("freshness monitoring = %v", pipeline.FreshnessMonitoring)
					}
					if pipeline.WatermarkMonitoring != test.watermark {
						t.Fatalf("watermark monitoring = %v", pipeline.WatermarkMonitoring)
					}
				}
			}
			if !found {
				t.Fatalf("pipeline %q not found", test.pipeline)
			}
		})
	}
}

func TestPipelineEnvironmentWithoutFileStillUsesBuiltInRegistry(t *testing.T) {
	t.Setenv("MOOX_PIPELINE_CONFIG", "")
	t.Setenv("MOOX_PIPELINE_CONFIG_HASH", "")
	cfg, err := ValidatePipelineEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.IDsForModule("monitor"); len(got) != 1 || got[0] != "monitor-metrics" {
		t.Fatalf("monitor pipelines = %v", got)
	}
}
