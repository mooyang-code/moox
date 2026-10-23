package config

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"

	"github.com/BurntSushi/toml"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/security/domainpolicy"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

const (
	manifestName = "moox.toml"
	maxFileSize  = 1 << 20
	// The cloud disk mounted at /data is the canonical runtime volume. Keep all
	// generated packages, state, logs and credentials below this root so a
	// host's small system disk is not consumed by MooX.
	DefaultDeployRoot  = "/data/moox"
	DefaultControlRoot = "/data/moox/prod"
	DefaultStorageRoot = "/data/moox/storage"
	// SCFCLSReserveMilliseconds is injected into every short-lived market SCF.
	// Keep setup validation aligned with the runtime's CLS flush reservation.
	SCFCLSReserveMilliseconds = 3000
	// SCFCompletionReserveMilliseconds leaves enough time for the durable
	// completion event after Storage has accepted the aggregate write.
	SCFCompletionReserveMilliseconds      = 13000
	SCFTimerClaimReserveMilliseconds      = 3000
	SCFColdCompletionReserveMilliseconds  = 13000
	SCFInstrumentNamesReserveMilliseconds = 250
	SCFMetricsResponseReserveMilliseconds = 750
	DefaultSCFTimerTimeoutSeconds         = tencent.CollectorTimerTimeoutSeconds
	// SCFFinalResponseReserveMilliseconds keeps the SCF runtime enough time to
	// serialize and return its response after best-effort CLS logging.
	SCFFinalResponseReserveMilliseconds = 500
	// DefaultCryptoMarketTimerFunctionCount is the historical config name for the
	// built-in crypto market function pool. Crypto now publishes this capacity
	// as Scheduler-owned Invoke functions rather than Tencent Timer functions.
	DefaultCryptoMarketTimerFunctionCount = 60
	// DefaultStockCNMarketTimerFunctionCount is used by callers that need the
	// release baseline. stockcn config validation still requires an explicit
	// timer_function_count instead of applying this value implicitly.
	DefaultStockCNMarketTimerFunctionCount = 170
	// Invoke execution needs a larger budget than stockcn Timer execution for the
	// Storage acknowledgement and durable completion path.
	DefaultStockCNInvokeTimeoutSeconds = 90
	// Synchronized with active Kline providers in stockcn/route.yaml.
	StockCNInvokeProviderChainLength  = 4
	StockCNMaxRealtimeItems           = 40
	DefaultCryptoInvokeTimeoutSeconds = 60
	// Stock Timer groups are spread across this fixed second window. These are
	// release defaults; changing them requires a new rendered configuration.
	DefaultStockCNStaggerStartSecond        = 5
	DefaultStockCNStaggerWindowSeconds      = 35
	DefaultStockCNStaggerMaxStartsPerSecond = 6
)

const (
	// Tencent SCF publishes these as the default account quotas. Keep them in
	// the manifest so release planning does not depend on a hidden allocator
	// constant. An account or namespace may have purchased higher quotas.
	// A region can host MaxNamespacesPerRegion namespaces, and each namespace
	// can host MaxFunctionsPerNamespace functions, so the default regional
	// ceiling is 5 * 50 = 250 functions. A Space uses its primary namespace
	// first and overflow namespaces (moox-<space>-ns2, ...) when one
	// namespace is full.
	DefaultSCFMaxNamespacesPerRegion       = 5
	DefaultSCFMaxFunctionsPerNamespace     = 50
	DefaultSCFMaxBurstConcurrencyPerMinute = 500
)

// Paths controls where setup-cli installs the control and Storage packages.
// Storage may be deployed to a separate host with a different mount layout,
// so StorageRoot is independently validated as an absolute safe path.
// The section is optional: omitted values resolve to the cloud-disk defaults
// above, which keeps older moox.toml files deterministic.
type Paths struct {
	DeployRoot  string `toml:"deploy_root"`
	ControlRoot string `toml:"control_root"`
	StorageRoot string `toml:"storage_root"`
}

// LocalLogs bounds process stdout/stderr and framework log files under each
// deployment root. The host health timer applies this policy once per minute.
type LocalLogs struct {
	MaxSizeMB   int `toml:"max_size_mb" json:"max_size_mb"`
	BackupCount int `toml:"backup_count" json:"backup_count"`
}

func (p Paths) Resolved() Paths {
	if strings.TrimSpace(p.DeployRoot) == "" {
		p.DeployRoot = DefaultDeployRoot
	}
	if strings.TrimSpace(p.ControlRoot) == "" {
		p.ControlRoot = filepath.Join(p.DeployRoot, "prod")
	}
	if strings.TrimSpace(p.StorageRoot) == "" {
		p.StorageRoot = filepath.Join(p.DeployRoot, "storage")
	}
	p.DeployRoot = filepath.Clean(strings.TrimSpace(p.DeployRoot))
	p.ControlRoot = filepath.Clean(strings.TrimSpace(p.ControlRoot))
	p.StorageRoot = filepath.Clean(strings.TrimSpace(p.StorageRoot))
	return p
}

type Admin struct {
	Username string `toml:"username"`
	Password string `toml:"password"`
}

