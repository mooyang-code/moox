// Package config loads moox-monitor process configuration.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

type Config struct {
	GatewayClient  gatewayclient.FileConfig `yaml:"gateway_client"`
	sourcePath     string                   `yaml:"-"`
	Database       DatabaseConfig           `yaml:"database"`
	Health         HealthConfig             `yaml:"health"`
	HealthAuth     HealthAuthConfig         `yaml:"health_auth"`
	Instance       InstanceConfig           `yaml:"instance"`
	Scheduler      SchedulerConfig          `yaml:"scheduler"`
	Placement      PlacementConfig          `yaml:"placement"`
	Alert          AlertConfig              `yaml:"alert"`
	Observability  ObservabilityConfig      `yaml:"observability"`
	Metrics        MetricsConfig            `yaml:"metrics"`
	MarketCanary   MarketCanaryConfig       `yaml:"market_canary"`
	MarketHealth   MarketHealthConfig       `yaml:"market_health"`
	KlineFreshness KlineFreshnessConfig     `yaml:"kline_freshness"`
}

type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

type HealthConfig struct {
	Addr string `yaml:"addr"`
}

type HealthAuthConfig struct {
	Version   string `yaml:"version"`
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
}

type InstanceConfig struct {
	InstanceID string `yaml:"instance_id"`
}

type SchedulerConfig struct {
	ResultRetentionDays int `yaml:"result_retention_days"`
	MaxConcurrency      int `yaml:"max_concurrency"`
}

type PlacementConfig struct {
	Enabled bool                          `yaml:"enabled"`
	HTTPS   map[string]domain.HTTPSConfig `yaml:"https"`
}

type AlertConfig struct {
	SendTimeoutSeconds int `yaml:"send_timeout_seconds"`
}

type MetricsConfig struct {
	Enabled                 bool                 `yaml:"enabled"`
	DatasetHealthPolicyPath string               `yaml:"dataset_health_policy_path"`
	NoDataIntervals         int                  `yaml:"no_data_intervals"`
	Storage                 MetricsStorageConfig `yaml:"storage"`
	HostStorage             HostStorageConfig    `yaml:"host_storage"`
}

type ObservabilityConfig struct {
	Enabled                    bool     `yaml:"enabled"`
	DeliverPolicy              string   `yaml:"deliver_policy"`
	EventBusURLs               []string `yaml:"eventbus_urls"`
	CredentialFile             string   `yaml:"credential_file"`
	BalanceDifferenceThreshold float64  `yaml:"balance_difference_threshold"`
}

type MarketCanaryConfig struct {
	Enabled              bool                  `yaml:"enabled"`
	Freshness            time.Duration         `yaml:"freshness"`
	ReturnThreshold      float64               `yaml:"return_threshold"`
	SettleDelay          time.Duration         `yaml:"settle_delay"`
	PostCloseDelay       time.Duration         `yaml:"post_close_delay"`
	CalendarWarningLead  time.Duration         `yaml:"calendar_warning_lead"`
	ClosedBarCount       int                   `yaml:"closed_bar_count"`
	ClosedBarMinCoverage float64               `yaml:"closed_bar_min_coverage"`
	Subjects             []MarketCanarySubject `yaml:"subjects"`
}

type MarketHealthConfig struct {
	TimerCoordinationStaleAfter   time.Duration `yaml:"timer_coordination_stale_after"`
	TimerCoordinationPendingGrace time.Duration `yaml:"timer_coordination_pending_grace"`
	LowCapacityHeadroom           int           `yaml:"low_capacity_headroom"`
	FeedFailureRateWindow         time.Duration `yaml:"feed_failure_rate_window"`
	FeedFailureRateThreshold      float64       `yaml:"feed_failure_rate_threshold"`
	InstrumentSnapshotMaxAge      time.Duration `yaml:"instrument_snapshot_max_age"`
	InstrumentMinimumCount        int           `yaml:"instrument_minimum_count"`
	InstrumentRequiredExchanges   []string      `yaml:"instrument_required_exchanges"`
}

