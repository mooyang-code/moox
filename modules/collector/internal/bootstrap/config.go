// Package bootstrap loads configuration and wires the moox-collector service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
	"gopkg.in/yaml.v3"
)

// Config is the root collector control-plane configuration.
type Config struct {
	GatewayClient       gatewayclient.FileConfig         `yaml:"gateway_client"`
	sourcePath          string                           `yaml:"-"`
	SCFRegionBlacklists map[string][]string              `yaml:"scf_region_blacklists"`
	Database            DatabaseConfig                   `yaml:"database"`
	Storage             StorageConfig                    `yaml:"storage"`
	SCFAccess           gatewayclient.ExternalFileConfig `yaml:"scf_access"`
	SCFAccessRoutes     map[string]SCFAccessEndpoint     `yaml:"scf_access_routes"`
	CollectorRetention  CollectorRetentionConfig         `yaml:"collector_retention"`
	StockCN             StockCNConfig                    `yaml:"stockcn"`
	PeriodReadiness     PeriodReadinessConfig            `yaml:"period_readiness"`
	KlineResample       KlineResampleConfig              `yaml:"kline_resample"`
	Health              HealthConfig                     `yaml:"health"`
	DNS                 DNSConfig                        `yaml:"dns"`
	EgressProxy         EgressProxyConfig                `yaml:"egress_proxy"`
}

// StockCNConfig carries the release-time capacity contract to the Collector
// reconciler. It is rendered from moox.toml; zero values fail closed when a
// stockcn collection task is selected rather than silently choosing a default fleet.
type StockCNConfig struct {
	ExpectedTimerFunctionCount int `yaml:"expected_timer_function_count"`
	MeasuredSafeGroupSize      int `yaml:"measured_safe_group_size"`
	StaggerStartSecond         int `yaml:"stagger_start_second"`
	StaggerWindowSeconds       int `yaml:"stagger_window_seconds"`
	StaggerMaxStartsPerSecond  int `yaml:"stagger_max_starts_per_second"`
}

// DatabaseConfig describes SQLite settings.
type DatabaseConfig struct {
	Type            string        `yaml:"type"`
	Path            string        `yaml:"path"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// StorageConfig identifies the node used for collected results.
type StorageConfig struct {
	ResultDataNodeID string `yaml:"result_data_node_id"`
}

// SCFAccessEndpoint is the fixed endpoint selected for one function region.
// The publisher supplies private endpoints only after verifying its VPC route.
type SCFAccessEndpoint struct {
	Address    string `yaml:"access_address"`
	InstanceID string `yaml:"access_id"`
}

// CollectorRetentionConfig bounds the process-level execution-history cleanup.
type CollectorRetentionConfig struct {
	MaintenanceInterval          string `yaml:"maintenance_interval"`
	MaintenanceOffset            string `yaml:"maintenance_offset"`
	MaintenanceTimeout           string `yaml:"maintenance_timeout"`
	MaxRowsPerPass               int    `yaml:"max_rows_per_pass"`
	ExecutionDetailRetention     string `yaml:"execution_detail_retention"`
	ScheduledRunSummaryRetention string `yaml:"scheduled_run_summary_retention"`
	TerminalRetryRetention       string `yaml:"terminal_retry_retention"`
	PeriodSnapshotRetention      string `yaml:"period_snapshot_retention"`
}

func (c CollectorRetentionConfig) interval() time.Duration {
	value, _ := time.ParseDuration(c.MaintenanceInterval)
	return value
}

func (c CollectorRetentionConfig) offset() time.Duration {
	value, _ := time.ParseDuration(c.MaintenanceOffset)
	return value
}

func (c CollectorRetentionConfig) timeout() time.Duration {
	value, _ := time.ParseDuration(c.MaintenanceTimeout)
	return value
}

func (c CollectorRetentionConfig) duration(value string) time.Duration {
	parsed, _ := time.ParseDuration(value)
	return parsed
}

// PeriodReadinessConfig controls the durable Collector period completion
// projection and its retry/retention loops.
type PeriodReadinessConfig struct {
	Grace           time.Duration `yaml:"grace"`
	ReportInterval  time.Duration `yaml:"report_interval"`
	ItemRetention   int           `yaml:"item_retention"`
	ParentRetention time.Duration `yaml:"parent_retention"`
}

// KlineResampleConfig controls the local derived-kline scheduler. Task
// identity and source/target semantics remain in CollectionTask; these values are
// process-wide execution policy.
type KlineResampleConfig struct {
	Enabled                     bool          `yaml:"enabled"`
	ScanTimeout                 time.Duration `yaml:"scan_timeout"`
	WorkerConcurrency           int           `yaml:"worker_concurrency"`
	MaxClaimsPerTick            int           `yaml:"max_claims_per_tick"`
	WorkerSubjectBatchSize      int           `yaml:"worker_subject_batch_size"`
	WorkerJobTimeout            time.Duration `yaml:"worker_job_timeout"`
	WorkerPollInterval          time.Duration `yaml:"worker_poll_interval"`
	WorkerMaxSourceKeysPerClaim int           `yaml:"worker_max_source_keys_per_claim"`
	StaleRunningAfter           time.Duration `yaml:"stale_running_after"`
	DefaultSettleDelay          time.Duration `yaml:"default_settle_delay"`
	RepairLookbackBuckets       int           `yaml:"repair_lookback_buckets"`
}

// HealthConfig controls the lightweight HTTP health endpoint.
type HealthConfig struct {
	Addr string `yaml:"addr"`
}

// DNSConfig controls the small control-plane DNS snapshot sent with SCF
// requests. Empty nameservers use the host resolver.
type DNSConfig struct {
	Domains         []string      `yaml:"domains"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	ResolveTimeout  time.Duration `yaml:"resolve_timeout"`
	Nameservers     []string      `yaml:"nameservers"`
}

