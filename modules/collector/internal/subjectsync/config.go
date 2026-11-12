package subjectsync

import (
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/robfig/cron"
)

type Config struct {
	FetchTimeout time.Duration  `yaml:"fetch_timeout"`
	Attributes   []AttributeJob `yaml:"attributes"`
}

type AttributeJob struct {
	SpaceID        string   `yaml:"space_id"`
	Sources        []string `yaml:"sources"`
	InstrumentType string   `yaml:"instrument_type"`
	Cron           string   `yaml:"cron"`
	Timezone       string   `yaml:"timezone"`
}

// DefaultConfig 的标的属性同步任务取各市场 market.yaml 的 subject_sync；没写的市场没有默认任务。
// market.yaml 编在二进制里，解析失败是编码错误，直接 panic。
func DefaultConfig() Config {
	config := Config{FetchTimeout: 2 * time.Minute}
	for _, marketID := range markets.IDs() {
		market, err := markets.ConfigFor(marketID)
		if err != nil {
			panic(err)
		}
		if job := market.SubjectSync; job != nil {
			config.Attributes = append(config.Attributes, AttributeJob{
				SpaceID: marketID, Sources: append([]string(nil), job.Sources...), InstrumentType: job.InstrumentType, Cron: job.Cron, Timezone: job.Timezone,
			})
		}
	}
	return config
}

// Validate normalizes the subject_sync section of the Collector configuration.
func (c *Config) Validate() error {
	if c.FetchTimeout <= 0 || c.FetchTimeout > 10*time.Minute {
		return fmt.Errorf("fetch_timeout must be positive and at most 10m")
	}
	for i := range c.Attributes {
		job := &c.Attributes[i]
		job.SpaceID = strings.TrimSpace(job.SpaceID)
		job.Cron = strings.TrimSpace(job.Cron)
		job.Timezone = strings.TrimSpace(job.Timezone)
		job.InstrumentType = strings.ToLower(strings.TrimSpace(job.InstrumentType))
		if job.Timezone == "" {
			job.Timezone = "UTC"
		}
		if job.Cron == "" {
			job.Cron = "0 0 * * *"
		}
		if job.SpaceID == "" || len(job.Sources) == 0 {
			return fmt.Errorf("attributes[%d]: space_id and sources are required", i)
		}
		seen := map[string]bool{}
		for j, source := range job.Sources {
			source = strings.ToLower(strings.TrimSpace(source))
			if source == "" || seen[source] {
				return fmt.Errorf("attributes[%d].sources: empty or duplicate source", i)
			}
			seen[source], job.Sources[j] = true, source
		}
		if strings.HasPrefix(job.Cron, "@every") {
			return fmt.Errorf("attributes[%d].cron must define a calendar schedule", i)
		}
		schedule, err := cron.ParseStandard(job.Cron)
		if err != nil {
			return fmt.Errorf("attributes[%d].cron: %w", i, err)
		}
		loc, err := time.LoadLocation(job.Timezone)
		if err != nil {
			return fmt.Errorf("attributes[%d].timezone: %w", i, err)
		}
		if schedule.Next(time.Now().In(loc)).IsZero() {
			return fmt.Errorf("attributes[%d].cron has no scheduled occurrence", i)
		}
	}
	return nil
}