type KlineFreshnessConfig struct {
	Enabled                  bool          `yaml:"enabled"`
	SpaceIDs                 []string      `yaml:"space_ids"`
	EvaluationInterval       time.Duration `yaml:"evaluation_interval"`
	InventoryRefreshInterval time.Duration `yaml:"inventory_refresh_interval"`
	InventoryPageSize        int           `yaml:"inventory_page_size"`
	InventoryMaxEntries      int           `yaml:"inventory_max_entries"`
	StaleAfter               time.Duration `yaml:"stale_after"`
	MaxSubjectsPerAlert      int           `yaml:"max_subjects_per_alert"`
}

type MarketCanarySubject struct {
	SpaceID                string   `yaml:"space_id"`
	DatasetID              string   `yaml:"dataset_id"`
	Symbol                 string   `yaml:"symbol"`
	Frequency              string   `yaml:"frequency"`
	SeriesTag              *string  `yaml:"series_tag"`
	MarketID               string   `yaml:"market_id"`
	CalendarPath           string   `yaml:"calendar_path"`
	EligibleKlineProviders []string `yaml:"eligible_kline_providers"`
}

type MetricsStorageConfig struct {
	SpaceID                    string        `yaml:"space_id"`
	DatasetID                  string        `yaml:"dataset_id"`
	Frequency                  string        `yaml:"frequency"`
	MetadataValidationInterval time.Duration `yaml:"metadata_validation_interval"`
	WriteBatchSize             int           `yaml:"write_batch_size"`
}

// HostStorageConfig controls the Storage datasets for host snapshots.
type HostStorageConfig struct {
	Enabled                 bool          `yaml:"enabled"`
	SpaceID                 string        `yaml:"space_id"`
	Frequency               string        `yaml:"frequency"`
	WriteTimeout            time.Duration `yaml:"write_timeout"`
	ReadLimit               int           `yaml:"read_limit"`
	MetadataRefreshInterval time.Duration `yaml:"metadata_refresh_interval"`
	RuleRefreshInterval     time.Duration `yaml:"rule_refresh_interval"`
	ResourceDatasetID       string        `yaml:"resource_dataset_id"`
	FilesystemDatasetID     string        `yaml:"filesystem_dataset_id"`
	DiskDatasetID           string        `yaml:"disk_dataset_id"`
	NetworkDatasetID        string        `yaml:"network_dataset_id"`
}