type TencentCloud struct {
	SecretID  string `toml:"secret_id"`
	SecretKey string `toml:"secret_key"`
	Region    string `toml:"region"`
}

type EventBus struct {
	PublicAddress string `toml:"-"`
	Port          int    `toml:"port"`
	TLSEnabled    bool   `toml:"tls_enabled"`
}

type Observability struct {
	DeliverPolicy string `toml:"deliver_policy"`
}

// EgressProxy separates upstream policy from service placement.
type EgressProxy struct {
	HTTPDomains []string  `toml:"http_domains"`
	DNS         EgressDNS `toml:"dns"`
}
type EgressDNS struct {
	RefreshIntervalSeconds int      `toml:"refresh_interval_seconds"`
	RequestTimeoutMS       int      `toml:"request_timeout_ms"`
	LookupTimeoutMS        int      `toml:"lookup_timeout_ms"`
	ProbeTimeoutMS         int      `toml:"probe_timeout_ms"`
	ProbePort              int      `toml:"probe_port"`
	CacheTTLSeconds        int      `toml:"cache_ttl_seconds"`
	MaxIPsPerDomain        int      `toml:"max_ips_per_domain"`
	Domains                []string `toml:"domains"`
}

// StorageRetention is the [storage_retention] table: how long each
// time-series Dataset keeps its rows, by space and frequency, and how many
// bars a View keeps. An omitted defaults or spaces table takes the
// recommended values from storagepolicy.Default; a given table is used as is,
// so defaults must cover every standard frequency.
type StorageRetention struct {
	ViewBars uint64                       `toml:"view_rows_per_series" json:"view_rows_per_series"`
	Defaults map[string]string            `toml:"defaults" json:"defaults"`
	Spaces   map[string]map[string]string `toml:"spaces" json:"spaces"`
}

// StorageView is the [storage_view] table: the View maintenance schedule
// and the View file size limit.
type StorageView struct {
	MaintenanceCheckInterval string `toml:"maintenance_check_interval" json:"maintenance_check_interval"`
	CapacityCheckInterval    string `toml:"capacity_check_interval" json:"capacity_check_interval"`
	CapacityCheckJitter      string `toml:"capacity_check_jitter" json:"capacity_check_jitter"`
	MaxViewFileBytes         int64  `toml:"max_view_file_bytes" json:"max_view_file_bytes"`
}

// StoragePolicy renders storage-policy.json, the file storage-primary and
// storage-view load at startup.
func (m Manifest) StoragePolicy() storagepolicy.Policy {
	return storagepolicy.Policy{
		Retention: storagepolicy.Retention{Defaults: m.StorageRetention.Defaults, Spaces: m.StorageRetention.Spaces},
		View: storagepolicy.View{
			Bars:                     m.StorageRetention.ViewBars,
			TrimBars:                 storagepolicy.TrimBarsFor(m.StorageRetention.ViewBars),
			MaintenanceCheckInterval: m.StorageView.MaintenanceCheckInterval,
			CapacityCheckInterval:    m.StorageView.CapacityCheckInterval,
			CapacityCheckJitter:      m.StorageView.CapacityCheckJitter,
			MaxViewFileBytes:         m.StorageView.MaxViewFileBytes,
		},
	}
}

// CollectorRetention describes the Collector maintenance worker and terminal
// execution-history retention windows rendered into the Collector config.
type CollectorRetention struct {
	MaintenanceInterval string `toml:"maintenance_interval" json:"maintenance_interval"`
	MaintenanceOffset   string `toml:"maintenance_offset" json:"maintenance_offset"`
	MaintenanceTimeout  string `toml:"maintenance_timeout" json:"maintenance_timeout"`
	MaxRowsPerPass      int    `toml:"max_rows_per_pass" json:"max_rows_per_pass"`
}

// RequireNotification 要求告警推送地址已配置：监控只在页面展示而不推送，等于没有告警，所以初始化和部署 Monitor 前必须填好。
func (m Manifest) RequireNotification() error {
	if strings.TrimSpace(m.Notification.WebhookURL) == "" {
		return fmt.Errorf("config_invalid: notification.webhook_url is required: 请在 moox.toml 的 [notification] 配置 %s 群机器人 webhook，用于告警推送", m.Notification.ChannelType)
	}
	return nil
}

type Notification struct {
	ChannelType string `toml:"channel_type"`
	WebhookURL  string `toml:"webhook_url"`
}

// Host is an execution target projected from one canonical host definition.
// It is not a TOML input or an independently mutable deployment record.
type Host struct {
	Name           string
	Address        string
	PrivateAddress string
	Region         string
	Port           int
	Username       string
	Password       string `json:"-"`
	Provider       string
	TLSMode        string
	// Reachability 为空表示可直达，outbound 表示只能出站。
	Reachability string
	// Label 是主机的中文名（例如“海外机”），登记为 Admin 的主机描述，用于界面展示。
	Label string
}

type SSHConfig struct {
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password" json:"-"`
}

