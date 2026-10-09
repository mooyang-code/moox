package subjectsync

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/robfig/cron"
	"gopkg.in/yaml.v3"
)

type Config struct {
	PollInterval time.Duration  `yaml:"poll_interval"`
	FetchTimeout time.Duration  `yaml:"fetch_timeout"`
	HealthAddr   string         `yaml:"health_addr"`
	Attributes   []AttributeJob `yaml:"attributes"`
}

type AttributeJob struct {
	SpaceID        string   `yaml:"space_id"`
	Sources        []string `yaml:"sources"`
	InstrumentType string   `yaml:"instrument_type"`
	Cron           string   `yaml:"cron"`
	Timezone       string   `yaml:"timezone"`
}

func LoadConfig(path string) (Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var cfg Config
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, fmt.Errorf("subject config must contain exactly one YAML document")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Minute
	}
	if cfg.FetchTimeout <= 0 {
		cfg.FetchTimeout = 2 * time.Minute
	}
	if cfg.HealthAddr == "" {
		cfg.HealthAddr = "127.0.0.1:11413"
	}
	for i := range cfg.Attributes {
		job := &cfg.Attributes[i]
		job.SpaceID = strings.TrimSpace(job.SpaceID)
		if job.Timezone == "" {
			job.Timezone = "UTC"
		}
		if job.Cron == "" {
			job.Cron = "0 0 * * *"
		}
		if job.SpaceID == "" || len(job.Sources) == 0 {
			return Config{}, fmt.Errorf("attributes[%d]: space_id and sources are required", i)
		}
		if _, err := cron.ParseStandard(job.Cron); err != nil {
			return Config{}, fmt.Errorf("attributes[%d].cron: %w", i, err)
		}
		if _, err := time.LoadLocation(job.Timezone); err != nil {
			return Config{}, fmt.Errorf("attributes[%d].timezone: %w", i, err)
		}
	}
	return cfg, nil
}
