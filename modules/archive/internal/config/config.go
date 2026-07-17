package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var durablePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var sourcePattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

type Config struct {
	Archive ArchiveConfig `yaml:"archive"`
	Health  HealthConfig  `yaml:"health"`
}

type ArchiveConfig struct {
	RootDir     string                  `yaml:"root_dir"`
	StateDir    string                  `yaml:"state_dir"`
	DeviceID    string                  `yaml:"device_id"`
	Sources     map[string]SourceConfig `yaml:"sources"`
	EventBus    EventBusConfig          `yaml:"eventbus"`
	Materialize MaterializeConfig       `yaml:"materialize"`
	StorageRPC  StorageRPCConfig        `yaml:"storage_rpc"`
	COS         COSConfig               `yaml:"cos"`
}

type SourceConfig struct {
	Datasets []string `yaml:"datasets"`
}

type EventBusConfig struct {
	URLs            []string      `yaml:"urls"`
	Stream          string        `yaml:"stream"`
	Subject         string        `yaml:"subject"`
	Durable         string        `yaml:"durable"`
	FetchBatch      int           `yaml:"fetch_batch"`
	FetchMaxWait    time.Duration `yaml:"fetch_max_wait"`
	AckWait         time.Duration `yaml:"ack_wait"`
	MaxAckPending   int           `yaml:"max_ack_pending"`
	DedupeRetention time.Duration `yaml:"dedupe_retention"`
}

