package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

var consumerPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
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
	CredentialFile  string        `yaml:"credential_file"`
	Consumer        string        `yaml:"-"`
	FetchBatch      int           `yaml:"fetch_batch"`
	FetchMaxWait    time.Duration `yaml:"fetch_max_wait"`
	DedupeRetention time.Duration `yaml:"dedupe_retention"`
}

const ArchiveConsumer = "moox_archive_kline_v2"

type MaterializeConfig struct {
	PendingRows     int           `yaml:"pending_rows"`
	Workers         int           `yaml:"workers"`
	RowGroupRows    int64         `yaml:"row_group_rows"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
}

type StorageRPCConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
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
				"stock_cn": {Datasets: []string{"equity_kline", "etf_kline", "index_kline"}},
				"stock_us": {Datasets: []string{"equity_kline", "etf_kline", "index_kline"}},
				"crypto":   {Datasets: []string{"spot_kline_1h", "perpetual_kline_1h"}},
			},
			EventBus: EventBusConfig{
				URLs:     []string{"nats://127.0.0.1:4222"},
				Consumer: ArchiveConsumer, FetchBatch: 128, FetchMaxWait: time.Second,
				DedupeRetention: 168 * time.Hour,
			},
			Materialize: MaterializeConfig{PendingRows: 10000, Workers: 2, RowGroupRows: 65536, ShutdownTimeout: 2 * time.Minute},
			StorageRPC:  StorageRPCConfig{GatewayTarget: "ip://127.0.0.1:11003", KeyID: "archive"},
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
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
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
	if c.Archive.EventBus.Consumer == "" {
		c.Archive.EventBus.Consumer = d.Archive.EventBus.Consumer
	}
	if c.Archive.EventBus.FetchBatch == 0 {
		c.Archive.EventBus.FetchBatch = d.Archive.EventBus.FetchBatch
	}
	if c.Archive.EventBus.FetchMaxWait == 0 {
		c.Archive.EventBus.FetchMaxWait = d.Archive.EventBus.FetchMaxWait
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
	if c.Archive.StorageRPC.GatewayTarget == "" {
		c.Archive.StorageRPC.GatewayTarget = d.Archive.StorageRPC.GatewayTarget
	}
	if c.Archive.StorageRPC.KeyID == "" {
		c.Archive.StorageRPC.KeyID = d.Archive.StorageRPC.KeyID
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
	allowed := map[string]bool{"stock_cn": true, "stock_us": true, "crypto": true}
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
	if !consumerPattern.MatchString(e.Consumer) {
		return fmt.Errorf("invalid archive eventbus consumer %q", e.Consumer)
	}
	if e.FetchBatch <= 0 || e.FetchMaxWait <= 0 {
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
	if strings.TrimSpace(c.Archive.StorageRPC.HMACKeyFile) != "" {
		if _, err := gatewayauth.CredentialsFromKeyFile(c.Archive.StorageRPC.KeyID, c.Archive.StorageRPC.HMACKeyFile); err != nil {
			return fmt.Errorf("archive storage hmac credentials: %w", err)
		}
	}
	return nil
}

func pathContains(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
