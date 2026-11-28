// Package config loads moox-monitor process configuration.
package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	watchdogpkg "github.com/mooyang-code/moox/modules/monitor/internal/watchdog"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/report"
	"gopkg.in/yaml.v3"
)

type Config struct {
	GatewayClient  gatewayclient.FileConfig `yaml:"gateway_client"`
	sourcePath     string                   `yaml:"-"`
	Database       DatabaseConfig           `yaml:"database"`
	Instance       InstanceConfig           `yaml:"instance"`
	Scheduler      SchedulerConfig          `yaml:"scheduler"`
	Deployment     DeploymentConfig         `yaml:"deployment"`
	Observability  ObservabilityConfig      `yaml:"observability"`
	EventBus       EventBusConfig           `yaml:"eventbus"`
	Metrics        MetricsConfig            `yaml:"metrics"`
	MarketCanary   MarketCanaryConfig       `yaml:"market_canary"`
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

type InstanceConfig struct {
	InstanceID string `yaml:"instance_id"`
}

// SchedulerConfig 是告警事件的保留天数（检查由检查定时器每 30 秒评估，没有单独的调度并发；检查结果不保存历史）。
type SchedulerConfig struct {
	AlertEventRetentionDays int `yaml:"alert_event_retention_days"`
}

// DeploymentConfig 控制是否从 Admin 同步部署拓扑（每台主机应该有哪些组件）；组件健康只看上报，不做探测。
type DeploymentConfig struct {
	Enabled bool `yaml:"enabled"`
}

type MetricsConfig struct {
	Enabled         bool                            `yaml:"enabled"`
	DatasetHealth   report.RealtimeTimeSeriesPolicy `yaml:"dataset_health"`
	NoDataIntervals int                             `yaml:"no_data_intervals"`
	// MaxNewSeriesPerHour / MaxNewSeriesPerDay 限制每小时、每天新建的指标序列数，超出的新序列被丢弃并告警；不写用默认值，负数表示不限。
	// 现状目录约 500 条序列，全新启动时一小时内要建全，所以默认值留足余量，只拦标签失控这种成倍增长。
	MaxNewSeriesPerHour int `yaml:"max_new_series_per_hour"`
	MaxNewSeriesPerDay  int `yaml:"max_new_series_per_day"`
}

type ObservabilityConfig struct {
	Enabled       bool   `yaml:"enabled"`
	DeliverPolicy string `yaml:"deliver_policy"`
}

type EventBusConfig struct {
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
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

type KlineFreshnessConfig struct {
	SpaceIDs                 []string      `yaml:"space_ids"`
	EvaluationInterval       time.Duration `yaml:"evaluation_interval"`
	InventoryRefreshInterval time.Duration `yaml:"inventory_refresh_interval"`
	InventoryPageSize        int           `yaml:"inventory_page_size"`
	InventoryMaxEntries      int           `yaml:"inventory_max_entries"`
	MaxSubjectsPerAlert      int           `yaml:"max_subjects_per_alert"`
}

type MarketCanarySubject struct {
	SpaceID                string   `yaml:"space_id"`
	DatasetID              string   `yaml:"dataset_id"`
	Symbol                 string   `yaml:"symbol"`
	Frequency              string   `yaml:"frequency"`
	SeriesTag              *string  `yaml:"series_tag"`
	MarketID               string   `yaml:"market_id"`
	EligibleKlineProviders []string `yaml:"eligible_kline_providers"`
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
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.FileConfig{Caller: "monitor", KeyFile: "../../secrets/host.key"},
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/monitor/monitor.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Instance: InstanceConfig{
			InstanceID: defaultInstanceID(),
		},
		Scheduler: SchedulerConfig{
			AlertEventRetentionDays: 14,
		},
		Deployment:    DeploymentConfig{Enabled: true},
		Observability: ObservabilityConfig{Enabled: true, DeliverPolicy: "all"},
		EventBus:      EventBusConfig{URLs: []string{"nats://127.0.0.1:4222"}},
		// Canary subjects name concrete collection results, so they come only
		// from deployment config; without subjects the canary stays disabled.
		MarketCanary: MarketCanaryConfig{Freshness: 3 * time.Minute, ReturnThreshold: 0.05, SettleDelay: 5 * time.Second, PostCloseDelay: time.Minute, CalendarWarningLead: 14 * 24 * time.Hour, ClosedBarCount: 3, ClosedBarMinCoverage: 0.99},
		KlineFreshness: KlineFreshnessConfig{
			SpaceIDs: registeredMarketIDs(), EvaluationInterval: 30 * time.Second, InventoryRefreshInterval: time.Minute,
			InventoryPageSize: 100, InventoryMaxEntries: 1000, MaxSubjectsPerAlert: 20,
		},
		Metrics: MetricsConfig{Enabled: true, DatasetHealth: report.DefaultRealtimeTimeSeriesPolicy(), NoDataIntervals: 2, MaxNewSeriesPerHour: 2000, MaxNewSeriesPerDay: 10000},
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
	if c.Instance.InstanceID == "" {
		c.Instance.InstanceID = defaults.Instance.InstanceID
	}
	if c.Scheduler.AlertEventRetentionDays == 0 {
		c.Scheduler.AlertEventRetentionDays = defaults.Scheduler.AlertEventRetentionDays
	}
	metricsDefaults := Default().Metrics
	observabilityDefaults := Default().Observability
	c.Observability.DeliverPolicy = strings.ToLower(strings.TrimSpace(c.Observability.DeliverPolicy))
	if c.Observability.DeliverPolicy == "" {
		c.Observability.DeliverPolicy = observabilityDefaults.DeliverPolicy
	}
	if len(c.EventBus.URLs) == 0 {
		c.EventBus.URLs = defaults.EventBus.URLs
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
	if c.KlineFreshness.MaxSubjectsPerAlert == 0 {
		c.KlineFreshness.MaxSubjectsPerAlert = klineDefaults.MaxSubjectsPerAlert
	}
	if c.Metrics.NoDataIntervals == 0 {
		c.Metrics.NoDataIntervals = metricsDefaults.NoDataIntervals
	}
	if c.Metrics.MaxNewSeriesPerHour == 0 {
		c.Metrics.MaxNewSeriesPerHour = metricsDefaults.MaxNewSeriesPerHour
	}
	if c.Metrics.MaxNewSeriesPerDay == 0 {
		c.Metrics.MaxNewSeriesPerDay = metricsDefaults.MaxNewSeriesPerDay
	}
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_MONITOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_MONITOR_INSTANCE_ID"); v != "" {
		c.Instance.InstanceID = v
	}
	if v := firstEnv("MOOX_OBSERVABILITY_EVENTBUS_URL", "MOOX_EVENTBUS_NATS_URL", "MOOX_EVENTBUS_URL"); v != "" {
		c.EventBus.URLs = strings.Split(v, ",")
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_OBSERVABILITY_CREDENTIAL_FILE")); v != "" {
		c.EventBus.CredentialFile = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_OBSERVABILITY_DELIVER_POLICY")); v != "" {
		c.Observability.DeliverPolicy = strings.ToLower(v)
	}
}

func (c *Config) Validate() error {
	if c.Observability.DeliverPolicy != "all" && c.Observability.DeliverPolicy != "new" {
		return fmt.Errorf("observability.deliver_policy must be all or new")
	}
	if c.Instance.InstanceID == "" {
		return fmt.Errorf("instance.instance_id must not be empty")
	}
	if c.Observability.Enabled {
		if len(c.EventBus.URLs) == 0 {
			return fmt.Errorf("eventbus.urls must not be empty")
		}
		for _, url := range c.EventBus.URLs {
			if strings.TrimSpace(url) == "" {
				return fmt.Errorf("eventbus.urls must not contain empty values")
			}
		}
	}
	if err := c.validateKlineFreshness(); err != nil {
		return err
	}
	if err := c.Metrics.DatasetHealth.Validate(); err != nil {
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
			if err := watchdogpkg.ValidateMarketCanary(watchdogpkg.MarketCanaryConfig{
				SpaceID: subject.SpaceID, MarketID: subject.MarketID, Frequency: subject.Frequency,
				EligibleKlineProviders: subject.EligibleKlineProviders,
			}); err != nil {
				return fmt.Errorf("market_canary 探针 %s/%s：%w", subject.SpaceID, subject.Symbol, err)
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

// registeredMarketIDs 返回 barclock 登记的全部市场 ID（空间即市场），是 K 线新鲜度默认检查的空间。
func registeredMarketIDs() []string {
	markets := barclock.Markets()
	ids := make([]string, 0, len(markets))
	for _, market := range markets {
		ids = append(ids, market.ID)
	}
	return ids
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