func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve monitor config: %w", err)
	}
	cfg := Default()
	cfg.sourcePath = absolutePath
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("monitor config must contain exactly one YAML document")
	}
	cfg.applyDefaults()
	cfg.applyEnv()
	for id, target := range cfg.Placement.HTTPS {
		for _, path := range []*string{&target.CAFile, &target.CABaseline} {
			if *path != "" && !filepath.IsAbs(*path) {
				*path = filepath.Join(filepath.Dir(absolutePath), *path)
			}
		}
		cfg.Placement.HTTPS[id] = target
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.FileConfig{Caller: "monitor", KeyFile: "../../secrets/caller-monitor.key"},
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/monitor/monitor.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Health: HealthConfig{
			Addr: ":11409",
		},
		HealthAuth: HealthAuthConfig{Version: "moox-health-v1"},
		Instance: InstanceConfig{
			InstanceID: defaultInstanceID(),
		},
		Scheduler: SchedulerConfig{
			ResultRetentionDays: 14,
			MaxConcurrency:      16,
		},
		Placement: PlacementConfig{Enabled: true},
		Alert: AlertConfig{
			SendTimeoutSeconds: 10,
		},
		Observability: ObservabilityConfig{Enabled: true, DeliverPolicy: "all", EventBusURLs: []string{"nats://127.0.0.1:4222"}, BalanceDifferenceThreshold: 0.05},
		// Canary subjects name concrete collection results, so they come only
		// from deployment config; without subjects the canary stays disabled.
		MarketCanary: MarketCanaryConfig{Freshness: 3 * time.Minute, ReturnThreshold: 0.05, SettleDelay: 5 * time.Second, PostCloseDelay: time.Minute, CalendarWarningLead: 14 * 24 * time.Hour, ClosedBarCount: 3, ClosedBarMinCoverage: 0.99},
		MarketHealth: MarketHealthConfig{TimerCoordinationStaleAfter: 15 * time.Minute, TimerCoordinationPendingGrace: 5 * time.Minute, LowCapacityHeadroom: 2, FeedFailureRateWindow: 5 * time.Minute, FeedFailureRateThreshold: 0.2, InstrumentSnapshotMaxAge: 36 * time.Hour, InstrumentMinimumCount: 4000, InstrumentRequiredExchanges: []string{"XSHG", "XSHE", "XBSE"}},
		KlineFreshness: KlineFreshnessConfig{
			Enabled: false, SpaceIDs: []string{"crypto", "stockcn"}, EvaluationInterval: 30 * time.Second, InventoryRefreshInterval: time.Minute,
			InventoryPageSize: 100, InventoryMaxEntries: 1000, StaleAfter: 5 * time.Minute, MaxSubjectsPerAlert: 20,
		},
		Metrics: MetricsConfig{Enabled: true, DatasetHealthPolicyPath: "../../config/setup/dataset-health-policy.yaml", NoDataIntervals: 2, Storage: MetricsStorageConfig{SpaceID: "mooxsys", DatasetID: "dataset_mooxsys_service_metrics", Frequency: "30s", MetadataValidationInterval: 30 * time.Second, WriteBatchSize: 1000}, HostStorage: HostStorageConfig{Enabled: true, SpaceID: "mooxsys", Frequency: "1m", WriteTimeout: 5 * time.Second, ReadLimit: 500, MetadataRefreshInterval: time.Minute, RuleRefreshInterval: 30 * time.Second, ResourceDatasetID: "dataset_mooxsys_host_resource", FilesystemDatasetID: "dataset_mooxsys_host_filesystem", DiskDatasetID: "dataset_mooxsys_host_disk", NetworkDatasetID: "dataset_mooxsys_host_network"}},
	}
}

