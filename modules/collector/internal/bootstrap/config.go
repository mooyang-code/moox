// Package bootstrap loads configuration and wires the moox-collector service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gopkg.in/yaml.v3"
)

// Config is the root collector control-plane configuration.
type Config struct {
	// GatewayClient 是 Collector 调用其他组件（Storage、交易服务等）使用的 gatewayclient 配置。
	GatewayClient       gatewayclient.Config     `yaml:"gateway_client"`
	SCFRegionBlacklists map[string][]string      `yaml:"scf_region_blacklists"`
	Database            DatabaseConfig           `yaml:"database"`
	CloudNode           CloudNodeConfig          `yaml:"cloudnode"`
	Storage             StorageConfig            `yaml:"storage"`
	CollectorRuntime    CollectorRuntimeConfig   `yaml:"collector_runtime"`
	CollectorRetention  CollectorRetentionConfig `yaml:"collector_retention"`
	StockCN             StockCNConfig            `yaml:"stockcn"`
	PeriodReadiness     PeriodReadinessConfig    `yaml:"period_readiness"`
	KlineResample       KlineResampleConfig      `yaml:"kline_resample"`
	Health              HealthConfig             `yaml:"health"`
	DNS                 DNSConfig                `yaml:"dns"`
	DNSResolver         DNSResolverConfig        `yaml:"dns_resolver"`
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

// StorageConfig 描述 Storage 相关的设置。Collector 自己访问 Storage 走 gateway_client；
// GatewayTarget、GatewayNodeID 只用来确定写入 SCF 调用载荷的 Storage 网关。
type StorageConfig struct {
	GatewayTarget    string `yaml:"gateway_target"`
	GatewayNodeID    string `yaml:"gateway_node_id"`
	ResultDataNodeID string `yaml:"result_data_node_id"`
}

// CollectorRuntimeConfig overrides the native Gateway endpoint used to reach
// the Collector host that owns MarketFetchRuntime. Both values are required
// together; Storage routing is never a fallback for runtime claims.
type CollectorRuntimeConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	NodeID        string `yaml:"node_id"`
}

// Dependencies 是写入 SCF 调用载荷的网关地址，取自配置。Collector 自己调用其他组件都走 gatewayclient。
type Dependencies struct {
	// CollectorRuntimeGatewayTarget、CollectorRuntimeGatewayNodeID 是 SCF 领取 Timer 批次时访问的网关。
	CollectorRuntimeGatewayTarget string
	CollectorRuntimeGatewayNodeID string
	// InvokeStorageRPCGatewayTarget 是 SCF 写入 Storage 时访问的网关。
	InvokeStorageRPCGatewayTarget string
}

// SCFTargets 返回写入 SCF 调用载荷的网关地址。
func (c *Config) SCFTargets() Dependencies {
	return Dependencies{
		CollectorRuntimeGatewayTarget: strings.TrimSpace(c.CollectorRuntime.GatewayTarget),
		CollectorRuntimeGatewayNodeID: strings.TrimSpace(c.CollectorRuntime.NodeID),
		InvokeStorageRPCGatewayTarget: strings.TrimSpace(c.Storage.GatewayTarget),
	}
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

// DNSResolverConfig 选择可选的交易服务侧 DNS 解析，经 gateway_client 调用交易服务。
type DNSResolverConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Domains         []string      `yaml:"domains"`
	RefreshInterval time.Duration `yaml:"refresh_interval"`
	RequestTimeout  time.Duration `yaml:"request_timeout"`
	CacheTTL        time.Duration `yaml:"cache_ttl"`
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
	if err := cfg.validateStorageTargets(); err != nil {
		return nil, err
	}
	if err := cfg.validateDNSResolver(); err != nil {
		return nil, err
	}
	if err := cfg.validateCollectorRuntime(); err != nil {
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
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET"); v != "" {
		c.Storage.GatewayTarget = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID"); v != "" {
		c.Storage.GatewayNodeID = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_RUNTIME_GATEWAY_TARGET"); v != "" {
		c.CollectorRuntime.GatewayTarget = strings.TrimSpace(v)
	}
	if v := os.Getenv("MOOX_COLLECTOR_RUNTIME_NODE_ID"); v != "" {
		c.CollectorRuntime.NodeID = strings.TrimSpace(v)
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
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_ENABLED"); v != "" {
		c.DNSResolver.Enabled = strings.EqualFold(strings.TrimSpace(v), "1") || strings.EqualFold(strings.TrimSpace(v), "true")
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_DOMAINS"); v != "" {
		c.DNSResolver.Domains = splitCSV(v)
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_REFRESH_INTERVAL"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			c.DNSResolver.RefreshInterval = parsed
		}
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_TIMEOUT"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			c.DNSResolver.RequestTimeout = parsed
		}
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_CACHE_TTL"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			c.DNSResolver.CacheTTL = parsed
		}
	}
}

func (c *Config) validateStorageTargets() error {
	if !isStorageTRPCTarget(c.Storage.GatewayTarget) {
		return fmt.Errorf("storage.gateway_target must be a tRPC target, got %q", c.Storage.GatewayTarget)
	}
	return nil
}

func (c *Config) validateCollectorRuntime() error {
	target := strings.TrimSpace(c.CollectorRuntime.GatewayTarget)
	nodeID := strings.TrimSpace(c.CollectorRuntime.NodeID)
	if (target == "") != (nodeID == "") {
		if target == "" {
			return fmt.Errorf("collector_runtime.gateway_target and collector_runtime.node_id must be configured together")
		}
		return fmt.Errorf("collector_runtime.node_id is required when gateway_target is configured")
	}
	if target != "" && !isStorageTRPCTarget(target) {
		return fmt.Errorf("collector_runtime.gateway_target must be a native tRPC target, got %q", target)
	}
	return nil
}

func (c *Config) validateDNSResolver() error {
	if !c.DNSResolver.Enabled {
		return nil
	}
	if len(c.DNSResolver.Domains) == 0 {
		return fmt.Errorf("dns_resolver.domains must not be empty when enabled")
	}
	if len(c.DNSResolver.Domains) > 16 {
		return fmt.Errorf("dns_resolver supports at most 16 domains")
	}
	seen := make(map[string]struct{}, len(c.DNSResolver.Domains))
	for _, raw := range c.DNSResolver.Domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
		if !validDNSResolverDomain(domain) {
			return fmt.Errorf("dns_resolver domain %q is invalid", raw)
		}
		if _, exists := seen[domain]; exists {
			return fmt.Errorf("dns_resolver domain %q is duplicated", raw)
		}
		seen[domain] = struct{}{}
	}
	if c.DNSResolver.RefreshInterval <= 0 || c.DNSResolver.RequestTimeout <= 0 || c.DNSResolver.CacheTTL <= 0 {
		return fmt.Errorf("dns_resolver intervals must be positive")
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

func isStorageTRPCTarget(raw string) bool {
	raw = strings.TrimSpace(strings.ToLower(raw))
	return raw != "" && !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://")
}

func validDNSResolverDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || net.ParseIP(domain) != nil || strings.Contains(domain, "..") {
		return false
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
				return false
			}
		}
	}
	return true
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
		Storage: StorageConfig{GatewayTarget: "ip://127.0.0.1:11003", ResultDataNodeID: "storage-node-0"},
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
		DNSResolver: DNSResolverConfig{
			RefreshInterval: 5 * time.Minute,
			RequestTimeout:  3 * time.Second,
			CacheTTL:        5 * time.Minute,
		},
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
