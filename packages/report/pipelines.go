package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type PipelineConfig struct {
	Version            int                      `yaml:"version" json:"version"`
	RealtimeTimeSeries RealtimeTimeSeriesPolicy `yaml:"realtime_timeseries" json:"realtime_timeseries"`
	Checksum           string                   `yaml:"-" json:"checksum"`

	// Pipelines remains an in-process doctor model while callers migrate away
	// from legacy pipeline checks. It is deliberately not accepted from YAML.
	Pipelines []Pipeline `yaml:"-" json:"-"`
}

type RealtimeTimeSeriesPolicy struct {
	Defaults  RealtimeTimeSeriesDefaults   `yaml:"defaults" json:"defaults"`
	Overrides []RealtimeTimeSeriesOverride `yaml:"overrides" json:"overrides"`
}

type RealtimeTimeSeriesDefaults struct {
	RunMissedIntervals     int           `yaml:"run_missed_intervals" json:"run_missed_intervals"`
	SuccessMissedIntervals int           `yaml:"success_missed_intervals" json:"success_missed_intervals"`
	WatermarkPeriods       int           `yaml:"watermark_periods" json:"watermark_periods"`
	MinimumWatermarkLag    time.Duration `yaml:"minimum_watermark_lag" json:"minimum_watermark_lag"`
}

type RealtimeTimeSeriesOverride struct {
	SpaceID                string        `yaml:"space_id" json:"space_id"`
	DatasetID              string        `yaml:"dataset_id" json:"dataset_id"`
	Freq                   string        `yaml:"freq" json:"freq"`
	CanarySubjectID        string        `yaml:"canary_subject_id,omitempty" json:"canary_subject_id,omitempty"`
	WatermarkLag           time.Duration `yaml:"watermark_lag,omitempty" json:"watermark_lag,omitempty"`
	MarketPriceChangeRatio float64       `yaml:"market_price_change_ratio,omitempty" json:"market_price_change_ratio,omitempty"`
	MarketVolumeRatio      float64       `yaml:"market_volume_ratio,omitempty" json:"market_volume_ratio,omitempty"`
}

type Pipeline struct {
	ID                     string        `yaml:"id" json:"id"`
	Module                 string        `yaml:"module" json:"module"`
	SpaceID                string        `yaml:"space_id" json:"space_id"`
	InputDataset           string        `yaml:"input_dataset" json:"input_dataset"`
	OutputDataset          string        `yaml:"output_dataset" json:"output_dataset"`
	LagTolerance           time.Duration `yaml:"lag_tolerance" json:"lag_tolerance"`
	Enabled                bool          `yaml:"enabled" json:"enabled"`
	CrossesStorageDeferred bool          `yaml:"crosses_storage_deferred" json:"crosses_storage_deferred"`
}

func LoadPipelineAllowlist(path string) (PipelineConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return PipelineConfig{}, fmt.Errorf("read pipeline allowlist: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var cfg PipelineConfig
	if err := decoder.Decode(&cfg); err != nil {
		return PipelineConfig{}, fmt.Errorf("decode pipeline allowlist: %w", err)
	}
	sum := sha256.Sum256(raw)
	cfg.Checksum = "sha256:" + hex.EncodeToString(sum[:])
	if err := cfg.Validate(); err != nil {
		return PipelineConfig{}, err
	}
	return cfg, nil
}

func (c PipelineConfig) Validate() error {
	if c.Version != 2 {
		return fmt.Errorf("unsupported pipeline config version %d", c.Version)
	}
	defaults := c.RealtimeTimeSeries.Defaults
	if defaults.RunMissedIntervals <= 0 || defaults.SuccessMissedIntervals <= 0 || defaults.WatermarkPeriods <= 0 || defaults.MinimumWatermarkLag <= 0 {
		return fmt.Errorf("realtime_timeseries defaults must be positive")
	}
	seen := make(map[string]bool, len(c.RealtimeTimeSeries.Overrides))
	for index, override := range c.RealtimeTimeSeries.Overrides {
		if strings.TrimSpace(override.SpaceID) == "" || strings.TrimSpace(override.DatasetID) == "" || strings.TrimSpace(override.Freq) == "" {
			return fmt.Errorf("realtime_timeseries override %d requires space_id, dataset_id, and freq", index)
		}
		interval, err := parsePolicyFrequency(strings.TrimSpace(override.Freq))
		if err != nil || interval <= 0 {
			return fmt.Errorf("realtime_timeseries override %d has invalid positive freq %q", index, override.Freq)
		}
		key := strings.Join([]string{override.SpaceID, override.DatasetID, override.Freq}, "\x00")
		if seen[key] {
			return fmt.Errorf("duplicate realtime_timeseries override for %s/%s/%s", override.SpaceID, override.DatasetID, override.Freq)
		}
		seen[key] = true
		if override.WatermarkLag < 0 || override.MarketPriceChangeRatio < 0 || override.MarketVolumeRatio < 0 {
			return fmt.Errorf("realtime_timeseries override %d thresholds must not be negative", index)
		}
	}
	return nil
}

func parsePolicyFrequency(raw string) (time.Duration, error) {
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.ParseUint(strings.TrimSuffix(raw, "d"), 10, 64)
		maxDays := uint64((time.Duration(1<<63 - 1)) / (24 * time.Hour))
		if err != nil || days == 0 || days > maxDays {
			return 0, fmt.Errorf("frequency must be a positive duration")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		return 0, fmt.Errorf("frequency must be a positive duration")
	}
	return interval, nil
}

func (c PipelineConfig) IDsForModule(module string) []string {
	var ids []string
	for _, pipeline := range c.Pipelines {
		if pipeline.Enabled && pipeline.Module == module {
			ids = append(ids, pipeline.ID)
		}
	}
	return ids
}

func ValidatePipelineEnvironment() (PipelineConfig, error) {
	path := strings.TrimSpace(os.Getenv("MOOX_PIPELINE_CONFIG"))
	if path == "" {
		return PipelineConfig{}, nil
	}
	cfg, err := LoadPipelineAllowlist(path)
	if err != nil {
		return PipelineConfig{}, err
	}
	want := strings.TrimSpace(os.Getenv("MOOX_PIPELINE_CONFIG_HASH"))
	if want == "" {
		return PipelineConfig{}, fmt.Errorf("MOOX_PIPELINE_CONFIG_HASH is required with MOOX_PIPELINE_CONFIG")
	}
	if want != cfg.Checksum {
		return PipelineConfig{}, fmt.Errorf("pipeline config checksum mismatch: got %s want %s", cfg.Checksum, want)
	}
	return cfg, nil
}