func (c *Config) applyDefaults() {
	defaults := Default()
	if c.Database.Type == "" {
		c.Database.Type = defaults.Database.Type
	}
	if c.Database.Path == "" {
		c.Database.Path = defaults.Database.Path
	}
	if c.Database.MaxIdleConns == 0 {
		c.Database.MaxIdleConns = defaults.Database.MaxIdleConns
	}
	if c.Database.MaxOpenConns == 0 {
		c.Database.MaxOpenConns = defaults.Database.MaxOpenConns
	}
	if c.Database.ConnMaxLifetime == 0 {
		c.Database.ConnMaxLifetime = defaults.Database.ConnMaxLifetime
	}
	if c.Database.ConnMaxIdleTime == 0 {
		c.Database.ConnMaxIdleTime = defaults.Database.ConnMaxIdleTime
	}
	if c.Health.Addr == "" {
		c.Health.Addr = defaults.Health.Addr
	}
	if c.HealthAuth.Version == "" {
		c.HealthAuth.Version = defaults.HealthAuth.Version
	}
	if c.Instance.InstanceID == "" {
		c.Instance.InstanceID = defaults.Instance.InstanceID
	}
	if c.Scheduler.ResultRetentionDays == 0 {
		c.Scheduler.ResultRetentionDays = defaults.Scheduler.ResultRetentionDays
	}
	if c.Scheduler.MaxConcurrency == 0 {
		c.Scheduler.MaxConcurrency = defaults.Scheduler.MaxConcurrency
	}
	if c.Alert.SendTimeoutSeconds == 0 {
		c.Alert.SendTimeoutSeconds = defaults.Alert.SendTimeoutSeconds
	}
	metricsDefaults := Default().Metrics
	observabilityDefaults := Default().Observability
	c.Observability.DeliverPolicy = strings.ToLower(strings.TrimSpace(c.Observability.DeliverPolicy))
	if c.Observability.DeliverPolicy == "" {
		c.Observability.DeliverPolicy = observabilityDefaults.DeliverPolicy
	}
	if len(c.Observability.EventBusURLs) == 0 {
		c.Observability.EventBusURLs = observabilityDefaults.EventBusURLs
	}
	if c.Observability.BalanceDifferenceThreshold == 0 {
		c.Observability.BalanceDifferenceThreshold = observabilityDefaults.BalanceDifferenceThreshold
	}
	canaryDefaults := Default().MarketCanary
	if c.MarketCanary.Freshness == 0 {
		c.MarketCanary.Freshness = canaryDefaults.Freshness
	}
	if c.MarketCanary.ReturnThreshold == 0 {
		c.MarketCanary.ReturnThreshold = canaryDefaults.ReturnThreshold
	}
	if c.MarketCanary.PostCloseDelay == 0 {
		c.MarketCanary.PostCloseDelay = canaryDefaults.PostCloseDelay
	}
	if c.MarketCanary.ClosedBarMinCoverage == 0 {
		c.MarketCanary.ClosedBarMinCoverage = canaryDefaults.ClosedBarMinCoverage
	}
	marketHealthDefaults := Default().MarketHealth
	if c.MarketHealth.TimerCoordinationStaleAfter == 0 {
		c.MarketHealth.TimerCoordinationStaleAfter = marketHealthDefaults.TimerCoordinationStaleAfter
	}
	if c.MarketHealth.TimerCoordinationPendingGrace == 0 {
		c.MarketHealth.TimerCoordinationPendingGrace = marketHealthDefaults.TimerCoordinationPendingGrace
	}
	if c.MarketHealth.LowCapacityHeadroom == 0 {
		c.MarketHealth.LowCapacityHeadroom = marketHealthDefaults.LowCapacityHeadroom
	}
	if c.MarketHealth.FeedFailureRateWindow == 0 {
		c.MarketHealth.FeedFailureRateWindow = marketHealthDefaults.FeedFailureRateWindow
	}
	if c.MarketHealth.FeedFailureRateThreshold == 0 {
		c.MarketHealth.FeedFailureRateThreshold = marketHealthDefaults.FeedFailureRateThreshold
	}
	if c.MarketHealth.InstrumentSnapshotMaxAge == 0 {
		c.MarketHealth.InstrumentSnapshotMaxAge = marketHealthDefaults.InstrumentSnapshotMaxAge
	}
	if c.MarketHealth.InstrumentMinimumCount == 0 {
		c.MarketHealth.InstrumentMinimumCount = marketHealthDefaults.InstrumentMinimumCount
	}
	if len(c.MarketHealth.InstrumentRequiredExchanges) == 0 {
		c.MarketHealth.InstrumentRequiredExchanges = append([]string(nil), marketHealthDefaults.InstrumentRequiredExchanges...)
	}
	klineDefaults := Default().KlineFreshness
	if c.KlineFreshness.EvaluationInterval == 0 {
		c.KlineFreshness.EvaluationInterval = klineDefaults.EvaluationInterval
	}
	if len(c.KlineFreshness.SpaceIDs) == 0 {
		c.KlineFreshness.SpaceIDs = append([]string(nil), klineDefaults.SpaceIDs...)
	}
	if c.KlineFreshness.InventoryRefreshInterval == 0 {
		c.KlineFreshness.InventoryRefreshInterval = klineDefaults.InventoryRefreshInterval
	}
	if c.KlineFreshness.InventoryPageSize == 0 {
		c.KlineFreshness.InventoryPageSize = klineDefaults.InventoryPageSize
	}
	if c.KlineFreshness.InventoryMaxEntries == 0 {
		c.KlineFreshness.InventoryMaxEntries = klineDefaults.InventoryMaxEntries
	}
	if c.KlineFreshness.StaleAfter == 0 {
		c.KlineFreshness.StaleAfter = klineDefaults.StaleAfter
	}
	if c.KlineFreshness.MaxSubjectsPerAlert == 0 {
		c.KlineFreshness.MaxSubjectsPerAlert = klineDefaults.MaxSubjectsPerAlert
	}
	if c.Metrics.DatasetHealthPolicyPath == "" {
		c.Metrics.DatasetHealthPolicyPath = metricsDefaults.DatasetHealthPolicyPath
	}
	if c.Metrics.NoDataIntervals == 0 {
		c.Metrics.NoDataIntervals = metricsDefaults.NoDataIntervals
	}
	if c.Metrics.Storage.SpaceID == "" {
		c.Metrics.Storage.SpaceID = metricsDefaults.Storage.SpaceID
	}
	if c.Metrics.Storage.DatasetID == "" {
		c.Metrics.Storage.DatasetID = metricsDefaults.Storage.DatasetID
	}
	if c.Metrics.Storage.Frequency == "" {
		c.Metrics.Storage.Frequency = metricsDefaults.Storage.Frequency
	}
	if c.Metrics.Storage.MetadataValidationInterval == 0 {
		c.Metrics.Storage.MetadataValidationInterval = metricsDefaults.Storage.MetadataValidationInterval
	}
	if c.Metrics.Storage.WriteBatchSize == 0 {
		c.Metrics.Storage.WriteBatchSize = metricsDefaults.Storage.WriteBatchSize
	}
	if c.Metrics.HostStorage.SpaceID == "" {
		c.Metrics.HostStorage.SpaceID = metricsDefaults.HostStorage.SpaceID
	}
	if c.Metrics.HostStorage.Frequency == "" {
		c.Metrics.HostStorage.Frequency = metricsDefaults.HostStorage.Frequency
	}
	if c.Metrics.HostStorage.WriteTimeout == 0 {
		c.Metrics.HostStorage.WriteTimeout = metricsDefaults.HostStorage.WriteTimeout
	}
	if c.Metrics.HostStorage.ReadLimit == 0 {
		c.Metrics.HostStorage.ReadLimit = metricsDefaults.HostStorage.ReadLimit
	}
	if c.Metrics.HostStorage.MetadataRefreshInterval == 0 {
		c.Metrics.HostStorage.MetadataRefreshInterval = metricsDefaults.HostStorage.MetadataRefreshInterval
	}
	if c.Metrics.HostStorage.RuleRefreshInterval == 0 {
		c.Metrics.HostStorage.RuleRefreshInterval = metricsDefaults.HostStorage.RuleRefreshInterval
	}
	if c.Metrics.HostStorage.ResourceDatasetID == "" {
		c.Metrics.HostStorage.ResourceDatasetID = metricsDefaults.HostStorage.ResourceDatasetID
	}
	if c.Metrics.HostStorage.FilesystemDatasetID == "" {
		c.Metrics.HostStorage.FilesystemDatasetID = metricsDefaults.HostStorage.FilesystemDatasetID
	}
	if c.Metrics.HostStorage.DiskDatasetID == "" {
		c.Metrics.HostStorage.DiskDatasetID = metricsDefaults.HostStorage.DiskDatasetID
	}
	if c.Metrics.HostStorage.NetworkDatasetID == "" {
		c.Metrics.HostStorage.NetworkDatasetID = metricsDefaults.HostStorage.NetworkDatasetID
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_MONITOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_MONITOR_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_VERSION")); v != "" {
		c.HealthAuth.Version = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_ACCESS_KEY")); v != "" {
		c.HealthAuth.AccessKey = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")); v != "" {
		c.HealthAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_MONITOR_INSTANCE_ID"); v != "" {
		c.Instance.InstanceID = v
	}
	if v := firstEnv("MOOX_OBSERVABILITY_EVENTBUS_URL", "MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_URL"); v != "" {
		c.Observability.EventBusURLs = strings.Split(v, ",")
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_OBSERVABILITY_CREDENTIAL_FILE")); v != "" {
		c.Observability.CredentialFile = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_OBSERVABILITY_DELIVER_POLICY")); v != "" {
		c.Observability.DeliverPolicy = strings.ToLower(v)
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_DATASET_HEALTH_POLICY")); v != "" {
		c.Metrics.DatasetHealthPolicyPath = v
	}

}

