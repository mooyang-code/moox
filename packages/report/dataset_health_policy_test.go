package report

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const validDatasetHealthPolicy = `version: 2
realtime_timeseries:
  defaults:
    run_missed_intervals: 2
    success_missed_intervals: 3
    minute_lateness: 30s
    hour_lateness: 1m
  overrides:
    - space_id: crypto
      dataset_id: dataset_spot_kline_1h
      freq: 1H
      lateness: 5m
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

// 仓库默认策略：分钟级允许迟到 30 秒、小时级 1 分钟，日线及以上 1 个周期；按数据集的覆盖优先。
func TestDatasetHealthPolicyLatenessByFrequency(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test path")
	}
	path := filepath.Join(filepath.Dir(currentFile), "..", "..", "config", "setup", "dataset-health-policy.yaml")
	cfg, err := LoadDatasetHealthPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	policy := cfg.RealtimeTimeSeries
	for freq, want := range map[string]time.Duration{
		"30s": 30 * time.Second, "1m": 30 * time.Second, "5m": 30 * time.Second, "30m": 30 * time.Second,
		"1h": time.Minute, "1H": time.Minute, "4h": time.Minute,
		"1d": 24 * time.Hour, "1w": 7 * 24 * time.Hour, "bad": 0,
	} {
		if got := policy.Lateness("crypto", "dataset_x", freq); got != want {
			t.Fatalf("lateness(%s) = %s, want %s", freq, got, want)
		}
	}
	policy.Overrides = []RealtimeTimeSeriesOverride{{SpaceID: "crypto", DatasetID: "dataset_x", Freq: "1H", Lateness: 5 * time.Minute}}
	if got := policy.Lateness("crypto", "dataset_x", "1h"); got != 5*time.Minute {
		t.Fatalf("override lateness = %s", got)
	}
	if got := policy.Lateness("crypto", "dataset_y", "1h"); got != time.Minute {
		t.Fatalf("another dataset must keep the default: %s", got)
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
