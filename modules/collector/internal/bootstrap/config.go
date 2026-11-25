// Package bootstrap loads configuration and wires the moox-collector service process.
package bootstrap

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/cloud"
	"github.com/mooyang-code/moox/modules/collector/internal/cloud/tencentscf"
	"github.com/mooyang-code/moox/modules/collector/internal/eventbus"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/subjectsync"
	"github.com/mooyang-code/moox/packages/cnstock"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
	"github.com/mooyang-code/moox/packages/storagepolicy"
	"gopkg.in/yaml.v3"
)

// Config is the root collector control-plane configuration.
type Config struct {
	GatewayClient gatewayclient.FileConfig         `yaml:"gateway_client"`
	sourcePath    string                           `yaml:"-"`
	Database      DatabaseConfig                   `yaml:"database"`
	SCFEntry      gatewayclient.ExternalFileConfig `yaml:"scf_entry"`
	// SCFRegions 是各地域的函数网络；SCFMarkets 是每个空间、市场使用的地域；SCFPlan 是函数部署参数。
	SCFRegions         map[string]fetchplan.PlanNetwork `yaml:"scf_regions"`
	SCFMarkets         map[string]map[string][]string   `yaml:"scf_markets"`
	SCFPlan            SCFPlanConfig                    `yaml:"scf_plan"`
	CollectorRetention CollectorRetentionConfig         `yaml:"collector_retention"`
	KlineResample      KlineResampleConfig              `yaml:"kline_resample"`
	// EventBus 是 Collector 到 EventBus 的连接，K 线合成与下架检测共用；不配置时不发布任何事件，合成只靠兜底检查。
	EventBus    eventbus.Config   `yaml:"event_bus"`
	EgressProxy EgressProxyConfig `yaml:"egress_proxy"`
	// Delist 是下架检测的开关，安装时按拓扑渲染：没有出口代理（没配海外机）时关掉自动检测。
	Delist      DelistConfig       `yaml:"delist"`
	SubjectSync subjectsync.Config `yaml:"subject_sync"`
	// EgressLimits 是 Collector 进程直连外部数据源时的主动限流与冷却；目前只有东方财富，都有代码默认值，配置里可以不写。
	EgressLimits EgressLimitsConfig `yaml:"egress_limits"`
	// Stream 是 WS 推送采集的 Collector 侧参数（截止、提交预留随计划下发给 collector-stream）。
	Stream StreamConfig `yaml:"stream"`
	// Cloud 是 Collector 直接调腾讯云的参数：云函数接口限频与日志服务（CLS）开通。
	Cloud CloudConfig `yaml:"cloud"`
	// PlanHealth 是采集计划协调的告警线。
	PlanHealth PlanHealthConfig `yaml:"plan_health"`
	// HistorySeed 是数据集首次接入时的历史补齐参数，都有代码默认值，配置里可以不写。
	HistorySeed HistorySeedConfig `yaml:"history_seed"`
}

// PlanHealthConfig 是采集计划协调的告警线（关注项 fetch_coordination_failing）：最近一次对账成功超过 StaleAfter，
// 或协调失败、且进行中的协调已超过 PendingGrace 时上报。
type PlanHealthConfig struct {
	StaleAfter   time.Duration `yaml:"stale_after"`
	PendingGrace time.Duration `yaml:"pending_grace"`
}

func (c PlanHealthConfig) validate() error {
	if c.StaleAfter <= 0 || c.PendingGrace <= 0 {
		return fmt.Errorf("plan_health: stale_after 与 pending_grace 必须大于 0")
	}
	return nil
}

// HistorySeedConfig 是历史补齐的参数：数据集首次进入采集计划时，把历史补到保留期与行情源都允许的深度；补齐期间下游不生效。
type HistorySeedConfig struct {
	// Enabled 不写时开启。
	Enabled *bool `yaml:"enabled"`
	// MaxDepth 是补齐深度的上限（保留期为 forever 的频率兜底），默认 365d。行情源能取到的最长历史按行情源目录的声明，不在这里配。
	MaxDepth string `yaml:"max_depth"`
	// SubjectsPerJob 是一个补齐作业最多放的标的数，默认 500。
	SubjectsPerJob int `yaml:"subjects_per_job"`
	// MergeSessions 为真（默认）时日内分块跨休市区间合并，函数调用次数少一个数量级；行情源取数出问题时可以关掉退回按交易时段拆。
	MergeSessions *bool `yaml:"merge_sessions"`
	// MaxRowsPerSecond 是补齐写 Storage 的全局速率上限（给实时写入留余量），默认 10000；
	// LocalMaxRowsPerSecond 是本地执行端（数据经海外机）的上限，默认 1000。
	MaxRowsPerSecond      float64 `yaml:"max_rows_per_second"`
	LocalMaxRowsPerSecond float64 `yaml:"local_max_rows_per_second"`
}