func (c *Config) Validate() error {
	if c.Observability.DeliverPolicy != "all" && c.Observability.DeliverPolicy != "new" {
		return fmt.Errorf("observability.deliver_policy must be all or new")
	}
	if c.Instance.InstanceID == "" {
		return fmt.Errorf("instance.instance_id must not be empty")
	}
	if c.Alert.SendTimeoutSeconds <= 0 || c.Alert.SendTimeoutSeconds > 300 {
		return fmt.Errorf("alert.send_timeout_seconds must be between 1 and 300")
	}
	if c.Observability.Enabled {
		if len(c.Observability.EventBusURLs) == 0 {
			return fmt.Errorf("observability.eventbus_urls must not be empty")
		}
		for _, url := range c.Observability.EventBusURLs {
			if strings.TrimSpace(url) == "" {
				return fmt.Errorf("observability.eventbus_urls must not contain empty values")
			}
		}
		if c.Observability.BalanceDifferenceThreshold <= 0 || c.Observability.BalanceDifferenceThreshold > 1 {
			return fmt.Errorf("observability.balance_difference_threshold must be in (0, 1]")
		}
	}
	if err := c.validateKlineFreshness(); err != nil {
		return err
	}
	if c.MarketCanary.Enabled {
		if c.MarketCanary.Freshness <= 0 || c.MarketCanary.ReturnThreshold <= 0 || c.MarketCanary.SettleDelay < 0 || c.MarketCanary.PostCloseDelay < 0 || c.MarketCanary.CalendarWarningLead <= 0 || c.MarketCanary.ClosedBarCount <= 0 {
			return fmt.Errorf("market_canary price threshold and freshness must be positive")
		}
		if c.MarketCanary.ClosedBarMinCoverage <= 0 || c.MarketCanary.ClosedBarMinCoverage > 1 {
			return fmt.Errorf("market_canary.closed_bar_min_coverage must be in (0, 1]")
		}
		if len(c.MarketCanary.Subjects) == 0 || len(c.MarketCanary.Subjects) > 8 {
			return fmt.Errorf("market_canary subjects must contain between 1 and 8 entries")
		}
		for _, subject := range c.MarketCanary.Subjects {
			if strings.TrimSpace(subject.SpaceID) == "" || strings.TrimSpace(subject.DatasetID) == "" ||
				strings.TrimSpace(subject.Symbol) == "" || strings.TrimSpace(subject.Frequency) == "" {
				return fmt.Errorf("market_canary subject requires space_id, dataset_id, symbol, and frequency")
			}
			if subject.SeriesTag == nil {
				return fmt.Errorf("market_canary subject requires series_tag (use an explicit empty value for the default series)")
			}
			if strings.EqualFold(subject.MarketID, "stockcn") && (strings.TrimSpace(subject.CalendarPath) == "" || len(subject.EligibleKlineProviders) == 0) {
				return fmt.Errorf("stockcn market_canary subject requires calendar_path and eligible_kline_providers")
			}
		}
	}
	if c.MarketHealth.TimerCoordinationStaleAfter <= 0 {
		return fmt.Errorf("market_health.timer_coordination_stale_after must be positive")
	}
	if c.MarketHealth.TimerCoordinationPendingGrace <= 0 {
		return fmt.Errorf("market_health.timer_coordination_pending_grace must be positive")
	}
	if c.MarketHealth.LowCapacityHeadroom < 0 {
		return fmt.Errorf("market_health.low_capacity_headroom must not be negative")
	}
	if c.MarketHealth.FeedFailureRateWindow <= 0 || c.MarketHealth.FeedFailureRateThreshold <= 0 || c.MarketHealth.FeedFailureRateThreshold > 1 {
		return fmt.Errorf("market_health feed failure window and threshold are invalid")
	}
	if c.MarketHealth.InstrumentSnapshotMaxAge <= 0 || c.MarketHealth.InstrumentMinimumCount <= 0 {
		return fmt.Errorf("market_health instrument snapshot age and minimum count must be positive")
	}
	if len(c.MarketHealth.InstrumentRequiredExchanges) == 0 {
		return fmt.Errorf("market_health.instrument_required_exchanges must not be empty")
	}
	for id, target := range c.Placement.HTTPS {
		if err := target.Validate(); err != nil {
			return fmt.Errorf("placement.https.%s: %w", id, err)
		}
	}
	if c.Placement.Enabled && (strings.TrimSpace(c.HealthAuth.Version) == "" || strings.TrimSpace(c.HealthAuth.AccessKey) == "" || strings.TrimSpace(c.HealthAuth.SecretKey) == "") {
		return fmt.Errorf("health_auth version, access_key, and secret_key must not be empty when placement monitoring is enabled")
	}
	if c.Metrics.HostStorage.Enabled {
		h := c.Metrics.HostStorage
		if h.SpaceID != "mooxsys" {
			return fmt.Errorf("metrics.host_storage.space_id must be mooxsys")
		}
		if h.Frequency != "1m" {
			return fmt.Errorf("metrics.host_storage.frequency must be 1m")
		}
		if h.ReadLimit <= 0 || h.ReadLimit > 500 {
			return fmt.Errorf("metrics.host_storage.read_limit must be between 1 and 500")
		}
		for name, value := range map[string]string{"resource_dataset_id": h.ResourceDatasetID, "filesystem_dataset_id": h.FilesystemDatasetID, "disk_dataset_id": h.DiskDatasetID, "network_dataset_id": h.NetworkDatasetID} {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("metrics.host_storage.%s must not be empty", name)
			}
		}
	}
	if c.GatewayClient.Caller != "monitor" {
		return fmt.Errorf("gateway_client.caller must be monitor")
	}
	if err := c.GatewayClient.Validate(); err != nil {
		return err
	}

	return nil
}