type HostDefinition struct {
	// Label 是主机的中文名，例如“控制机”“存储机”“海外机”“因子机”；英文 ID 是表名里的主机 ID。
	Label          string `toml:"label"`
	Address        string `toml:"address"`
	PrivateAddress string `toml:"private_address"`
	Region         string `toml:"region"`
	Provider       string `toml:"provider"`
	TLSMode        string `toml:"tls_mode"`
	// Reachability：direct（默认）或 outbound（只能出站，例如家庭网络，经 control 网关反向连接）。
	Reachability string    `toml:"reachability"`
	SSH          SSHConfig `toml:"ssh"`
}

// FactorEngineLocal 表示因子引擎运行在执行 moox-cli 的这台电脑上。
const FactorEngineLocal = "local"

// FactorEngineReference 记录因子引擎所在的机器：local，或 [hosts.*] 里的主机 ID。
// 该主机只登记登录方式，不放置组件时不属于 MooX 部署拓扑。
type FactorEngineReference struct {
	Host string `toml:"host"`
}

type SCFCloudAccountConfig struct {
	AccountID          string `toml:"account_id"`
	AccountName        string `toml:"account_name"`
	CredentialSecretID string `toml:"credential_secret_id"`
	AppID              string `toml:"app_id"`
	COSRegion          string `toml:"cos_region"`
	COSBucket          string `toml:"cos_bucket"`
}

// FactorSetup describes the factor definitions (YAML factor library files),
// the dataset-backed sets and which sets each factor joins, imported by `moox-cli setup init` or
// `setup factors`. Definitions are global; members bind a definition to a set.
type FactorSetup struct {
	Enabled     bool                    `toml:"enabled"`
	SourceDir   string                  `toml:"source_dir"`
	Sets        []FactorSetupSet        `toml:"sets"`
	Definitions []FactorSetupDefinition `toml:"definitions"`
	Members     []FactorSetupMember     `toml:"members"`
}

// FactorSetupSet identifies the source Dataset and subject scope shared by a
// group of factor members.
type FactorSetupSet struct {
	SpaceID         string   `toml:"space_id"`
	SourceDatasetID string   `toml:"source_dataset_id"`
	Freq            string   `toml:"freq"`
	SubjectMode     string   `toml:"subject_mode"`
	Subjects        []string `toml:"subjects"`
}

// FactorSetupDefinition 引用 YAML 因子库文件里的一个定义（docs/计划/因子表达式引擎详细设计.md 9.5）：
// 展示名、表达式都来自库文件，类型、输入列、输出列与回看由 FactorMgr 编译得到。定义不含数据集、频率和状态。
type FactorSetupDefinition struct {
	FactorID string `toml:"factor_id"`
	File     string `toml:"file"`
}

// FactorSetupMember adds one definition to one set (identified by
// source_dataset_id and freq) with the target run status.
type FactorSetupMember struct {
	SourceDatasetID string `toml:"source_dataset_id"`
	Freq            string `toml:"freq"`
	FactorID        string `toml:"factor_id"`
	Status          string `toml:"status"`
}

type Manifest struct {
	Admin              Admin                     `toml:"admin"`
	TencentCloud       TencentCloud              `toml:"tencent_cloud"`
	EventBus           EventBus                  `toml:"eventbus"`
	Paths              Paths                     `toml:"paths"`
	EgressProxy        EgressProxy               `toml:"egress_proxy"`
	Placements         map[string][]string       `toml:"placements"`
	StorageRetention   StorageRetention          `toml:"storage_retention"`
	StorageView        StorageView               `toml:"storage_view"`
	CollectorRetention CollectorRetention        `toml:"collector_retention"`
	LocalLogs          LocalLogs                 `toml:"local_logs"`
	Observability      Observability             `toml:"observability"`
	Notification       Notification              `toml:"notification"`
	Factors            FactorSetup               `toml:"factors"`
	SCFPlan            SCFPlan                   `toml:"scf_plan"`
	HostCatalog        map[string]HostDefinition `toml:"hosts"`
	FactorEngineRef    FactorEngineReference     `toml:"factor_engine"`
}

type Snapshot struct {
	Manifest Manifest
	// Encrypted 表示磁盘上的 moox.toml 是加密保存的。
	Encrypted bool
	path      string
	info      os.FileInfo
	digest    [sha256.Size]byte
}

func Load(path, repositoryRoot string) (*Snapshot, error) {
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: resolve moox.toml path")
	}
	resolvedRoot, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: resolve repository root")
	}
	expectedPath := filepath.Join(filepath.Clean(resolvedRoot), manifestName)
	if filepath.Clean(resolvedPath) != expectedPath {
		if filepath.Base(resolvedPath) != manifestName {
			return nil, fmt.Errorf("config_invalid: setup file must be named moox.toml")
		}
		return nil, fmt.Errorf("config_invalid: moox.toml must be in repository root")
	}

	info, raw, err := readSecureFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	plain, encrypted, err := Plaintext(raw)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := decodeStrict(plain, &manifest); err != nil {
		return nil, err
	}
	return &Snapshot{
		Manifest:  manifest,
		Encrypted: encrypted,
		path:      resolvedPath,
		info:      info,
		digest:    sha256.Sum256(raw),
	}, nil
}

// Plaintext 返回 moox.toml 的明文：加密保存的用本机密钥解开，明文原样返回。
func Plaintext(raw []byte) (plain []byte, encrypted bool, err error) {
	if !configcrypt.IsEncrypted(raw) {
		return raw, false, nil
	}
	plain, err = configcrypt.Decrypt(raw, configcrypt.Default())
	if err != nil {
		return nil, true, fmt.Errorf("config_invalid: %w", err)
	}
	return plain, true, nil
}

