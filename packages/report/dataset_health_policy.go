package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/frequency"
	"gopkg.in/yaml.v3"
)

const maxDatasetHealthPolicyBytes = 1 << 20

type DatasetHealthPolicy struct {
	Version            int                      `yaml:"version" json:"version"`
	RealtimeTimeSeries RealtimeTimeSeriesPolicy `yaml:"realtime_timeseries" json:"realtime_timeseries"`
	Checksum           string                   `yaml:"-" json:"checksum"`
}

type RealtimeTimeSeriesPolicy struct {
	Defaults  RealtimeTimeSeriesDefaults   `yaml:"defaults" json:"defaults"`
	Overrides []RealtimeTimeSeriesOverride `yaml:"overrides" json:"overrides"`
}

type RealtimeTimeSeriesDefaults struct {
	RunMissedIntervals     int `yaml:"run_missed_intervals" json:"run_missed_intervals"`
	SuccessMissedIntervals int `yaml:"success_missed_intervals" json:"success_missed_intervals"`
	// MinuteLateness 与 HourLateness 是应收的最新一根 K 线收线后允许迟到多久：分钟级（短于 1 小时的周期）与
	// 小时级（1h、4h）；日线及以上允许迟到 1 个周期。超过仍没到，数据集判为过期并告警。
	MinuteLateness time.Duration `yaml:"minute_lateness" json:"minute_lateness"`
	HourLateness   time.Duration `yaml:"hour_lateness" json:"hour_lateness"`
}

type RealtimeTimeSeriesOverride struct {
	SpaceID   string `yaml:"space_id" json:"space_id"`
	DatasetID string `yaml:"dataset_id" json:"dataset_id"`
	Freq      string `yaml:"freq" json:"freq"`
	// Lateness 覆盖该数据集最新一根 K 线收线后允许迟到的时间。
	Lateness time.Duration `yaml:"lateness,omitempty" json:"lateness,omitempty"`
}

// Lateness 返回数据集最新一根 K 线收线后允许迟到多久：有按数据集的覆盖就用覆盖，否则分钟级用 MinuteLateness、
// 小时级用 HourLateness，日线及以上允许迟到 1 个周期（月按 30 天计）。周期无法识别时返回 0（不判断迟到）。
func (p RealtimeTimeSeriesPolicy) Lateness(spaceID, datasetID, freq string) time.Duration {
	parsed, err := frequency.Parse(freq)
	if err != nil {
		return 0
	}
	for _, override := range p.Overrides {
		overrideFreq, err := frequency.Parse(override.Freq)
		if err == nil && override.SpaceID == spaceID && override.DatasetID == datasetID && overrideFreq == parsed && override.Lateness > 0 {
			return override.Lateness
		}
	}
	switch period := parsed.NominalDuration(); {
	case period < time.Hour:
		return p.Defaults.MinuteLateness
	case period < 24*time.Hour:
		return p.Defaults.HourLateness
	default:
		return period
	}
}

func LoadDatasetHealthPolicy(path string) (DatasetHealthPolicy, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return DatasetHealthPolicy{}, fmt.Errorf("read Dataset health policy: %w", err)
	}
	if len(raw) > maxDatasetHealthPolicyBytes {
		return DatasetHealthPolicy{}, fmt.Errorf("Dataset health policy exceeds %d bytes", maxDatasetHealthPolicyBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var cfg DatasetHealthPolicy
	if err := decoder.Decode(&cfg); err != nil {
		return DatasetHealthPolicy{}, fmt.Errorf("decode Dataset health policy: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return DatasetHealthPolicy{}, fmt.Errorf("Dataset health policy must contain exactly one YAML document")
		}
		return DatasetHealthPolicy{}, fmt.Errorf("decode Dataset health policy: %w", err)
	}
	sum := sha256.Sum256(raw)
	cfg.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	if err := cfg.Validate(); err != nil {
		return DatasetHealthPolicy{}, err
	}
	return cfg, nil
}

func (c DatasetHealthPolicy) Validate() error {
	if c.Version != 2 {
		return fmt.Errorf("unsupported Dataset health policy version %d", c.Version)
	}
	defaults := c.RealtimeTimeSeries.Defaults
	if defaults.RunMissedIntervals <= 0 ||
		defaults.SuccessMissedIntervals <= 0 ||
		defaults.MinuteLateness <= 0 ||
		defaults.HourLateness <= 0 {
		return fmt.Errorf("realtime_timeseries defaults must be positive")
	}
	seen := make(map[string]bool, len(c.RealtimeTimeSeries.Overrides))
	for index, override := range c.RealtimeTimeSeries.Overrides {
		if strings.TrimSpace(override.SpaceID) == "" ||
			strings.TrimSpace(override.DatasetID) == "" ||
			strings.TrimSpace(override.Freq) == "" {
			return fmt.Errorf("realtime_timeseries override %d requires space_id, dataset_id, and freq", index)
		}
		freq, err := frequency.Parse(override.Freq)
		if err != nil {
			return fmt.Errorf("realtime_timeseries override %d has invalid freq %q", index, override.Freq)
		}
		key := strings.Join([]string{override.SpaceID, override.DatasetID, string(freq)}, "\x00")
		if seen[key] {
			return fmt.Errorf(
				"duplicate realtime_timeseries override for %s/%s/%s",
				override.SpaceID,
				override.DatasetID,
				override.Freq,
			)
		}
		seen[key] = true
		if override.Lateness < 0 {
			return fmt.Errorf("realtime_timeseries override %d lateness must not be negative", index)
		}
	}
	return nil
}

func ValidateDatasetHealthEnvironment() (DatasetHealthPolicy, error) {
	path := strings.TrimSpace(os.Getenv("MOOX_DATASET_HEALTH_POLICY"))
	if path == "" {
		return DatasetHealthPolicy{}, nil
	}
	cfg, err := LoadDatasetHealthPolicy(path)
	if err != nil {
		return DatasetHealthPolicy{}, err
	}
	want := strings.TrimSpace(os.Getenv("MOOX_DATASET_HEALTH_POLICY_HASH"))
	if want == "" {
		return DatasetHealthPolicy{}, fmt.Errorf(
			"MOOX_DATASET_HEALTH_POLICY_HASH is required with MOOX_DATASET_HEALTH_POLICY",
		)
	}
	if want != cfg.Checksum {
		return DatasetHealthPolicy{}, fmt.Errorf(
			"Dataset health policy checksum mismatch: got %s want %s",
			cfg.Checksum,
			want,
		)
	}
	return cfg, nil
}
