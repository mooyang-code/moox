package report

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const validDatasetHealthPolicy = `version: 2
realtime_timeseries:
  defaults:
    run_missed_intervals: 2
    success_missed_intervals: 3
    watermark_periods: 3
    minimum_watermark_lag: 10m
  overrides:
    - space_id: crypto
      dataset_id: spot_kline_1h
      freq: 1H
      watermark_lag: 5m
`

func writeDatasetHealthPolicy(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dataset-health-policy.yaml")
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadDatasetHealthPolicyValidatesAndHashes(t *testing.T) {
	cfg, err := LoadDatasetHealthPolicy(writeDatasetHealthPolicy(t, validDatasetHealthPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.RealtimeTimeSeries.Overrides) != 1 {
		t.Fatalf("overrides = %+v", cfg.RealtimeTimeSeries.Overrides)
	}
	if !strings.HasPrefix(cfg.Checksum, "sha256:") {
		t.Fatalf("checksum = %q", cfg.Checksum)
	}
}

func TestLoadDatasetHealthPolicyRejectsUnknownFieldsAndDocuments(t *testing.T) {
	tests := map[string]string{
		"unknown field":   validDatasetHealthPolicy + "unknown: true\n",
		"second document": validDatasetHealthPolicy + "---\nversion: 2\n",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadDatasetHealthPolicy(writeDatasetHealthPolicy(t, raw)); err == nil {
				t.Fatal("invalid Dataset health policy was accepted")
			}
		})
	}
}

func TestDefaultDatasetHealthPolicyDoesNotOverrideHourlyWatermarkBelowOnePeriod(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test path")
	}
	path := filepath.Join(
		filepath.Dir(currentFile),
		"..", "..", "examples", "setup", "default", "dataset-health-policy.yaml",
	)
	cfg, err := LoadDatasetHealthPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range cfg.RealtimeTimeSeries.Overrides {
		if override.SpaceID == "crypto" &&
			override.DatasetID == "spot_kline_1h" &&
			strings.EqualFold(override.Freq, "1h") {
			t.Fatalf("hourly Dataset must use the default multi-period watermark tolerance: %+v", override)
		}
	}
}

func TestValidateDatasetHealthEnvironment(t *testing.T) {
	path := writeDatasetHealthPolicy(t, validDatasetHealthPolicy)
	cfg, err := LoadDatasetHealthPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_DATASET_HEALTH_POLICY", path)
	t.Setenv("MOOX_DATASET_HEALTH_POLICY_HASH", cfg.Checksum)
	if _, err := ValidateDatasetHealthEnvironment(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MOOX_DATASET_HEALTH_POLICY_HASH", "sha256:wrong")
	if _, err := ValidateDatasetHealthEnvironment(); err == nil {
		t.Fatal("checksum mismatch was accepted")
	}
}