// Parse 按与 Load 相同的严格解码和校验解析 moox.toml 的内容，不读取文件，也不检查文件名与权限。
// 生成配置的工具用它保证写出的文件一定能被 CLI 自己接受。
func Parse(raw []byte) (Manifest, error) {
	if len(raw) > maxFileSize {
		return Manifest{}, fmt.Errorf("config_invalid: moox.toml exceeds 1 MiB")
	}
	var manifest Manifest
	if err := decodeStrict(raw, &manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func (s *Snapshot) VerifyUnchanged() error {
	info, raw, err := readSecureFile(s.path)
	if err != nil {
		return fmt.Errorf("config_changed: moox.toml security or identity changed")
	}
	if !os.SameFile(s.info, info) || s.digest != sha256.Sum256(raw) {
		return fmt.Errorf("config_changed: moox.toml changed during command")
	}
	return nil
}

func readSecureFile(path string) (os.FileInfo, []byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: moox.toml is not readable")
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("config_insecure: moox.toml must be a regular file")
	}
	if linkInfo.Mode().Perm() != 0o600 {
		return nil, nil, fmt.Errorf("config_insecure: moox.toml must have mode 0600")
	}
	if !ownedByCurrentUser(linkInfo) {
		return nil, nil, fmt.Errorf("config_insecure: moox.toml must be owned by the current user")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: moox.toml is not readable")
	}
	defer f.Close()
	openInfo, err := f.Stat()
	if err != nil || !openInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openInfo) {
		return nil, nil, fmt.Errorf("config_insecure: moox.toml must be a stable regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: moox.toml is not readable")
	}
	if len(raw) > maxFileSize {
		return nil, nil, fmt.Errorf("config_invalid: moox.toml exceeds 1 MiB")
	}
	return openInfo, raw, nil
}