func (c HistorySeedConfig) enabled() bool { return c.Enabled == nil || *c.Enabled }

func (c HistorySeedConfig) mergeSessions() bool { return c.MergeSessions == nil || *c.MergeSessions }

func (c HistorySeedConfig) maxDepth() time.Duration {
	return seedDuration(c.MaxDepth, 365*24*time.Hour)
}

func seedDuration(raw string, fallback time.Duration) time.Duration {
	period, err := storagepolicy.ParsePeriod(strings.TrimSpace(raw))
	if err != nil || period.Forever || period.Duration <= 0 {
		return fallback
	}
	return period.Duration
}

func (c *Config) validateHistorySeed() error {
	seed := c.HistorySeed
	if seed.MaxDepth != "" {
		if period, err := storagepolicy.ParsePeriod(strings.TrimSpace(seed.MaxDepth)); err != nil || period.Forever || period.Duration <= 0 {
			return fmt.Errorf("history_seed.max_depth %q 必须是时长，例如 365d", seed.MaxDepth)
		}
	}
	if seed.SubjectsPerJob < 0 || seed.MaxRowsPerSecond < 0 || seed.LocalMaxRowsPerSecond < 0 {
		return fmt.Errorf("history_seed 的数量与速率不能为负")
	}
	return nil
}

// CloudConfig 是 Collector 直接调腾讯云的参数。
type CloudConfig struct {
	TencentSCF TencentSCFConfig `yaml:"tencent_scf"`
	// CLS 是创建或更新采集函数时自动开通日志服务、补齐上报环境的配置；开通失败只记日志，不影响函数创建。
	CLS cloud.CLSConfig `yaml:"cls"`
}

// TencentSCFConfig 是云函数接口的参数。
type TencentSCFConfig struct {
	// APIRateLimits 按接口覆盖本地限频（次/秒），不能超过官方限频；不写时用官方默认值。
	APIRateLimits map[string]float64 `yaml:"api_rate_limits"`
}

func (c CloudConfig) validate() error {
	if err := tencentscf.ValidateRateLimits(c.TencentSCF.APIRateLimits); err != nil {
		return fmt.Errorf("cloud.tencent_scf.api_rate_limits: %w", err)
	}
	if err := c.CLS.Validate(); err != nil {
		return fmt.Errorf("cloud.%w", err)
	}
	return nil
}

// EgressLimitsConfig 是各外部数据源的限流参数。
type EgressLimitsConfig struct {
	Eastmoney cnstock.RequestLimitConfig `yaml:"eastmoney"`
}

// StreamConfig 是推送采集的参数。没有 REST 兜底，也没有兜底相关的配置。
type StreamConfig struct {
	// HeartbeatTimeoutSeconds 是 collector-stream 心跳的超时，只用于运行状态、告警与切换前检查。
	HeartbeatTimeoutSeconds int `yaml:"heartbeat_timeout_seconds"`
	// LatenessS 是推送方式按频率的截止默认值（任务显式指定的以任务为准）。
	LatenessS map[string]int `yaml:"lateness_s"`
	// CommitReserveMS 是周期闸门的提交预留：最晚开始提交 = 收盘 + 截止 − 预留，要容得下同一截止的全部数据集串行提交完。
	CommitReserveMS int `yaml:"commit_reserve_ms"`
}

func defaultStreamConfig() StreamConfig {
	return StreamConfig{HeartbeatTimeoutSeconds: 180, LatenessS: map[string]int{"1m": 5, "1h": 8}, CommitReserveMS: 1000}
}