type MaterializeConfig struct {
	PendingRows     int           `yaml:"pending_rows"`
	Workers         int           `yaml:"workers"`
	RowGroupRows    int64         `yaml:"row_group_rows"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type StorageRPCConfig struct {
	AccessTarget   string `yaml:"access_target"`
	MetadataTarget string `yaml:"metadata_target"`
}

type COSConfig struct {
	Enabled            bool   `yaml:"enabled"`
	Region             string `yaml:"region"`
	Bucket             string `yaml:"bucket"`
	Prefix             string `yaml:"prefix"`
	SyncOpenPartitions bool   `yaml:"sync_open_partitions"`
	Workers            int    `yaml:"workers"`
}

type HealthConfig struct {
	Addr string `yaml:"addr"`
}

func Default() *Config {
	return &Config{
		Archive: ArchiveConfig{
			RootDir:  "../data/archive",
			StateDir: "../data/archive-state",
			DeviceID: "parquet-local",
			Sources: map[string]SourceConfig{
				"stock_cn":       {Datasets: []string{"equity_kline", "etf_kline", "index_kline"}},
				"stock_us":       {Datasets: []string{"equity_kline", "etf_kline", "index_kline"}},
				"crypto_binance": {Datasets: []string{"spot_kline", "swap_kline"}},
				"crypto_okx":     {Datasets: []string{"spot_kline", "swap_kline"}},
			},
			EventBus: EventBusConfig{
				URLs: []string{"nats://127.0.0.1:4222"}, Stream: "MOOX_STORAGE",
				Subject: "moox.storage.time_series.rows_updated.v1", Durable: "moox_archive_kline_v1",
				FetchBatch: 128, FetchMaxWait: time.Second, AckWait: 5 * time.Minute,
				MaxAckPending: 256, DedupeRetention: 168 * time.Hour,
			},
			Materialize: MaterializeConfig{PendingRows: 10000, Workers: 2, RowGroupRows: 65536, ShutdownTimeout: 2 * time.Minute},
			StorageRPC:  StorageRPCConfig{AccessTarget: "ip://127.0.0.1:20102", MetadataTarget: "ip://127.0.0.1:20100"},
			COS:         COSConfig{Prefix: "moox/archive", Workers: 2},
		},
		Health: HealthConfig{Addr: "127.0.0.1:11416"},
	}
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read archive config %s: %w", path, err)
	}
	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse archive config %s: %w", path, err)
	}
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaults() {
	d := Default()
	if c.Archive.RootDir == "" {
		c.Archive.RootDir = d.Archive.RootDir
	}
	if c.Archive.StateDir == "" {
		c.Archive.StateDir = d.Archive.StateDir
	}
	if c.Archive.DeviceID == "" {
		c.Archive.DeviceID = d.Archive.DeviceID
	}
	if len(c.Archive.Sources) == 0 {
		c.Archive.Sources = d.Archive.Sources
	}
	if len(c.Archive.EventBus.URLs) == 0 {
		c.Archive.EventBus.URLs = d.Archive.EventBus.URLs
	}
	if c.Archive.EventBus.Stream == "" {
		c.Archive.EventBus.Stream = d.Archive.EventBus.Stream
	}
	if c.Archive.EventBus.Subject == "" {
		c.Archive.EventBus.Subject = d.Archive.EventBus.Subject
	}
	if c.Archive.EventBus.Durable == "" {
		c.Archive.EventBus.Durable = d.Archive.EventBus.Durable
	}
	if c.Archive.EventBus.FetchBatch == 0 {
		c.Archive.EventBus.FetchBatch = d.Archive.EventBus.FetchBatch
	}
	if c.Archive.EventBus.FetchMaxWait == 0 {
		c.Archive.EventBus.FetchMaxWait = d.Archive.EventBus.FetchMaxWait
	}
	if c.Archive.EventBus.AckWait == 0 {
		c.Archive.EventBus.AckWait = d.Archive.EventBus.AckWait
	}
	if c.Archive.EventBus.MaxAckPending == 0 {
		c.Archive.EventBus.MaxAckPending = d.Archive.EventBus.MaxAckPending
	}
	if c.Archive.EventBus.DedupeRetention == 0 {
		c.Archive.EventBus.DedupeRetention = d.Archive.EventBus.DedupeRetention
	}
	if c.Archive.Materialize.PendingRows == 0 {
		c.Archive.Materialize.PendingRows = d.Archive.Materialize.PendingRows
	}
	if c.Archive.Materialize.Workers == 0 {
		c.Archive.Materialize.Workers = d.Archive.Materialize.Workers
	}
	if c.Archive.Materialize.RowGroupRows == 0 {
		c.Archive.Materialize.RowGroupRows = d.Archive.Materialize.RowGroupRows
	}
	if c.Archive.Materialize.ShutdownTimeout == 0 {
		c.Archive.Materialize.ShutdownTimeout = d.Archive.Materialize.ShutdownTimeout
	}
	if c.Archive.COS.Prefix == "" {
		c.Archive.COS.Prefix = d.Archive.COS.Prefix
	}
	if c.Archive.COS.Workers == 0 {
		c.Archive.COS.Workers = d.Archive.COS.Workers
	}
	if c.Health.Addr == "" {
		c.Health.Addr = d.Health.Addr
	}
}

func (c *Config) SourceSpaceIDs() []string {
	ids := make([]string, 0, len(c.Archive.Sources))
	for id := range c.Archive.Sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c *Config) Validate() error {
	if strings.TrimSpace(c.Archive.DeviceID) == "" {
		return fmt.Errorf("archive device_id is required")
	}
	root, err := filepath.Abs(filepath.Clean(c.Archive.RootDir))
	if err != nil {
		return err
	}
	state, err := filepath.Abs(filepath.Clean(c.Archive.StateDir))
	if err != nil {
		return err
	}
	if root == state || pathContains(root, state) || pathContains(state, root) {
		return fmt.Errorf("archive root_dir and state_dir must not overlap")
	}
	for _, p := range []string{root, state} {
		if info, statErr := os.Lstat(p); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("archive directory %s must not be a symlink", p)
		}
	}
	allowed := map[string]bool{"stock_cn": true, "stock_us": true, "crypto_binance": true, "crypto_okx": true}
	if len(c.Archive.Sources) == 0 {
		return fmt.Errorf("archive sources are required")
	}
	for space, source := range c.Archive.Sources {
		if !allowed[space] || !sourcePattern.MatchString(space) {
			return fmt.Errorf("unknown archive space %q", space)
		}
		seen := make(map[string]bool)
		for _, dataset := range source.Datasets {
			if strings.TrimSpace(dataset) == "" || seen[dataset] {
				return fmt.Errorf("duplicate or empty dataset in %s", space)
			}
			seen[dataset] = true
		}
		if len(source.Datasets) == 0 {
			return fmt.Errorf("archive datasets are required for %s", space)
		}
	}
	e := c.Archive.EventBus
	if len(e.URLs) == 0 || strings.TrimSpace(strings.Join(e.URLs, ",")) == "" {
		return fmt.Errorf("archive eventbus urls are required")
	}
	if !durablePattern.MatchString(e.Durable) {
		return fmt.Errorf("invalid archive eventbus durable %q", e.Durable)
	}
	if e.Stream == "" || e.Subject == "" || e.FetchBatch <= 0 || e.FetchMaxWait <= 0 || e.AckWait <= 0 || e.MaxAckPending <= 0 {
		return fmt.Errorf("archive eventbus settings are invalid")
	}
	if e.DedupeRetention < 168*time.Hour {
		return fmt.Errorf("archive dedupe_retention must be at least 168h")
	}
	m := c.Archive.Materialize
	if m.PendingRows <= 0 || m.Workers < 1 || m.Workers > 32 || m.RowGroupRows <= 0 || m.ShutdownTimeout <= 0 {
		return fmt.Errorf("archive materialize settings are invalid")
	}
	if c.Archive.COS.Enabled && (strings.TrimSpace(c.Archive.COS.Region) == "" || strings.TrimSpace(c.Archive.COS.Bucket) == "") {
		return fmt.Errorf("archive cos region and bucket are required")
	}
	return nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