func (c *Config) validateKlineFreshness() error {
	kline := c.KlineFreshness
	if len(kline.SpaceIDs) < 1 || len(kline.SpaceIDs) > 16 {
		return fmt.Errorf("kline_freshness.space_ids must contain between 1 and 16 Space IDs")
	}
	seenSpaces := make(map[string]struct{}, len(kline.SpaceIDs))
	for _, spaceID := range kline.SpaceIDs {
		spaceID = strings.TrimSpace(spaceID)
		if spaceID == "" {
			return fmt.Errorf("kline_freshness.space_ids must not contain an empty Space ID")
		}
		if _, exists := seenSpaces[spaceID]; exists {
			return fmt.Errorf("kline_freshness.space_ids contains duplicate Space %q", spaceID)
		}
		seenSpaces[spaceID] = struct{}{}
	}
	if kline.EvaluationInterval < 30*time.Second {
		return fmt.Errorf("kline_freshness.evaluation_interval must be at least 30s")
	}
	if kline.InventoryRefreshInterval < 30*time.Second || kline.InventoryRefreshInterval > 10*time.Minute {
		return fmt.Errorf("kline_freshness.inventory_refresh_interval must be between 30s and 10m")
	}
	if kline.InventoryPageSize < 1 || kline.InventoryPageSize > 100 {
		return fmt.Errorf("kline_freshness.inventory_page_size must be between 1 and 100")
	}
	if kline.InventoryMaxEntries < 1 || kline.InventoryMaxEntries > 1000 {
		return fmt.Errorf("kline_freshness.inventory_max_entries must be between 1 and 1000")
	}
	if kline.StaleAfter < 2*kline.EvaluationInterval {
		return fmt.Errorf("kline_freshness.stale_after must be at least twice evaluation_interval")
	}
	if kline.MaxSubjectsPerAlert < 1 || kline.MaxSubjectsPerAlert > 100 {
		return fmt.Errorf("kline_freshness.max_subjects_per_alert must be between 1 and 100")
	}
	return nil
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "monitor"
	}
	return fmt.Sprintf("%s-%d", host, os.Getpid())
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

// OpenGateway owns the client shared by Storage, Collector and Admin consumers.
func (c *Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c == nil || c.sourcePath == "" {
		return nil, fmt.Errorf("monitor gateway client requires a loaded module configuration")
	}
	if c.GatewayClient.Caller != "monitor" {
		return nil, fmt.Errorf("gateway_client.caller must be monitor")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.Database.Path), onRefreshError)
}