func decodeStrict(raw []byte, out *Manifest) error {
	md, err := toml.DecodeReader(bytes.NewReader(raw), out)
	if err != nil {
		return fmt.Errorf("config_invalid: decode moox.toml")
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		if first := keys[0].String(); strings.HasPrefix(first, "factors.definitions.") {
			return fmt.Errorf("config_invalid: %s is no longer supported; [[factors.definitions]] accepts only factor_id and file (a YAML factor library file); name, spec and the derived fields come from the library", first)
		}
		if first := keys[0].String(); first == "factors.items" || strings.HasPrefix(first, "factors.items.") {
			return fmt.Errorf("config_invalid: factors.items is no longer supported; split it into [[factors.definitions]] (factor contract) and [[factors.members]] (source_dataset_id, freq, factor_id, status)")
		}
		first := keys[0].String()
		section := string(keys[0][0])
		switch section {
		case "control_host", "storage_host", "view_host", "strategy_host", "other_hosts":
			return fmt.Errorf("config_invalid: %s is retired; use [hosts.<host-id>] with address and ssh, and [placements]", section)
		case "hosts":
			return fmt.Errorf("config_invalid: invalid hosts field; use [hosts.<host-id>] with address and ssh = { port, username, password }")
		case "scf_fetcher":
			return fmt.Errorf("config_invalid: [scf_fetcher] is retired; use [scf_plan] (cloud_account, regions, spaces)")
		case "eventbus":
			if first == "eventbus.host" {
				return fmt.Errorf("config_invalid: eventbus.host is derived from the eventbus component in [placements]")
			}
		case "dns_resolver":
			return fmt.Errorf("config_invalid: dns_resolver is retired; use [egress_proxy.dns] and remove trade_node")
		case "compile_host":
			return fmt.Errorf("config_invalid: [compile_host] is retired; builds run on the local machine (see docs/构建手册.md); remove [compile_host] and any host that was only used for compiling")
		case "factor_engine":
			return fmt.Errorf("config_invalid: factor_engine accepts only host = \"local\" or <host-id>; SSH credentials belong to hosts.<host-id>.ssh")
		}
		return fmt.Errorf("config_invalid: unknown field %s", first)
	}
	if !md.IsDefined("eventbus", "port") {
		out.EventBus.Port = 4222
	}
	if !md.IsDefined("tencent_cloud", "region") {
		out.TencentCloud.Region = "ap-guangzhou"
	}
	for id, definition := range out.HostCatalog {
		definition.Label = strings.TrimSpace(definition.Label)
		definition.Address = strings.TrimSpace(definition.Address)
		definition.PrivateAddress = strings.TrimSpace(definition.PrivateAddress)
		definition.Region = strings.ToLower(strings.TrimSpace(definition.Region))
		definition.Provider = strings.ToLower(strings.TrimSpace(definition.Provider))
		definition.TLSMode = strings.ToLower(strings.TrimSpace(definition.TLSMode))
		definition.Reachability = strings.ToLower(strings.TrimSpace(definition.Reachability))
		definition.SSH.Username = strings.TrimSpace(definition.SSH.Username)
		if !md.IsDefined("hosts", id, "ssh", "port") {
			definition.SSH.Port = 22
		}
		out.HostCatalog[id] = definition
	}
	out.FactorEngineRef.Host = strings.TrimSpace(out.FactorEngineRef.Host)
	out.Paths = out.Paths.Resolved()
	if !md.IsDefined("factors", "enabled") {
		out.Factors.Enabled = false
	}
	recommended := storagepolicy.Default()
	if !md.IsDefined("storage_retention", "view_rows_per_series") {
		out.StorageRetention.ViewBars = recommended.View.Bars
	}
	if !md.IsDefined("storage_retention", "defaults") {
		out.StorageRetention.Defaults = recommended.Retention.Defaults
	}
	if !md.IsDefined("storage_retention", "spaces") {
		out.StorageRetention.Spaces = recommended.Retention.Spaces
	}
	if !md.IsDefined("storage_view", "maintenance_check_interval") {
		out.StorageView.MaintenanceCheckInterval = "1m"
	}
	if !md.IsDefined("storage_view", "capacity_check_interval") {
		out.StorageView.CapacityCheckInterval = "1h"
	}
	if !md.IsDefined("storage_view", "capacity_check_jitter") {
		jitter := time.Hour
		if interval, err := time.ParseDuration(strings.TrimSpace(out.StorageView.CapacityCheckInterval)); err == nil && interval > 0 && interval < jitter {
			jitter = interval
		}
		out.StorageView.CapacityCheckJitter = jitter.String()
	}
	if !md.IsDefined("storage_view", "max_view_file_bytes") {
		out.StorageView.MaxViewFileBytes = storagepolicy.DefaultMaxViewFileBytes
	}
	if !md.IsDefined("collector_retention", "maintenance_interval") {
		out.CollectorRetention.MaintenanceInterval = "1m"
	}
	if !md.IsDefined("collector_retention", "maintenance_offset") {
		out.CollectorRetention.MaintenanceOffset = "35s"
	}
	if !md.IsDefined("collector_retention", "maintenance_timeout") {
		out.CollectorRetention.MaintenanceTimeout = "20s"
	}
	if !md.IsDefined("collector_retention", "max_rows_per_pass") {
		out.CollectorRetention.MaxRowsPerPass = 50000
	}
	if !md.IsDefined("local_logs", "max_size_mb") {
		out.LocalLogs.MaxSizeMB = 50
	}
	if !md.IsDefined("observability", "deliver_policy") {
		out.Observability.DeliverPolicy = "all"
	}
	if !md.IsDefined("local_logs", "backup_count") {
		out.LocalLogs.BackupCount = 5
	}
	if !md.IsDefined("factors", "source_dir") || strings.TrimSpace(out.Factors.SourceDir) == "" {
		out.Factors.SourceDir = "./modules/factor/factors"
	}
	if !md.IsDefined("egress_proxy", "http_domains") {
		out.EgressProxy.HTTPDomains = []string{"*.binance.com", "data-api.binance.vision"}
	}
	if !md.IsDefined("egress_proxy", "dns", "refresh_interval_seconds") {
		out.EgressProxy.DNS.RefreshIntervalSeconds = 300
	}
	if !md.IsDefined("egress_proxy", "dns", "request_timeout_ms") {
		out.EgressProxy.DNS.RequestTimeoutMS = 3000
	}
	if !md.IsDefined("egress_proxy", "dns", "lookup_timeout_ms") {
		out.EgressProxy.DNS.LookupTimeoutMS = 1500
	}
	if !md.IsDefined("egress_proxy", "dns", "probe_timeout_ms") {
		out.EgressProxy.DNS.ProbeTimeoutMS = 500
	}
	if !md.IsDefined("egress_proxy", "dns", "probe_port") {
		out.EgressProxy.DNS.ProbePort = 443
	}
	if !md.IsDefined("egress_proxy", "dns", "cache_ttl_seconds") {
		out.EgressProxy.DNS.CacheTTLSeconds = 300
	}
	if !md.IsDefined("egress_proxy", "dns", "max_ips_per_domain") {
		out.EgressProxy.DNS.MaxIPsPerDomain = 4
	}
	if err := resolveManifestReferences(out); err != nil {
		return err
	}
	return validate(out)
}

