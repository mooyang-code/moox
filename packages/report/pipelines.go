package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const MaxPipelines = 32

type PipelineConfig struct {
	Version   int        `yaml:"version" json:"version"`
	Pipelines []Pipeline `yaml:"pipelines" json:"pipelines"`
	Checksum  string     `yaml:"-" json:"checksum"`
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
	if c.Version != 1 {
		return fmt.Errorf("unsupported pipeline config version %d", c.Version)
	}
	if len(c.Pipelines) > MaxPipelines {
		return fmt.Errorf("pipeline count %d exceeds %d", len(c.Pipelines), MaxPipelines)
	}
	seen := map[string]bool{}
	for i, pipeline := range c.Pipelines {
		if err := validateMetricLabel("pipeline id", pipeline.ID); err != nil {
			return fmt.Errorf("pipeline %d: %w", i, err)
		}
		if seen[pipeline.ID] {
			return fmt.Errorf("duplicate pipeline id %q", pipeline.ID)
		}
		seen[pipeline.ID] = true
		if !allowedModules[pipeline.Module] {
			return fmt.Errorf("pipeline %q has unknown module %q", pipeline.ID, pipeline.Module)
		}
		if strings.TrimSpace(pipeline.SpaceID) == "" || strings.TrimSpace(pipeline.InputDataset) == "" || strings.TrimSpace(pipeline.OutputDataset) == "" {
			return fmt.Errorf("pipeline %q requires space_id, input_dataset, and output_dataset", pipeline.ID)
		}
		if pipeline.LagTolerance <= 0 {
			return fmt.Errorf("pipeline %q requires positive lag_tolerance", pipeline.ID)
		}
	}
	return nil
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
