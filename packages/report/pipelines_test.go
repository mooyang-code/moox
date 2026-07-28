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

func TestPipelineAllowlistRejectsInvalidDefinitions(t *testing.T) {
	for _, raw := range []string{
		"version: 1\npipelines: []\n",
		"version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 0\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n",
		"version: 2\nrealtime_timeseries:\n  defaults:\n    run_missed_intervals: 2\n    success_missed_intervals: 3\n    watermark_periods: 3\n    minimum_watermark_lag: 10m\n  overrides:\n    - space_id: s\n      dataset_id: d\n      freq: bad\n",
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
	}{
		{"archive", "archive-materialize", "materialize"},
		{"cloudnode", "cloudnode-jobs", "dispatch"},
		{"collector", "collector-market-data", "collect"},
		{"factor", "factor-calculation", "calculate"},
		{"monitor", "monitor-metrics", "ingest"},
		{"strategy", "strategy-targets", "target_commit"},
		{"trade", "trade-rebalance", "rebalance"},
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
			if err := metrics.AdvanceWatermark(test.stage, test.pipeline, now); err != nil {
				t.Fatal(err)
			}
			families, err := registry.Gather()
			if err != nil {
				t.Fatal(err)
			}
			requireMetricFamily(t, families, "moox_"+test.module+"_runs_total")
			requireMetricFamily(t, families, "moox_"+test.module+"_business_watermark_timestamp_seconds")
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
