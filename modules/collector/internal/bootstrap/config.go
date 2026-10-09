// Package bootstrap loads configuration and wires the moox-collector service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Config is the root collector control-plane configuration.
type Config struct {
	// GatewayClient 是 Collector 调用其他组件（Storage、CloudNode、出口代理等）使用的 gatewayclient 配置。
	GatewayClient       gatewayclient.Config     `yaml:"gateway_client"`
	SCFRegionBlacklists map[string][]string      `yaml:"scf_region_blacklists"`
	Database            DatabaseConfig           `yaml:"database"`
	CloudNode           CloudNodeConfig          `yaml:"cloudnode"`
	Storage             StorageConfig            `yaml:"storage"`
	CollectorRetention  CollectorRetentionConfig `yaml:"collector_retention"`
	StockCN             StockCNConfig            `yaml:"stockcn"`
	PeriodReadiness     PeriodReadinessConfig    `yaml:"period_readiness"`
	KlineResample       KlineResampleConfig      `yaml:"kline_resample"`
	Health              HealthConfig             `yaml:"health"`
	DNS                 DNSConfig                `yaml:"dns"`
	EgressProxy         EgressProxyConfig        `yaml:"egress_proxy"`
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

// CloudNodeConfig describes cloudnode RPC routing.
type CloudNodeConfig struct {
	Address     string `yaml:"address"`
	ServicePath string `yaml:"service_path"`
}

// StorageConfig 描述 Storage 相关的设置。Collector 自己访问 Storage 走 gateway_client。
type StorageConfig struct {
	ResultDataNodeID string `yaml:"result_data_node_id"`
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

// EgressProxyConfig 是 Collector 使用出口代理的设置。出口代理部署在香港，经主机网关调用；由 CLI 按
// moox.toml 渲染。
type EgressProxyConfig struct {
	// Domains 是 HTTP 请求走出口代理的域名，支持 "*." 前缀；为空时所有请求直连。
	Domains []string `yaml:"domains"`
	// DNS 是由出口代理解析的域名，结果写入 SCF 的 DNS 快照。
	DNS EgressDNSConfig `yaml:"dns"`
}

// EgressDNSConfig 是向出口代理请求解析结果的设置；Domains 为空时只用本机解析（dns 段）。
type EgressDNSConfig struct {
	Domains         []string      `yaml:"domains"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	CacheTTL        time.Duration `yaml:"cache_ttl"`
}

// Enabled 判断是否向出口代理请求解析结果。
func (c EgressDNSConfig) Enabled() bool { return len(c.Domains) > 0 }

// maxEgressDNSDomains 是出口代理一次解析的域名上限。
const maxEgressDNSDomains = 16

// SnapshotDomains 返回写入 SCF DNS 快照的全部域名：本机解析的域名加上出口代理解析的域名，规范化并去重。
func (c *Config) SnapshotDomains() []string {
	all := append(append([]string(nil), c.DNS.Domains...), c.EgressProxy.DNS.Domains...)
	seen := make(map[string]struct{}, len(all))
	domains := make([]string, 0, len(all))
	for _, raw := range all {
		domain := egresspb.NormalizeHost(raw)
		if _, exists := seen[domain]; exists || domain == "" {
			continue
		}
		seen[domain] = struct{}{}
		domains = append(domains, domain)
	}
	return domains
}

// Load reads YAML config from path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	cfg := Default()
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.applyEnv()
	if err := cfg.GatewayClient.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.validateEgressProxy(); err != nil {
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
}

func (c *Config) validateEgressProxy() error {
	if _, err := egresspb.ParseDomainList(c.EgressProxy.Domains); err != nil {
		return fmt.Errorf("egress_proxy.domains: %w", err)
	}
	dns := c.EgressProxy.DNS
	seen := make(map[string]struct{}, len(dns.Domains))
	for _, raw := range dns.Domains {
		domain := egresspb.NormalizeHost(raw)
		if !egresspb.ValidDomain(domain) {
			return fmt.Errorf("egress_proxy.dns.domains 中的 %q 不是合法域名", raw)
		}
		if _, exists := seen[domain]; exists {
			return fmt.Errorf("egress_proxy.dns.domains 中的 %q 重复", raw)
		}
		seen[domain] = struct{}{}
	}
	if !dns.Enabled() {
		return nil
	}
	// 出口代理一次请求解析快照里的全部域名，超过上限时整个请求都会被拒绝。
	if count := len(c.SnapshotDomains()); count > maxEgressDNSDomains {
		return fmt.Errorf("dns.domains 与 egress_proxy.dns.domains 合计 %d 个域名，出口代理一次最多解析 %d 个", count, maxEgressDNSDomains)
	}
	if dns.RefreshInterval <= 0 || dns.RequestTimeout <= 0 || dns.CacheTTL <= 0 {
		return fmt.Errorf("egress_proxy.dns 的 refresh_interval、request_timeout、cache_ttl 必须大于 0")
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
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_collector.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		CloudNode: CloudNodeConfig{
			Address:     "127.0.0.1:11401",
			ServicePath: "trpc.moox.cloudnode.CloudNodeMgr",
		},
		Storage: StorageConfig{ResultDataNodeID: "storage-node-0"},
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
		EgressProxy: EgressProxyConfig{DNS: EgressDNSConfig{
			RefreshInterval: 5 * time.Minute,
			RequestTimeout:  3 * time.Second,
			CacheTTL:        5 * time.Minute,
		}},
	}
}
