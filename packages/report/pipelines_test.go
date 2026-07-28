package report

import (
	"os"
	"path/filepath"
	"testing"
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