func validate(manifest *Manifest) error {
	manifest.Paths = manifest.Paths.Resolved()
	if err := validatePaths(&manifest.Paths); err != nil {
		return err
	}
	manifest.Admin.Username = strings.TrimSpace(manifest.Admin.Username)
	if manifest.Admin.Username == "" {
		return fmt.Errorf("config_invalid: admin.username is required")
	}
	if manifest.Admin.Password == "" {
		return fmt.Errorf("config_invalid: admin.password is required")
	}
	if len([]byte(manifest.Admin.Password)) > 72 {
		return fmt.Errorf("config_invalid: admin.password must not exceed 72 bytes")
	}
	manifest.TencentCloud.SecretID = strings.TrimSpace(manifest.TencentCloud.SecretID)
	if manifest.TencentCloud.SecretID == "" {
		return fmt.Errorf("config_invalid: tencent_cloud.secret_id is required")
	}
	if manifest.TencentCloud.SecretKey == "" {
		return fmt.Errorf("config_invalid: tencent_cloud.secret_key is required")
	}
	manifest.TencentCloud.Region = strings.TrimSpace(manifest.TencentCloud.Region)
	if manifest.TencentCloud.Region == "" {
		return fmt.Errorf("config_invalid: tencent_cloud.region is required")
	}
	eventBusAddress := strings.TrimSpace(manifest.EventBus.PublicAddress)
	if eventBusAddress != manifest.EventBus.PublicAddress || !servicecatalog.ValidHostAddress(eventBusAddress) {
		return fmt.Errorf("config_invalid: eventbus.public_address must be an IP address or DNS hostname")
	}
	manifest.EventBus.PublicAddress = eventBusAddress
	if manifest.EventBus.Port < 1 || manifest.EventBus.Port > 65535 {
		return fmt.Errorf("config_invalid: eventbus.port must be between 1 and 65535")
	}
	if !manifest.EventBus.TLSEnabled {
		return fmt.Errorf("config_invalid: eventbus.tls_enabled must be true")
	}
	manifest.Notification.ChannelType = strings.TrimSpace(manifest.Notification.ChannelType)
	if manifest.Notification.ChannelType == "" {
		manifest.Notification.ChannelType = "wecom"
	}
	if manifest.Notification.ChannelType != "wecom" && manifest.Notification.ChannelType != "feishu" {
		return fmt.Errorf("config_invalid: notification.channel_type must be wecom or feishu")
	}
	manifest.Notification.WebhookURL = strings.TrimSpace(manifest.Notification.WebhookURL)
	if manifest.Notification.WebhookURL != "" && !validNotificationWebhook(manifest.Notification.ChannelType, manifest.Notification.WebhookURL) {
		return fmt.Errorf("config_invalid: notification.webhook_url must use HTTPS and match an approved %s platform host", manifest.Notification.ChannelType)
	}
	if err := validateFactorSetup(&manifest.Factors); err != nil {
		return err
	}
	policy := manifest.StoragePolicy()
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("config_invalid: storage policy: %w", err)
	}
	// 交易时段市场的估算只是提示；7×24 市场的保留期装不下 view_rows_per_series 时报错。
	if errs, _ := policy.Coverage(); len(errs) > 0 {
		return fmt.Errorf("config_invalid: storage policy: %s", strings.Join(errs, "; "))
	}
	if err := validateCollectorRetention(&manifest.CollectorRetention); err != nil {
		return err
	}
	if manifest.LocalLogs.MaxSizeMB < 1 || manifest.LocalLogs.MaxSizeMB > 10240 {
		return fmt.Errorf("config_invalid: local_logs.max_size_mb must be between 1 and 10240")
	}
	if manifest.Observability.DeliverPolicy != "all" && manifest.Observability.DeliverPolicy != "new" {
		return fmt.Errorf("config_invalid: observability.deliver_policy must be all or new")
	}
	if manifest.LocalLogs.BackupCount < 1 || manifest.LocalLogs.BackupCount > 100 {
		return fmt.Errorf("config_invalid: local_logs.backup_count must be between 1 and 100")
	}
	if err := validateHostCatalog(manifest.HostCatalog); err != nil {
		return err
	}

	if err := validateEgressProxy(&manifest.EgressProxy); err != nil {
		return err
	}
	if err := validatePlacements(manifest); err != nil {
		return err
	}
	return validateSCFPlan(manifest)
}

