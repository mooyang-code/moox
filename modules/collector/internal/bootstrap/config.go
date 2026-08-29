// Package bootstrap loads configuration and wires the moox-collector service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"
)

// Config is the root collector control-plane configuration.
type Config struct {
	Database        DatabaseConfig        `yaml:"database"`
	CloudNode       CloudNodeConfig       `yaml:"cloudnode"`
	Storage         StorageConfig         `yaml:"storage"`
	PeriodReadiness PeriodReadinessConfig `yaml:"period_readiness"`
	KlineResample   KlineResampleConfig   `yaml:"kline_resample"`
	SysDeploy       SysDeployConfig       `yaml:"sysdeploy"`
	Health          HealthConfig          `yaml:"health"`
	DNS             DNSConfig             `yaml:"dns"`
	DNSResolver     DNSResolverConfig     `yaml:"dns_resolver"`
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

// StorageConfig describes storage service addresses.
type StorageConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

// PeriodReadinessConfig controls the durable Collector period completion
// projection and its retry/retention loops.
type PeriodReadinessConfig struct {
	Grace           time.Duration `yaml:"grace"`
	ReportInterval  time.Duration `yaml:"report_interval"`
	ItemRetention   int           `yaml:"item_retention"`
	ParentRetention time.Duration `yaml:"parent_retention"`
}

// KlineResampleConfig controls the local derived-kline scheduler. Rule
// identity and source/target semantics remain in TaskRule; these values are
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
	TargetKeepDuration          time.Duration `yaml:"target_keep_duration"`
}

// SysDeployConfig describes optional dependency discovery through admin SysDeploy.
type SysDeployConfig struct {
	AdminGatewayURL string            `yaml:"admin_gateway_url"`
	ServiceAuth     ServiceAuthConfig `yaml:"service_auth"`
}

// ServiceAuthConfig describes backend HMAC auth for /api/service calls.
type ServiceAuthConfig struct {
	AccessKey     string `yaml:"access_key"`
	SecretKey     string `yaml:"secret_key"`
	TargetNode    string `yaml:"target_node"`
	CAFile        string `yaml:"ca_file"`
	CAPEMBase64   string `yaml:"ca_pem_base64"`
	ExpireSeconds int64  `yaml:"expire_seconds"`
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

// DNSResolverConfig selects the optional Trade-side resolver. The native
// Gateway target and node are rendered from custom.toml by moox-cli.
type DNSResolverConfig struct {
	Enabled         bool          `yaml:"enabled"`
	Target          string        `yaml:"target"`
	NodeID          string        `yaml:"node_id"`
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
	if err := cfg.validateStorageTargets(); err != nil {
		return nil, err
	}
	if err := cfg.validateDNSResolver(); err != nil {
		return nil, err
	}
	if err := cfg.validateKlineResample(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_COLLECTOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_ADMIN_GATEWAY_URL"); v != "" {
		c.SysDeploy.AdminGatewayURL = v
	}
	if v := os.Getenv("MOOX_GATEWAY_NODE_ID"); v != "" {
		c.SysDeploy.ServiceAuth.TargetNode = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"); v != "" {
		c.SysDeploy.ServiceAuth.AccessKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"); v != "" {
		c.SysDeploy.ServiceAuth.SecretKey = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_FILE"); v != "" {
		c.SysDeploy.ServiceAuth.CAFile = v
	}
	if v := os.Getenv("MOOX_GATEWAY_CA_PEM_B64"); v != "" {
		c.SysDeploy.ServiceAuth.CAPEMBase64 = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET"); v != "" {
		c.Storage.GatewayTarget = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID"); v != "" {
		c.Storage.GatewayNodeID = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_KEY_ID"); v != "" {
		c.Storage.KeyID = v
	}
	if v := os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_HMAC_KEY_FILE"); v != "" {
		c.Storage.HMACKeyFile = v
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
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_TARGET"); v != "" {
		c.DNSResolver.Target = strings.TrimSpace(v)
	}
	if v := os.Getenv("MOOX_COLLECTOR_DNS_RESOLVER_NODE_ID"); v != "" {
		c.DNSResolver.NodeID = strings.TrimSpace(v)
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
	if strings.TrimSpace(c.Storage.HMACKeyFile) != "" {
		if _, err := gatewayauth.CredentialsFromKeyFile(c.Storage.KeyID, c.Storage.HMACKeyFile); err != nil {
			return fmt.Errorf("storage hmac credentials: %w", err)
		}
	}
	return nil
}

func (c *Config) validateDNSResolver() error {
	if !c.DNSResolver.Enabled {
		return nil
	}
	if !isPublicDNSResolverTarget(c.DNSResolver.Target) {
		return fmt.Errorf("dns_resolver.target must be an ip://public-ip:port tRPC target, got %q", c.DNSResolver.Target)
	}
	if strings.TrimSpace(c.DNSResolver.NodeID) == "" {
		return fmt.Errorf("dns_resolver.node_id is required when enabled")
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
	if c.KlineResample.ScanTimeout <= 0 || c.KlineResample.WorkerJobTimeout <= 0 || c.KlineResample.WorkerPollInterval <= 0 || c.KlineResample.StaleRunningAfter <= 0 || c.KlineResample.DefaultSettleDelay < 0 || c.KlineResample.TargetKeepDuration <= 0 {
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

func isPublicDNSResolverTarget(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "ip" || parsed.User != nil || parsed.Hostname() == "" || parsed.Port() == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	ip := net.ParseIP(parsed.Hostname())
	return isPublicResolverIP(ip)
}

func isPublicResolverIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	first, second, third := ip[0], ip[1], ip[2]
	return first != 0 && first < 224 &&
		!(first == 100 && second >= 64 && second <= 127) &&
		!(first == 192 && second == 0 && (third == 0 || third == 2)) &&
		!(first == 198 && (second == 18 || second == 19 || (second == 51 && third == 100))) &&
		!(first == 203 && second == 0 && third == 113)
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
		Storage: StorageConfig{GatewayTarget: "ip://127.0.0.1:11003"},
		PeriodReadiness: PeriodReadinessConfig{
			Grace: 2 * time.Minute, ReportInterval: 5 * time.Second,
			ItemRetention: 60, ParentRetention: 7 * 24 * time.Hour,
		},
		KlineResample: KlineResampleConfig{
			Enabled: true, ScanTimeout: 30 * time.Second, WorkerConcurrency: 2, MaxClaimsPerTick: 100,
			WorkerSubjectBatchSize: 50, WorkerJobTimeout: 30 * time.Second,
			WorkerPollInterval: 5 * time.Second, WorkerMaxSourceKeysPerClaim: 20000,
			StaleRunningAfter: 2 * time.Minute, DefaultSettleDelay: 10 * time.Second,
			RepairLookbackBuckets: 3, TargetKeepDuration: 4320 * time.Hour,
		},
		SysDeploy: SysDeployConfig{
			ServiceAuth: ServiceAuthConfig{ExpireSeconds: 60},
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