// EgressProxyConfig selects HTTP domains and DNS snapshots through the shared gateway.
type EgressProxyConfig struct {
	Domains []string        `yaml:"domains"`
	DNS     EgressDNSConfig `yaml:"dns"`
}
type EgressDNSConfig struct {
	Domains         []string      `yaml:"domains"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	CacheTTL        time.Duration `yaml:"cache_ttl"`
}

func (c EgressDNSConfig) Enabled() bool { return len(c.Domains) > 0 }

// Load reads YAML config from path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	cfg.sourcePath, err = filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve collector config: %w", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("collector config must contain exactly one YAML document")
	}
	if cfg.GatewayClient.Caller != "collector" {
		return nil, fmt.Errorf("gateway_client.caller must be collector")
	}
	if err := cfg.GatewayClient.Validate(); err != nil {
		return nil, err
	}
	cfg.applyEnv()
	if err := cfg.validateEgressProxy(); err != nil {
		return nil, err
	}
	if err := cfg.validateSCFAccess(); err != nil {
		return nil, err
	}
	if err := cfg.validateKlineResample(); err != nil {
		return nil, err
	}
	if err := cfg.validatePeriodReadiness(); err != nil {
		return nil, err
	}
	if err := cfg.validateCollectorRetention(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_COLLECTOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_RESULT_DATA_NODE_ID"); v != "" {
		c.Storage.ResultDataNodeID = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_HEALTH_ADDR"); v != "" {
		c.Health.Addr = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_DOMAINS"); v != "" {
		c.DNS.Domains = splitCSV(v)
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_NAMESERVERS"); v != "" {
		c.DNS.Nameservers = splitCSV(v)
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_REFRESH_INTERVAL"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			c.DNS.RefreshInterval = parsed
		}
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVE_TIMEOUT"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			c.DNS.ResolveTimeout = parsed
		}
	}
}

func (c *Config) validateSCFAccess() error {
	if c.SCFAccess.Caller != "scf-collector" {
		return fmt.Errorf("scf_access.caller must be scf-collector")
	}
	// Offline Collector configuration can precede Admin credential assignment.
	if c.SCFAccess.KeyID == "" {
		return nil
	}
	if err := c.SCFAccess.Validate(); err != nil {
		return fmt.Errorf("scf_access: %w", err)
	}
	for region, endpoint := range c.SCFAccessRoutes {
		if region == "" || region != strings.ToLower(strings.TrimSpace(region)) {
			return fmt.Errorf("scf_access_routes requires canonical regions")
		}
		config := c.SCFAccess
		config.Address, config.InstanceID = endpoint.Address, endpoint.InstanceID
		if err := config.Validate(); err != nil {
			return fmt.Errorf("scf_access_routes[%s]: %w", region, err)
		}
	}
	return nil
}

func (c *Config) validateEgressProxy() error {
	if _, err := domainpolicy.New(c.EgressProxy.Domains); err != nil {
		return fmt.Errorf("egress_proxy.domains: %w", err)
	}
	dns := &c.EgressProxy.DNS
	if len(dns.Domains) > 16 {
		return fmt.Errorf("egress_proxy.dns supports at most 16 domains")
	}
	seen := map[string]bool{}
	for i, raw := range dns.Domains {
		domain, ok := domainpolicy.Host(strings.TrimSpace(raw))
		if !ok || seen[domain] {
			return fmt.Errorf("egress_proxy.dns domain %q is invalid or duplicated", raw)
		}
		seen[domain] = true
		dns.Domains[i] = domain
	}
	if dns.RefreshInterval <= 0 || dns.RequestTimeout <= 0 || dns.RequestTimeout > 60*time.Second || dns.CacheTTL <= 0 {
		return fmt.Errorf("egress_proxy.dns intervals must be positive and request_timeout at most 60s")
	}
	return nil
}

func (c *Config) validateKlineResample() error {
	if c.KlineResample.ScanTimeout <= 0 || c.KlineResample.WorkerJobTimeout <= 0 || c.KlineResample.WorkerPollInterval <= 0 || c.KlineResample.StaleRunningAfter <= 0 || c.KlineResample.DefaultSettleDelay < 0 {
		return fmt.Errorf("kline_resample durations must be positive, except default_settle_delay")
	}
	if c.KlineResample.WorkerConcurrency <= 0 || c.KlineResample.WorkerConcurrency > 250 || c.KlineResample.MaxClaimsPerTick < 3 || c.KlineResample.MaxClaimsPerTick > 1000 || c.KlineResample.WorkerSubjectBatchSize <= 0 || c.KlineResample.WorkerSubjectBatchSize > 200 || c.KlineResample.WorkerMaxSourceKeysPerClaim <= 0 {
		return fmt.Errorf("kline_resample worker quantities are invalid")
	}
	if c.KlineResample.RepairLookbackBuckets < 0 || c.KlineResample.RepairLookbackBuckets > 10 {
		return fmt.Errorf("kline_resample.repair_lookback_buckets must be between 0 and 10")
	}
	return nil
}

func (c *Config) validateCollectorRetention() error {
	retention := c.CollectorRetention
	interval, err := time.ParseDuration(strings.TrimSpace(retention.MaintenanceInterval))
	if err != nil || interval <= 0 || interval > 24*time.Hour {
		return fmt.Errorf("collector_retention.maintenance_interval must be greater than 0 and at most 24h")
	}
	offset, offsetErr := time.ParseDuration(strings.TrimSpace(retention.MaintenanceOffset))
	timeout, timeoutErr := time.ParseDuration(strings.TrimSpace(retention.MaintenanceTimeout))
	if offsetErr != nil || timeoutErr != nil || marketfetch.ValidateMaintenanceSchedule(interval, offset, timeout) != nil {
		return fmt.Errorf("collector_retention.maintenance_offset and maintenance_timeout must keep each pass inside one interval: 0 <= offset < interval and offset + timeout <= interval")
	}
	if retention.MaxRowsPerPass < 9 || retention.MaxRowsPerPass > 50000 {
		return fmt.Errorf("collector_retention.max_rows_per_pass must be between 9 and 50000")
	}
	for name, raw := range map[string]string{
		"execution_detail_retention":      retention.ExecutionDetailRetention,
		"scheduled_run_summary_retention": retention.ScheduledRunSummaryRetention,
		"terminal_retry_retention":        retention.TerminalRetryRetention,
		"period_snapshot_retention":       retention.PeriodSnapshotRetention,
	} {
		duration, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || duration <= 0 || duration > 365*24*time.Hour {
			return fmt.Errorf("collector_retention.%s must be greater than 0 and at most 365 days", name)
		}
	}
	return nil
}

func (c *Config) validatePeriodReadiness() error {
	if c.PeriodReadiness.ItemRetention < 1 {
		return fmt.Errorf("period_readiness.item_retention must be positive")
	}
	if c.PeriodReadiness.ParentRetention <= 0 || c.PeriodReadiness.ParentRetention > 365*24*time.Hour {
		return fmt.Errorf("period_readiness.parent_retention must be greater than 0 and at most 365 days")
	}
	return nil
}

// Default returns safe local defaults.
func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.FileConfig{Caller: "collector", KeyFile: "../../secrets/caller-collector.key"},
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_collector.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		Storage:   StorageConfig{ResultDataNodeID: "storage-node-0"},
		SCFAccess: gatewayclient.ExternalFileConfig{Address: "access.example.test:11004", InstanceID: "access@storage", Caller: "scf-collector", KeyFile: "../../secrets/caller-scf-collector.key"},
		PeriodReadiness: PeriodReadinessConfig{
			Grace: 2 * time.Minute, ReportInterval: 5 * time.Second,
			ItemRetention: 60, ParentRetention: 7 * 24 * time.Hour,
		},
		CollectorRetention: CollectorRetentionConfig{
			MaintenanceInterval: "1m", MaintenanceOffset: "35s", MaintenanceTimeout: "20s", MaxRowsPerPass: 50000,
			ExecutionDetailRetention: "6h", ScheduledRunSummaryRetention: "720h",
			TerminalRetryRetention: "168h", PeriodSnapshotRetention: "720h",
		},
		KlineResample: KlineResampleConfig{
			Enabled: false, ScanTimeout: 30 * time.Second, WorkerConcurrency: 2, MaxClaimsPerTick: 100,
			WorkerSubjectBatchSize: 50, WorkerJobTimeout: 30 * time.Second,
			WorkerPollInterval: 5 * time.Second, WorkerMaxSourceKeysPerClaim: 20000,
			StaleRunningAfter: 2 * time.Minute, DefaultSettleDelay: 10 * time.Second,
			RepairLookbackBuckets: 3,
		},
		Health: HealthConfig{
			Addr: ":11412",
		},
		DNS: DNSConfig{
			Domains:         []string{"data-api.binance.vision", "api.binance.com", "fapi.binance.com"},
			RefreshInterval: 5 * time.Minute,
			ResolveTimeout:  5 * time.Second,
		},
		EgressProxy: EgressProxyConfig{Domains: []string{"*.binance.com", "data-api.binance.vision"}, DNS: EgressDNSConfig{
			RefreshInterval: 5 * time.Minute,
			RequestTimeout:  3 * time.Second,
			CacheTTL:        5 * time.Minute,
		}},
	}
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			result = append(result, value)
		}
	}
	return result
}

// OpenGateway opens one process-owned client using the canonical host identity.
func (c *Config) OpenGateway(onRefreshError func(error)) (*gatewayclient.Client, error) {
	if c == nil || c.sourcePath == "" || c.GatewayClient.Caller != "collector" {
		return nil, fmt.Errorf("collector gateway client requires a loaded collector configuration")
	}
	return c.GatewayClient.OpenInternal(c.sourcePath, filepath.Dir(c.Database.Path), onRefreshError)
}

func (c *Config) scfAccessEnvironment(region string) (map[string]string, error) {
	config := c.SCFAccess
	if route, ok := c.SCFAccessRoutes[strings.ToLower(strings.TrimSpace(region))]; ok {
		config.Address, config.InstanceID = route.Address, route.InstanceID
	}
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("SCF Access configuration: %w", err)
	}
	keyPath := config.KeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(c.sourcePath), keyPath)
	}
	secret, err := gatewayauth.ReadSigningSecret(keyPath)
	if err != nil {
		return nil, fmt.Errorf("SCF Access signing key: %w", err)
	}
	return map[string]string{"MOOX_ACCESS_ADDRESS": config.Address, "MOOX_ACCESS_ID": config.InstanceID, "MOOX_CALLER": config.Caller, "MOOX_CALLER_KEY_ID": config.KeyID, "MOOX_CALLER_KEY": secret}, nil
}