func validatePaths(paths *Paths) error {
	if paths == nil {
		return fmt.Errorf("config_invalid: paths is required")
	}
	paths.DeployRoot = filepath.Clean(strings.TrimSpace(paths.DeployRoot))
	paths.ControlRoot = filepath.Clean(strings.TrimSpace(paths.ControlRoot))
	paths.StorageRoot = filepath.Clean(strings.TrimSpace(paths.StorageRoot))
	for name, value := range map[string]string{
		"deploy_root": paths.DeployRoot, "control_root": paths.ControlRoot, "storage_root": paths.StorageRoot,
	} {
		if value == "" || !filepath.IsAbs(value) || value == "/" || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("config_invalid: paths.%s must be a non-root absolute path", name)
		}
		for _, r := range value {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/._-", r) {
				continue
			}
			return fmt.Errorf("config_invalid: paths.%s contains an unsupported character", name)
		}
	}
	base := paths.DeployRoot + string(filepath.Separator)
	if paths.ControlRoot != paths.DeployRoot && !strings.HasPrefix(paths.ControlRoot, base) {
		return fmt.Errorf("config_invalid: paths.control_root must stay under paths.deploy_root")
	}
	if paths.StorageRoot == paths.DeployRoot || pathsOverlap(paths.StorageRoot, paths.ControlRoot) {
		return fmt.Errorf("config_invalid: paths.storage_root must not overlap deploy_root or control_root")
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	parent = filepath.Clean(strings.TrimSpace(parent))
	child = filepath.Clean(strings.TrimSpace(child))
	if parent == "" || child == "" {
		return false
	}
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validateCollectorRetention(cfg *CollectorRetention) error {
	interval, err := time.ParseDuration(strings.TrimSpace(cfg.MaintenanceInterval))
	if err != nil || interval <= 0 || interval > 24*time.Hour {
		return fmt.Errorf("config_invalid: collector_retention.maintenance_interval must be greater than 0 and at most 24h")
	}
	offset, err := time.ParseDuration(strings.TrimSpace(cfg.MaintenanceOffset))
	if err != nil || offset < 0 || offset >= interval {
		return fmt.Errorf("config_invalid: collector_retention.maintenance_offset must be at least 0 and less than maintenance_interval")
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(cfg.MaintenanceTimeout))
	if err != nil || timeout <= 0 || offset+timeout > interval {
		// Each pass must end before the next interval boundary, where the
		// Collector market tick owns its single SQLite connection.
		return fmt.Errorf("config_invalid: collector_retention.maintenance_timeout must be positive and maintenance_offset + maintenance_timeout must not exceed maintenance_interval")
	}
	if cfg.MaxRowsPerPass < 1 || cfg.MaxRowsPerPass > 50000 {
		return fmt.Errorf("config_invalid: collector_retention.max_rows_per_pass must be between 1 and 50000")
	}
	return nil
}

func validateEgressProxy(cfg *EgressProxy) error {
	if _, err := domainpolicy.New(cfg.HTTPDomains); err != nil {
		return fmt.Errorf("config_invalid: egress_proxy.http_domains: %w", err)
	}
	dns := &cfg.DNS
	if dns.RefreshIntervalSeconds <= 0 || dns.RequestTimeoutMS <= 0 || dns.RequestTimeoutMS > 60000 || dns.LookupTimeoutMS <= 0 || dns.LookupTimeoutMS > 60000 || dns.ProbeTimeoutMS <= 0 || dns.ProbeTimeoutMS > 60000 || dns.CacheTTLSeconds <= 0 {
		return fmt.Errorf("config_invalid: egress_proxy.dns intervals must be positive and request/lookup/probe timeout at most 60000ms")
	}
	if dns.ProbePort < 1 || dns.ProbePort > 65535 || dns.MaxIPsPerDomain < 1 || dns.MaxIPsPerDomain > 4 {
		return fmt.Errorf("config_invalid: egress_proxy.dns invalid probe port or IP cap")
	}
	if len(dns.Domains) > 16 {
		return fmt.Errorf("config_invalid: egress_proxy.dns supports at most 16 domains")
	}
	seen := map[string]bool{}
	for i, raw := range dns.Domains {
		domain, ok := domainpolicy.Host(strings.TrimSpace(raw))
		if !ok || seen[domain] {
			return fmt.Errorf("config_invalid: egress_proxy.dns domain %q is invalid or duplicated", raw)
		}
		seen[domain] = true
		dns.Domains[i] = domain
	}
	return nil
}

// PlacementHost returns the selected host for a single component. Multi-copy
// components are addressed through the compiled Directory instead.
func (m Manifest) PlacementHost(component string) string {
	selected := ""
	for host, components := range m.Placements {
		for _, id := range components {
			if id != component {
				continue
			}
			if selected != "" && selected != host {
				return ""
			}
			selected = host
		}
	}
	return selected
}
func validatePlacements(m *Manifest) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	for host := range m.Placements {
		if _, ok := m.HostCatalog[host]; !ok {
			return fmt.Errorf("config_invalid: placements host ID %q is unknown", host)
		}
		for _, id := range m.Placements[host] {
			component, ok := catalog.Component(id)
			if !ok || component.Scope == servicecatalog.ScopeHost {
				return fmt.Errorf("config_invalid: placements contains an unknown or automatic component")
			}
		}
	}
	topology, err := m.Topology()
	if err != nil {
		return err
	}
	if err := catalog.ValidateTopology(topology); err != nil {
		return fmt.Errorf("config_invalid: placements: %w", err)
	}
	return nil
}