func (c StreamConfig) validate() error {
	if c.HeartbeatTimeoutSeconds < 30 || c.CommitReserveMS < 100 || c.CommitReserveMS > 5000 {
		return fmt.Errorf("stream: heartbeat_timeout_seconds 至少 30，commit_reserve_ms 在 100～5000 之间")
	}
	for frequency, seconds := range c.LatenessS {
		if seconds < 2 || seconds > 3600 || seconds*1000 <= c.CommitReserveMS {
			return fmt.Errorf("stream.lateness_s[%s] 必须在 2～3600 秒之间且大于提交预留", frequency)
		}
	}
	return nil
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

// SCFEntryEndpoint is the fixed endpoint selected for one function region.
// The publisher supplies private endpoints only after verifying its VPC route.
type SCFEntryEndpoint struct {
	Address string `yaml:"entry_address"`
	Host    string `yaml:"entry_host"`
}

// CollectorRetentionConfig bounds the process-level execution-history cleanup.
type CollectorRetentionConfig struct {
	MaintenanceInterval string `yaml:"maintenance_interval"`
	MaintenanceOffset   string `yaml:"maintenance_offset"`
	MaintenanceTimeout  string `yaml:"maintenance_timeout"`
	MaxRowsPerPass      int    `yaml:"max_rows_per_pass"`
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

// KlineResampleConfig 是本地 K 线合成的进程级执行策略。任务身份与源/目标语义仍在 CollectionTask 里。
type KlineResampleConfig struct {
	Enabled bool `yaml:"enabled"`
	// ScanTimeout 是定时器每一步（准备、兜底检查）的预算。
	ScanTimeout time.Duration `yaml:"scan_timeout"`
	// ExecTimeout 是按桶执行（读源到写完）的硬超时；补采触发的作业会推迟 2×ExecTimeout 再开始。
	ExecTimeout time.Duration `yaml:"exec_timeout"`
	// FallbackLookbackBars 是兜底检查每个任务查看的最近已收盘目标 K 线根数。
	FallbackLookbackBars int `yaml:"fallback_lookback_bars"`
	// FallbackGrace 是兜底检查给事件主路径留出的收盘后时间。
	FallbackGrace time.Duration `yaml:"fallback_grace"`
	// FallbackMaxBuckets 是一次兜底检查最多补多少个桶。
	FallbackMaxBuckets int `yaml:"fallback_max_buckets"`
	// HistoryBucketsPerRound 是历史合成作业每轮最多推进的桶数；HistoryMaxJobs 是每轮最多推进的作业数。
	HistoryBucketsPerRound int `yaml:"history_buckets_per_round"`
	HistoryMaxJobs         int `yaml:"history_max_jobs"`
	// MaxReadKeys 是单次读取源/目标行的键数上限。
	MaxReadKeys int `yaml:"max_read_keys"`
}

// DelistConfig 控制下架检测。AutoDetect 为 false 时不轮询币安现货、币安 U 本位、OKX 永续三个来源，
// 手工登记（collector delist register）照常可用。默认 true：配置里没写时保持自动检测。
type DelistConfig struct {
	AutoDetect bool `yaml:"auto_detect"`
}

// EgressProxyConfig 是经共享网关走出口代理的 HTTP 域名。
type EgressProxyConfig struct {
	Domains []string `yaml:"domains"`
}

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
	// YAML 解码会整体替换 map 里的元素：部署渲染的空间只写包名与命名空间，空间专属的默认参数（stockcn 的每函数标的数估算、
	// 取数时刻）要在解码后按字段补回，配置里写了的字段优先。
	cfg.SCFPlan.mergeSpaceDefaults(defaultSCFPlan().Spaces)
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
	if err := cfg.SubjectSync.Validate(); err != nil {
		return nil, fmt.Errorf("subject_sync: %w", err)
	}
	if err := cfg.EgressLimits.Eastmoney.Validate(); err != nil {
		return nil, fmt.Errorf("egress_limits.eastmoney: %w", err)
	}
	if err := cfg.validateSCFEntry(); err != nil {
		return nil, err
	}
	if err := cfg.Stream.validate(); err != nil {
		return nil, err
	}
	if err := cfg.Cloud.validate(); err != nil {
		return nil, err
	}
	if err := cfg.PlanHealth.validate(); err != nil {
		return nil, err
	}
	if err := cfg.validateSCFPlan(); err != nil {
		return nil, err
	}
	if err := cfg.validateKlineResample(); err != nil {
		return nil, err
	}
	if err := cfg.validateCollectorRetention(); err != nil {
		return nil, err
	}
	if err := cfg.validateHistorySeed(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyEnv() {
	if v := os.Getenv("MOOX_COLLECTOR_DB_PATH"); v != "" {
		c.Database.Path = v
	}
	// 日志保留天数由部署工具按 moox.toml 的 [logging] retention_days 下发；只在创建日志主题时生效。
	if v := strings.TrimSpace(os.Getenv("MOOX_CLS_RETENTION_DAYS")); v != "" {
		if days, err := strconv.ParseInt(v, 10, 64); err == nil && days > 0 {
			c.Cloud.CLS.RetentionDays = days
		}
	}
}

func (c *Config) validateSCFEntry() error {
	if c.SCFEntry.Caller != "scf-collector" {
		return fmt.Errorf("scf_entry.caller must be scf-collector")
	}
	if err := c.SCFEntry.Validate(); err != nil {
		return fmt.Errorf("scf_entry: %w", err)
	}
	return nil
}

func (c *Config) validateEgressProxy() error {
	if _, err := domainpolicy.New(c.EgressProxy.Domains); err != nil {
		return fmt.Errorf("egress_proxy.domains: %w", err)
	}
	return nil
}

func (c *Config) validateKlineResample() error {
	k := c.KlineResample
	if k.ScanTimeout <= 0 || k.ExecTimeout <= 0 || k.FallbackGrace <= 0 {
		return fmt.Errorf("kline_resample durations must be positive")
	}
	if k.FallbackLookbackBars <= 0 || k.FallbackLookbackBars > 100 || k.FallbackMaxBuckets <= 0 || k.HistoryBucketsPerRound <= 0 || k.HistoryMaxJobs <= 0 || k.MaxReadKeys <= 0 {
		return fmt.Errorf("kline_resample quantities are invalid")
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
	if offsetErr != nil || timeoutErr != nil || fetchplan.ValidateMaintenanceSchedule(interval, offset, timeout) != nil {
		return fmt.Errorf("collector_retention.maintenance_offset and maintenance_timeout must keep each pass inside one interval: 0 <= offset < interval and offset + timeout <= interval")
	}
	if retention.MaxRowsPerPass < 1 || retention.MaxRowsPerPass > 50000 {
		return fmt.Errorf("collector_retention.max_rows_per_pass must be between 1 and 50000")
	}
	return nil
}

// Default returns safe local defaults.
func Default() *Config {
	return &Config{
		GatewayClient: gatewayclient.FileConfig{Caller: "collector", KeyFile: "../../secrets/host.key"},
		Database: DatabaseConfig{
			Type:            "sqlite",
			Path:            "./data/moox_collector.db",
			MaxIdleConns:    1,
			MaxOpenConns:    1,
			ConnMaxLifetime: time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		},
		SCFPlan:      defaultSCFPlan(),
		SubjectSync:  subjectsync.DefaultConfig(),
		EgressLimits: EgressLimitsConfig{Eastmoney: cnstock.DefaultRequestLimitConfig()},
		Stream:       defaultStreamConfig(),
		Cloud:        CloudConfig{CLS: cloud.DefaultCLSConfig()},
		PlanHealth:   PlanHealthConfig{StaleAfter: 15 * time.Minute, PendingGrace: 5 * time.Minute},
		Delist:       DelistConfig{AutoDetect: true},
		SCFEntry:     gatewayclient.ExternalFileConfig{Address: "entry.example.test:11004", Host: "storage", Caller: "scf-collector", KeyFile: "../../secrets/scf-collector.key"},
		CollectorRetention: CollectorRetentionConfig{
			MaintenanceInterval: "1m", MaintenanceOffset: "35s", MaintenanceTimeout: "20s", MaxRowsPerPass: 50000,
		},
		KlineResample: KlineResampleConfig{
			Enabled: false, ScanTimeout: 30 * time.Second, ExecTimeout: 30 * time.Second,
			FallbackLookbackBars: 3, FallbackGrace: time.Minute, FallbackMaxBuckets: 200,
			HistoryBucketsPerRound: 20, HistoryMaxJobs: 8, MaxReadKeys: 5000,
		},
		EgressProxy: EgressProxyConfig{Domains: []string{"*.binance.com", "data-api.binance.vision", "openapi.okx.com"}},
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