func validateFactorSetup(cfg *FactorSetup) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	cfg.SourceDir = filepath.ToSlash(filepath.Clean(strings.TrimSpace(cfg.SourceDir)))
	if cfg.SourceDir == "" || cfg.SourceDir == "." || filepath.IsAbs(cfg.SourceDir) || cfg.SourceDir == ".." || strings.HasPrefix(cfg.SourceDir, "../") {
		return fmt.Errorf("config_invalid: factors.source_dir must be a repository-relative directory")
	}
	setKeys := make(map[string]struct{}, len(cfg.Sets))
	for index := range cfg.Sets {
		set := &cfg.Sets[index]
		path := fmt.Sprintf("factors.sets[%d]", index)
		set.SpaceID = strings.TrimSpace(set.SpaceID)
		set.SourceDatasetID = strings.TrimSpace(set.SourceDatasetID)
		set.Freq = strings.TrimSpace(set.Freq)
		set.SubjectMode = strings.TrimSpace(set.SubjectMode)
		if set.SpaceID == "" || set.SourceDatasetID == "" || set.Freq == "" {
			return fmt.Errorf("config_invalid: %s requires space_id, source_dataset_id and freq", path)
		}
		key := factorSetupSetKey(set.SourceDatasetID, set.Freq)
		if _, ok := setKeys[key]; ok {
			return fmt.Errorf("config_invalid: factor set for source_dataset_id %q and freq %q is duplicated", set.SourceDatasetID, set.Freq)
		}
		setKeys[key] = struct{}{}
		if set.SubjectMode == "" {
			set.SubjectMode = "all"
		}
		if set.SubjectMode != "all" && set.SubjectMode != "include" {
			return fmt.Errorf("config_invalid: %s.subject_mode must be all or include", path)
		}
		set.Subjects = uniqueTrimmedStrings(set.Subjects)
		if set.SubjectMode == "include" && len(set.Subjects) == 0 {
			return fmt.Errorf("config_invalid: %s.subjects must not be empty for include mode", path)
		}
		if set.SubjectMode == "all" && len(set.Subjects) != 0 {
			return fmt.Errorf("config_invalid: %s.subjects must be empty when subject_mode is all", path)
		}
	}
	seen := make(map[string]struct{}, len(cfg.Definitions))
	for index := range cfg.Definitions {
		item := &cfg.Definitions[index]
		path := fmt.Sprintf("factors.definitions[%d]", index)
		item.FactorID = strings.TrimSpace(item.FactorID)
		item.File = filepath.ToSlash(filepath.Clean(strings.TrimSpace(item.File)))
		if item.FactorID == "" || item.File == "" || item.File == "." {
			return fmt.Errorf("config_invalid: %s requires factor_id and file", path)
		}
		if filepath.IsAbs(item.File) || item.File == ".." || strings.HasPrefix(item.File, "../") {
			return fmt.Errorf("config_invalid: %s.file must stay under factors.source_dir", path)
		}
		if _, ok := seen[item.FactorID]; ok {
			return fmt.Errorf("config_invalid: factors definition %q is duplicated", item.FactorID)
		}
		seen[item.FactorID] = struct{}{}
		if strings.ToLower(filepath.Ext(item.File)) != ".yaml" {
			return fmt.Errorf("config_invalid: %s.file must be a YAML factor library file (*.yaml)", path)
		}
	}
	memberKeys := make(map[string]struct{}, len(cfg.Members))
	for index := range cfg.Members {
		member := &cfg.Members[index]
		path := fmt.Sprintf("factors.members[%d]", index)
		member.SourceDatasetID = strings.TrimSpace(member.SourceDatasetID)
		member.Freq = strings.TrimSpace(member.Freq)
		member.FactorID = strings.TrimSpace(member.FactorID)
		member.Status = strings.TrimSpace(member.Status)
		if member.FactorID == "" || member.SourceDatasetID == "" || member.Freq == "" {
			return fmt.Errorf("config_invalid: %s requires factor_id, source_dataset_id and freq", path)
		}
		if _, ok := setKeys[factorSetupSetKey(member.SourceDatasetID, member.Freq)]; !ok {
			return fmt.Errorf("config_invalid: %s has no matching factor set for source_dataset_id %q and freq %q", path, member.SourceDatasetID, member.Freq)
		}
		if _, ok := seen[member.FactorID]; !ok {
			return fmt.Errorf("config_invalid: %s references unknown factor_id %q; declare it in [[factors.definitions]]", path, member.FactorID)
		}
		key := factorSetupSetKey(member.SourceDatasetID, member.Freq) + "\x00" + member.FactorID
		if _, ok := memberKeys[key]; ok {
			return fmt.Errorf("config_invalid: factor %q is a member of set (%s, %s) more than once", member.FactorID, member.SourceDatasetID, member.Freq)
		}
		memberKeys[key] = struct{}{}
		if member.Status == "" {
			member.Status = "enabled"
		}
		if member.Status != "enabled" && member.Status != "disabled" {
			return fmt.Errorf("config_invalid: %s.status must be enabled or disabled", path)
		}
	}
	return nil
}

func factorSetupSetKey(sourceDatasetID, freq string) string {
	return strings.TrimSpace(sourceDatasetID) + "\x00" + strings.TrimSpace(freq)
}

func uniqueTrimmedStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

// supportedSCFRegion keeps an operator typo from creating a partial fleet.
// It intentionally covers the standard Tencent Cloud SCF regions used by MooX.
func supportedSCFRegion(region string) bool {
	return tencent.IsSCFRegion(region)
}

func validHTTPSWebhook(rawURL string) bool {
	parsed, err := url.ParseRequestURI(rawURL)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" &&
		parsed.User == nil && parsed.Fragment == ""
}

func validNotificationWebhook(channelType, rawURL string) bool {
	if !validHTTPSWebhook(rawURL) {
		return false
	}
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	switch channelType {
	case "wecom":
		return host == "qyapi.weixin.qq.com"
	case "feishu":
		return host == "open.feishu.cn" || strings.HasSuffix(host, ".feishu.cn") || strings.HasSuffix(host, ".larksuite.com")
	default:
		return false
	}
}

var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validAccessInstanceID(id string) bool {
	return strings.HasPrefix(id, "access@") && servicecatalog.ValidHostID(strings.TrimPrefix(id, "access@"))
}
func validateAccessAddress(address, path string) error {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.Atoi(port)
	if err != nil || portErr != nil || number < 1 || number > 65535 || !servicecatalog.ValidHostAddress(host) || address != strings.TrimSpace(address) {
		return fmt.Errorf("config_invalid: %s must be host:port", path)
	}
	return nil
}
