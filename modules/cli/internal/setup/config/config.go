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
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

const (
	manifestName = "custom.toml"
	maxFileSize  = 1 << 20
	// The cloud disk mounted at /data is the canonical runtime volume. Keep all
	// generated packages, state, logs and credentials below this root so a
	// host's small system disk is not consumed by MooX.
	DefaultDeployRoot  = "/data/moox"
	DefaultControlRoot = "/data/moox/prod"
	DefaultStorageRoot = "/data/moox/storage"
	// DefaultStorageViewRebuildLookbackPeriods is the number of completed bars
	// every View rebuild replays when no explicit manifest value is supplied.
	DefaultStorageViewRebuildLookbackPeriods uint64 = 1000
	// SCFCLSReserveMilliseconds is injected into every short-lived market SCF.
	// Keep setup validation aligned with the runtime's CLS flush reservation.
	SCFCLSReserveMilliseconds = 3000
	// SCFCompletionReserveMilliseconds leaves enough time for the durable
	// completion event after Storage has accepted the aggregate write.
	SCFCompletionReserveMilliseconds = 3000
	// SCFFinalResponseReserveMilliseconds keeps the SCF runtime enough time to
	// serialize and return its response after best-effort CLS logging.
	SCFFinalResponseReserveMilliseconds = 500
	// DefaultCryptoMarketTimerFunctionCount is the baseline Timer fleet size
	// for the built-in crypto market Space.
	DefaultCryptoMarketTimerFunctionCount = 60
	// DefaultStockCNMarketTimerFunctionCount is the baseline Timer fleet size
	// for the mainland China A-share Space. Each region is capped at 50
	// functions, so this default needs at least four enabled regions.
	DefaultStockCNMarketTimerFunctionCount = 200
	// DefaultStockCNInstrumentInvokeTimeoutSeconds leaves enough room for the
	// measured full-market Sina instrument snapshot while Timer functions keep
	// their fixed 15-second execution budget.
	DefaultStockCNInstrumentInvokeTimeoutSeconds = 60
)

// Paths controls where setup-cli installs the control and Storage packages on
// the target host. All paths are absolute and must remain below DeployRoot.
// The section is optional: omitted values resolve to the cloud-disk defaults
// above, which keeps older custom.toml files deterministic.
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
	PublicAddress string `toml:"public_address"`
	Port          int    `toml:"port"`
	TLSEnabled    bool   `toml:"tls_enabled"`
}

// DNSResolver configures the single Trade node that resolves and probes
// market API domains for Collector. custom.toml is the source of truth; the
// CLI renders the Trade-owned subset into Trade's app.yaml at deployment time.
type DNSResolver struct {
	Enabled                bool     `toml:"enabled"`
	TradeNode              string   `toml:"trade_node"`
	RefreshIntervalSeconds int      `toml:"refresh_interval_seconds"`
	RequestTimeoutMS       int      `toml:"request_timeout_ms"`
	LookupTimeoutMS        int      `toml:"lookup_timeout_ms"`
	ProbeTimeoutMS         int      `toml:"probe_timeout_ms"`
	ProbePort              int      `toml:"probe_port"`
	CacheTTLSeconds        int      `toml:"cache_ttl_seconds"`
	MaxIPsPerDomain        int      `toml:"max_ips_per_domain"`
	Domains                []string `toml:"domains"`
}

// StorageView contains the deployment-wide View maintenance policy. Zero values
// in nested overrides inherit the global value.
type StorageView struct {
	MaintenanceCheckInterval string                      `toml:"maintenance_check_interval" json:"maintenance_check_interval"`
	RebuildLookbackPeriods   uint64                      `toml:"rebuild_lookback_periods" json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries      uint64                      `toml:"max_periods_per_series" json:"max_periods_per_series"`
	MaxViewFileBytes         int64                       `toml:"max_view_file_bytes" json:"max_view_file_bytes"`
	SystemMonitor            StorageViewPolicyOverride   `toml:"system_monitor" json:"system_monitor"`
	Views                    []StorageViewPolicyOverride `toml:"views" json:"views"`
}

type StorageViewPolicyOverride struct {
	SpaceID                string `toml:"space_id" json:"space_id"`
	ViewID                 string `toml:"view_id" json:"view_id"`
	RebuildLookbackPeriods uint64 `toml:"rebuild_lookback_periods" json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries    uint64 `toml:"max_periods_per_series" json:"max_periods_per_series"`
	MaxViewFileBytes       int64  `toml:"max_view_file_bytes" json:"max_view_file_bytes"`
}

type ResolvedStorageViewPolicy struct {
	MaintenanceCheckInterval string `json:"maintenance_check_interval"`
	RebuildLookbackPeriods   uint64 `json:"rebuild_lookback_periods"`
	MaxPeriodsPerSeries      uint64 `json:"max_periods_per_series"`
	MaxViewFileBytes         int64  `json:"max_view_file_bytes"`
}

func (s StorageView) ResolvePolicy(spaceID, viewID string) ResolvedStorageViewPolicy {
	policy := ResolvedStorageViewPolicy{
		MaintenanceCheckInterval: s.MaintenanceCheckInterval,
		RebuildLookbackPeriods:   s.RebuildLookbackPeriods,
		MaxPeriodsPerSeries:      s.MaxPeriodsPerSeries,
		MaxViewFileBytes:         s.MaxViewFileBytes,
	}
	apply := func(override StorageViewPolicyOverride) {
		if override.RebuildLookbackPeriods > 0 {
			policy.RebuildLookbackPeriods = override.RebuildLookbackPeriods
		}
		if override.MaxPeriodsPerSeries > 0 {
			policy.MaxPeriodsPerSeries = override.MaxPeriodsPerSeries
		}
		if override.MaxViewFileBytes > 0 {
			policy.MaxViewFileBytes = override.MaxViewFileBytes
		}
	}
	if spaceID == "moox_system" {
		apply(s.SystemMonitor)
	}
	for _, override := range s.Views {
		if strings.TrimSpace(override.SpaceID) == strings.TrimSpace(spaceID) && strings.TrimSpace(override.ViewID) == strings.TrimSpace(viewID) {
			apply(override)
		}
	}
	return policy
}

type Notification struct {
	ChannelType string `toml:"channel_type"`
	WebhookURL  string `toml:"webhook_url"`
}

type Host struct {
	Name     string `toml:"name"`
	Address  string `toml:"address"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
	// TLSMode controls the certificate issuer for the control-plane HTTPS
	// edge. "internal" is useful when the endpoint is a raw IP that cannot
	// obtain a public ACME certificate; empty/"auto" keeps the existing
	// host-based selection.
	TLSMode string `toml:"tls_mode"`
}

type SCFFetcherRegion struct {
	Region             string `toml:"region"`
	DisplayName        string `toml:"display_name"`
	Enabled            bool   `toml:"enabled"`
	FunctionCount      int    `toml:"function_count"`
	CloudAccountID     string `toml:"cloud_account_id"`
	CloudAccountName   string `toml:"cloud_account_name"`
	CredentialSecretID string `toml:"credential_secret_id"`
	AppID              string `toml:"app_id"`
	COSBucket          string `toml:"cos_bucket"`
}

// SCFFetcher is the manifest container for independent, space-scoped SCF
// fleets. A function may only consume tasks from its configured space.
type SCFFetcher struct {
	Enabled bool              `toml:"enabled"`
	Spaces  []SCFFetcherSpace `toml:"spaces"`
}

// FactorSetup describes the local Python factors and their default View
// bindings imported by `moox-cli setup init` or `setup factors`.
type FactorSetup struct {
	Enabled   bool              `toml:"enabled"`
	SourceDir string            `toml:"source_dir"`
	Items     []FactorSetupItem `toml:"items"`
}

// FactorSetupItem is intentionally declarative: the source file remains the
// source of truth while this block supplies the runtime contract required by
// FactorMgr and the default source View binding.
type FactorSetupItem struct {
	FactorID        string   `toml:"factor_id"`
	File            string   `toml:"file"`
	Name            string   `toml:"name"`
	InputColumns    []string `toml:"input_columns"`
	Outputs         []string `toml:"outputs"`
	ParamsJSON      string   `toml:"params_json"`
	LookbackPeriods int      `toml:"lookback_periods"`
	SpaceID         string   `toml:"space_id"`
	SourceViewID    string   `toml:"source_view_id"`
	Freq            string   `toml:"freq"`
	SubjectMode     string   `toml:"subject_mode"`
	Subjects        []string `toml:"subjects"`
	Status          string   `toml:"status"`
}

// SCFFetcherSpace describes one separately packaged and deployed source
// collector fleet. PackageConfigDir is relative to modules/collector/configs.
type SCFFetcherSpace struct {
	SpaceID          string `toml:"space_id"`
	Entrypoint       string `toml:"entrypoint"`
	PackageConfigDir string `toml:"package_config_dir"`
	PackageName      string `toml:"package_name"`
	// CLSCloudAccountID owns the single regional CLS topic used by every
	// short-lived collector function in this space, regardless of its SCF region.
	CLSCloudAccountID string `toml:"cls_cloud_account_id"`
	Namespace         string `toml:"namespace"`
	Runtime           string `toml:"runtime"`
	FunctionPrefix    string `toml:"function_prefix"`
	// TimerFunctionCount is the total Timer fleet size for this Space. When it
	// is zero and all enabled regional function_count values are zero, the
	// built-in Space default is used and counts are distributed deterministically.
	TimerFunctionCount             int                `toml:"timer_function_count"`
	StorageGatewayNodeID           string             `toml:"storage_gateway_node_id"`
	StorageRPCGatewayTarget        string             `toml:"storage_rpc_gateway_target"`
	MemorySize                     int                `toml:"memory_size"`
	TimeoutSeconds                 int                `toml:"timeout_seconds"`
	InstrumentInvokeTimeoutSeconds int                `toml:"instrument_invoke_timeout_seconds"`
	RealtimeBatchSize              int                `toml:"realtime_batch_size"`
	RealtimeBarLimit               int                `toml:"realtime_bar_limit"`
	CatchupBatchSize               int                `toml:"catchup_batch_size"`
	CatchupBarLimit                int                `toml:"catchup_bar_limit"`
	MaxInflightRequests            int                `toml:"max_inflight_requests"`
	RequestTimeoutMS               int                `toml:"request_timeout_ms"`
	HTTPMaxAttempts                int                `toml:"http_max_attempts"`
	StorageMaxAttempts             int                `toml:"storage_max_attempts"`
	StorageTimeoutMS               int                `toml:"storage_timeout_ms"`
	MaxRetryAttempts               int                `toml:"max_retry_attempts"`
	RetryDelays                    []string           `toml:"retry_delays"`
	StaggerEnabled                 bool               `toml:"stagger_enabled"`
	Regions                        []SCFFetcherRegion `toml:"regions"`
}

// DefaultTimerFunctionCount returns the built-in Timer capacity for a known
// Space. Unknown Spaces must provide regional function_count values or an
// explicit timer_function_count.
func DefaultTimerFunctionCount(spaceID string) int {
	switch strings.ToLower(strings.TrimSpace(spaceID)) {
	case "crypto_market":
		return DefaultCryptoMarketTimerFunctionCount
	case "stock_cn":
		return DefaultStockCNMarketTimerFunctionCount
	default:
		return 0
	}
}

type Manifest struct {
	Admin        Admin        `toml:"admin"`
	TencentCloud TencentCloud `toml:"tencent_cloud"`
	EventBus     EventBus     `toml:"eventbus"`
	Paths        Paths        `toml:"paths"`
	DNSResolver  DNSResolver  `toml:"dns_resolver"`
	StorageView  StorageView  `toml:"storage_view"`
	LocalLogs    LocalLogs    `toml:"local_logs"`
	Notification Notification `toml:"notification"`
	Factors      FactorSetup  `toml:"factors"`
	SCFFetcher   SCFFetcher   `toml:"scf_fetcher"`
	ControlHost  Host         `toml:"control_host"`
	CompileHost  Host         `toml:"compile_host"`
	OtherHosts   []Host       `toml:"other_hosts"`
}

func (m Manifest) Hosts() []Host {
	hosts := make([]Host, 0, 1+len(m.OtherHosts))
	hosts = append(hosts, m.ControlHost)
	hosts = append(hosts, m.OtherHosts...)
	return hosts
}

func (m Manifest) HasCompileHost() bool {
	return hostConfigured(m.CompileHost)
}

type Snapshot struct {
	Manifest Manifest
	path     string
	info     os.FileInfo
	digest   [sha256.Size]byte
}

func Load(path, repositoryRoot string) (*Snapshot, error) {
	resolvedPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: resolve custom.toml path")
	}
	resolvedRoot, err := filepath.Abs(repositoryRoot)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: resolve repository root")
	}
	expectedPath := filepath.Join(filepath.Clean(resolvedRoot), manifestName)
	if filepath.Clean(resolvedPath) != expectedPath {
		if filepath.Base(resolvedPath) != manifestName {
			return nil, fmt.Errorf("config_invalid: setup file must be named custom.toml")
		}
		return nil, fmt.Errorf("config_invalid: custom.toml must be in repository root")
	}

	info, raw, err := readSecureFile(resolvedPath)
	if err != nil {
		return nil, err
	}
	var manifest Manifest
	if err := decodeStrict(raw, &manifest); err != nil {
		return nil, err
	}
	return &Snapshot{
		Manifest: manifest,
		path:     resolvedPath,
		info:     info,
		digest:   sha256.Sum256(raw),
	}, nil
}

func (s *Snapshot) VerifyUnchanged() error {
	info, raw, err := readSecureFile(s.path)
	if err != nil {
		return fmt.Errorf("config_changed: custom.toml security or identity changed")
	}
	if !os.SameFile(s.info, info) || s.digest != sha256.Sum256(raw) {
		return fmt.Errorf("config_changed: custom.toml changed during command")
	}
	return nil
}

func readSecureFile(path string) (os.FileInfo, []byte, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml is not readable")
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must be a regular file")
	}
	if linkInfo.Mode().Perm() != 0o600 {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must have mode 0600")
	}
	if !ownedByCurrentUser(linkInfo) {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must be owned by the current user")
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml is not readable")
	}
	defer f.Close()
	openInfo, err := f.Stat()
	if err != nil || !openInfo.Mode().IsRegular() || !os.SameFile(linkInfo, openInfo) {
		return nil, nil, fmt.Errorf("config_insecure: custom.toml must be a stable regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxFileSize+1))
	if err != nil {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml is not readable")
	}
	if len(raw) > maxFileSize {
		return nil, nil, fmt.Errorf("config_invalid: custom.toml exceeds 1 MiB")
	}
	return openInfo, raw, nil
}

func decodeStrict(raw []byte, out *Manifest) error {
	md, err := toml.DecodeReader(bytes.NewReader(raw), out)
	if err != nil {
		return fmt.Errorf("config_invalid: decode custom.toml")
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		return fmt.Errorf("config_invalid: unknown field %s", keys[0].String())
	}
	if out.ControlHost.Port == 0 {
		out.ControlHost.Port = 22
	}
	if !md.IsDefined("eventbus", "port") {
		out.EventBus.Port = 4222
	}
	if !md.IsDefined("tencent_cloud", "region") {
		out.TencentCloud.Region = "ap-guangzhou"
	}
	out.Paths = out.Paths.Resolved()
	if !md.IsDefined("factors", "enabled") {
		out.Factors.Enabled = false
	}
	if !md.IsDefined("storage_view", "rebuild_lookback_periods") {
		out.StorageView.RebuildLookbackPeriods = DefaultStorageViewRebuildLookbackPeriods
	}
	if !md.IsDefined("storage_view", "maintenance_check_interval") {
		out.StorageView.MaintenanceCheckInterval = "1m"
	}
	if !md.IsDefined("storage_view", "max_periods_per_series") {
		out.StorageView.MaxPeriodsPerSeries = 2000
	}
	if !md.IsDefined("storage_view", "max_view_file_bytes") {
		out.StorageView.MaxViewFileBytes = 1 << 30
	}
	if !md.IsDefined("local_logs", "max_size_mb") {
		out.LocalLogs.MaxSizeMB = 50
	}
	if !md.IsDefined("local_logs", "backup_count") {
		out.LocalLogs.BackupCount = 5
	}
	if !md.IsDefined("factors", "source_dir") || strings.TrimSpace(out.Factors.SourceDir) == "" {
		out.Factors.SourceDir = "./examples/factors"
	}
	if out.HasCompileHost() && out.CompileHost.Port == 0 {
		out.CompileHost.Port = 22
	}
	for i := range out.OtherHosts {
		if out.OtherHosts[i].Port == 0 {
			out.OtherHosts[i].Port = 22
		}
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
	if eventBusAddress != manifest.EventBus.PublicAddress || !validEventBusAddress(eventBusAddress) {
		return fmt.Errorf("config_invalid: eventbus.public_address must be an IPv4 address or DNS hostname")
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
	if err := validateSCFFetcher(&manifest.SCFFetcher); err != nil {
		return err
	}
	if err := validateFactorSetup(&manifest.Factors); err != nil {
		return err
	}
	if err := validateStorageView(&manifest.StorageView); err != nil {
		return err
	}
	if manifest.LocalLogs.MaxSizeMB < 1 || manifest.LocalLogs.MaxSizeMB > 10240 {
		return fmt.Errorf("config_invalid: local_logs.max_size_mb must be between 1 and 10240")
	}
	if manifest.LocalLogs.BackupCount < 1 || manifest.LocalLogs.BackupCount > 100 {
		return fmt.Errorf("config_invalid: local_logs.backup_count must be between 1 and 100")
	}

	names := make(map[string]struct{}, 1+len(manifest.OtherHosts))
	addresses := make(map[string]struct{}, 1+len(manifest.OtherHosts))
	if err := validateHost("control_host", &manifest.ControlHost, names, addresses, true); err != nil {
		return err
	}
	if manifest.HasCompileHost() {
		// The compiler may intentionally run on the control host. Keep it out
		// of the deployment-host uniqueness sets while still validating it.
		if err := validateHost("compile_host", &manifest.CompileHost, nil, nil, false); err != nil {
			return err
		}
	}
	for i := range manifest.OtherHosts {
		if err := validateHost(fmt.Sprintf("other_hosts[%d]", i), &manifest.OtherHosts[i], names, addresses, true); err != nil {
			return err
		}
	}
	if err := validateDNSResolver(&manifest.DNSResolver, manifest); err != nil {
		return err
	}
	return nil
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
	for name, value := range map[string]string{"control_root": paths.ControlRoot, "storage_root": paths.StorageRoot} {
		if value != paths.DeployRoot && !strings.HasPrefix(value, base) {
			return fmt.Errorf("config_invalid: paths.%s must stay under paths.deploy_root", name)
		}
	}
	return nil
}

func validateStorageView(cfg *StorageView) error {
	if cfg.RebuildLookbackPeriods == 0 || cfg.RebuildLookbackPeriods > 1_000_000 {
		return fmt.Errorf("config_invalid: storage_view.rebuild_lookback_periods must be between 1 and 1000000")
	}
	interval, err := time.ParseDuration(strings.TrimSpace(cfg.MaintenanceCheckInterval))
	if err != nil || interval < 30*time.Second {
		return fmt.Errorf("config_invalid: storage_view.maintenance_check_interval must be at least 30s")
	}
	if cfg.MaxPeriodsPerSeries == 0 || cfg.MaxPeriodsPerSeries > 1_000_000 {
		return fmt.Errorf("config_invalid: storage_view.max_periods_per_series must be between 1 and 1000000")
	}
	if cfg.MaxPeriodsPerSeries <= cfg.RebuildLookbackPeriods {
		return fmt.Errorf("config_invalid: storage_view.max_periods_per_series must be greater than rebuild_lookback_periods")
	}
	if cfg.MaxViewFileBytes <= 0 {
		return fmt.Errorf("config_invalid: storage_view.max_view_file_bytes must be positive")
	}
	seen := make(map[string]struct{}, len(cfg.Views))
	for i, override := range cfg.Views {
		spaceID := strings.TrimSpace(override.SpaceID)
		viewID := strings.TrimSpace(override.ViewID)
		if spaceID == "" || viewID == "" {
			return fmt.Errorf("config_invalid: storage_view.views[%d] requires space_id and view_id", i)
		}
		key := spaceID + "\x00" + viewID
		if _, ok := seen[key]; ok {
			return fmt.Errorf("config_invalid: duplicate storage_view.views entry %s/%s", spaceID, viewID)
		}
		seen[key] = struct{}{}
		if override.RebuildLookbackPeriods > 1_000_000 || override.MaxPeriodsPerSeries > 1_000_000 || (override.MaxPeriodsPerSeries > 0 && override.RebuildLookbackPeriods > 0 && override.MaxPeriodsPerSeries <= override.RebuildLookbackPeriods) || override.MaxViewFileBytes < 0 {
			return fmt.Errorf("config_invalid: storage_view.views[%d] contains invalid limits", i)
		}
	}
	if cfg.SystemMonitor.MaxPeriodsPerSeries > 1_000_000 || cfg.SystemMonitor.RebuildLookbackPeriods > 1_000_000 || (cfg.SystemMonitor.MaxPeriodsPerSeries > 0 && cfg.SystemMonitor.RebuildLookbackPeriods > 0 && cfg.SystemMonitor.MaxPeriodsPerSeries <= cfg.SystemMonitor.RebuildLookbackPeriods) || cfg.SystemMonitor.MaxViewFileBytes < 0 {
		return fmt.Errorf("config_invalid: storage_view.system_monitor contains invalid limits")
	}
	if strings.TrimSpace(cfg.SystemMonitor.SpaceID) != "" || strings.TrimSpace(cfg.SystemMonitor.ViewID) != "" {
		return fmt.Errorf("config_invalid: storage_view.system_monitor must not set space_id or view_id")
	}
	resolved := cfg.ResolvePolicy("moox_system", "__default__")
	if resolved.MaxPeriodsPerSeries <= resolved.RebuildLookbackPeriods {
		return fmt.Errorf("config_invalid: resolved system monitor max_periods_per_series must be greater than rebuild_lookback_periods")
	}
	for _, override := range cfg.Views {
		resolved = cfg.ResolvePolicy(override.SpaceID, override.ViewID)
		if resolved.MaxPeriodsPerSeries <= resolved.RebuildLookbackPeriods {
			return fmt.Errorf("config_invalid: resolved policy for %s/%s has max_periods_per_series not greater than rebuild_lookback_periods", override.SpaceID, override.ViewID)
		}
	}
	return nil
}

func validateDNSResolver(cfg *DNSResolver, manifest *Manifest) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	cfg.TradeNode = strings.TrimSpace(cfg.TradeNode)
	if cfg.TradeNode == "" {
		return fmt.Errorf("config_invalid: dns_resolver.trade_node is required when enabled")
	}
	if strings.EqualFold(cfg.TradeNode, manifest.ControlHost.Name) {
		return fmt.Errorf("config_invalid: dns_resolver.trade_node must not be control_host")
	}
	var selected *Host
	for i := range manifest.OtherHosts {
		if strings.EqualFold(strings.TrimSpace(manifest.OtherHosts[i].Name), cfg.TradeNode) {
			selected = &manifest.OtherHosts[i]
			break
		}
	}
	if selected == nil || strings.TrimSpace(selected.Address) == "" {
		return fmt.Errorf("config_invalid: dns_resolver.trade_node %q must match an other_hosts entry with an address", cfg.TradeNode)
	}
	if ip := net.ParseIP(strings.TrimSpace(selected.Address)); ip == nil || !isPublicResolverIP(ip) {
		return fmt.Errorf("config_invalid: dns_resolver.trade_node %q must use a public address", cfg.TradeNode)
	}
	if cfg.RefreshIntervalSeconds <= 0 {
		return fmt.Errorf("config_invalid: dns_resolver.refresh_interval_seconds must be positive")
	}
	if cfg.RequestTimeoutMS <= 0 {
		return fmt.Errorf("config_invalid: dns_resolver.request_timeout_ms must be positive")
	}
	if cfg.LookupTimeoutMS <= 0 {
		return fmt.Errorf("config_invalid: dns_resolver.lookup_timeout_ms must be positive")
	}
	if cfg.ProbeTimeoutMS <= 0 {
		return fmt.Errorf("config_invalid: dns_resolver.probe_timeout_ms must be positive")
	}
	if cfg.ProbePort < 1 || cfg.ProbePort > 65535 {
		return fmt.Errorf("config_invalid: dns_resolver.probe_port must be between 1 and 65535")
	}
	if cfg.CacheTTLSeconds <= 0 {
		return fmt.Errorf("config_invalid: dns_resolver.cache_ttl_seconds must be positive")
	}
	if cfg.MaxIPsPerDomain < 1 || cfg.MaxIPsPerDomain > 4 {
		return fmt.Errorf("config_invalid: dns_resolver.max_ips_per_domain must be between 1 and 4")
	}
	seen := make(map[string]struct{}, len(cfg.Domains))
	for i := range cfg.Domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(cfg.Domains[i]), "."))
		if !validDNSResolverDomain(domain) {
			return fmt.Errorf("config_invalid: dns_resolver.domains[%d] must be a public DNS hostname", i)
		}
		if _, ok := seen[domain]; ok {
			return fmt.Errorf("config_invalid: dns_resolver domain %q is duplicated", domain)
		}
		seen[domain] = struct{}{}
		cfg.Domains[i] = domain
	}
	if len(cfg.Domains) == 0 {
		return fmt.Errorf("config_invalid: dns_resolver.domains must not be empty when enabled")
	}
	if len(cfg.Domains) > 16 {
		return fmt.Errorf("config_invalid: dns_resolver.domains must contain at most 16 entries")
	}
	return nil
}

func isPublicResolverIP(ip net.IP) bool {
	ip = ip.To4()
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	first, second, third := ip[0], ip[1], ip[2]
	return first != 0 && first < 224 &&
		!(first == 100 && second >= 64 && second <= 127) &&
		!(first == 192 && second == 0 && (third == 0 || third == 2)) &&
		!(first == 198 && (second == 18 || second == 19 || (second == 51 && third == 100))) &&
		!(first == 203 && second == 0 && third == 113)
}

func validateFactorSetup(cfg *FactorSetup) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	cfg.SourceDir = filepath.ToSlash(filepath.Clean(strings.TrimSpace(cfg.SourceDir)))
	if cfg.SourceDir == "" || cfg.SourceDir == "." || filepath.IsAbs(cfg.SourceDir) || cfg.SourceDir == ".." || strings.HasPrefix(cfg.SourceDir, "../") {
		return fmt.Errorf("config_invalid: factors.source_dir must be a repository-relative directory")
	}
	seen := make(map[string]struct{}, len(cfg.Items))
	for index := range cfg.Items {
		item := &cfg.Items[index]
		path := fmt.Sprintf("factors.items[%d]", index)
		item.FactorID = strings.TrimSpace(item.FactorID)
		item.File = filepath.ToSlash(filepath.Clean(strings.TrimSpace(item.File)))
		item.Name = strings.TrimSpace(item.Name)
		item.SpaceID = strings.TrimSpace(item.SpaceID)
		item.SourceViewID = strings.TrimSpace(item.SourceViewID)
		item.Freq = strings.TrimSpace(item.Freq)
		item.SubjectMode = strings.TrimSpace(item.SubjectMode)
		item.Status = strings.TrimSpace(item.Status)
		if item.FactorID == "" || item.File == "" || item.SpaceID == "" || item.SourceViewID == "" || item.Freq == "" {
			return fmt.Errorf("config_invalid: %s requires factor_id, file, space_id, source_view_id and freq", path)
		}
		if filepath.IsAbs(item.File) || item.File == ".." || strings.HasPrefix(item.File, "../") {
			return fmt.Errorf("config_invalid: %s.file must stay under factors.source_dir", path)
		}
		if _, ok := seen[item.FactorID]; ok {
			return fmt.Errorf("config_invalid: factors item %q is duplicated", item.FactorID)
		}
		seen[item.FactorID] = struct{}{}
		if item.Name == "" {
			item.Name = item.FactorID
		}
		if item.ParamsJSON == "" {
			item.ParamsJSON = "{}"
		}
		if item.LookbackPeriods < 1 {
			return fmt.Errorf("config_invalid: %s.lookback_periods must be at least 1", path)
		}
		if item.SubjectMode == "" {
			item.SubjectMode = "all"
		}
		if item.SubjectMode != "all" && item.SubjectMode != "include" {
			return fmt.Errorf("config_invalid: %s.subject_mode must be all or include", path)
		}
		if item.SubjectMode == "include" && len(item.Subjects) == 0 {
			return fmt.Errorf("config_invalid: %s.subjects must not be empty for include mode", path)
		}
		if item.Status == "" {
			item.Status = "enabled"
		}
		if item.Status != "enabled" && item.Status != "disabled" {
			return fmt.Errorf("config_invalid: %s.status must be enabled or disabled", path)
		}
	}
	return nil
}

func validateSCFFetcher(cfg *SCFFetcher) error {
	if cfg == nil || !cfg.Enabled {
		return nil
	}
	if len(cfg.Spaces) == 0 {
		return fmt.Errorf("config_invalid: scf_fetcher.spaces must not be empty when enabled")
	}
	seenSpaces := make(map[string]struct{}, len(cfg.Spaces))
	for index := range cfg.Spaces {
		spaceID := strings.TrimSpace(cfg.Spaces[index].SpaceID)
		if spaceID == "" {
			return fmt.Errorf("config_invalid: scf_fetcher.spaces[%d].space_id is required", index)
		}
		if _, exists := seenSpaces[spaceID]; exists {
			return fmt.Errorf("config_invalid: scf_fetcher space %q is duplicated", spaceID)
		}
		seenSpaces[spaceID] = struct{}{}
		cfg.Spaces[index].SpaceID = spaceID
		if err := validateSCFFetcherSpace(&cfg.Spaces[index], fmt.Sprintf("scf_fetcher.spaces[%d]", index)); err != nil {
			return err
		}
	}
	return nil
}

func validateSCFFetcherSpace(cfg *SCFFetcherSpace, path string) error {
	if cfg == nil {
		return fmt.Errorf("config_invalid: %s is required", path)
	}
	if cfg.PackageConfigDir == "" {
		cfg.PackageConfigDir = filepath.ToSlash(filepath.Join("scf", cfg.SpaceID))
	}
	if cfg.Entrypoint == "" {
		cfg.Entrypoint = cfg.SpaceID
	}
	cfg.PackageConfigDir = filepath.ToSlash(filepath.Clean(cfg.PackageConfigDir))
	if cfg.PackageConfigDir == "." || strings.HasPrefix(cfg.PackageConfigDir, "../") || filepath.IsAbs(cfg.PackageConfigDir) {
		return fmt.Errorf("config_invalid: %s.package_config_dir must stay under collector configs", path)
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	cfg.StorageRPCGatewayTarget = strings.TrimSpace(cfg.StorageRPCGatewayTarget)
	if err := validateStorageRPCTarget(cfg.StorageRPCGatewayTarget, path+".storage_rpc_gateway_target"); err != nil {
		return err
	}
	if cfg.Runtime == "" {
		cfg.Runtime = "Go1"
	}
	if cfg.FunctionPrefix == "" {
		cfg.FunctionPrefix = "moox-fetcher-" + cfg.SpaceID
	}
	if !includesSpaceIdentity(cfg.FunctionPrefix, cfg.SpaceID) {
		return fmt.Errorf("config_invalid: %s.function_prefix must include space_id", path)
	}
	if cfg.PackageName == "" {
		cfg.PackageName = "moox-collector-" + cfg.SpaceID
	}
	if !includesSpaceIdentity(cfg.PackageName, cfg.SpaceID) {
		return fmt.Errorf("config_invalid: %s.package_name must include space_id", path)
	}
	cfg.CLSCloudAccountID = strings.TrimSpace(cfg.CLSCloudAccountID)
	if cfg.MemorySize != 64 {
		return fmt.Errorf("config_invalid: %s.memory_size must be 64", path)
	}
	if cfg.TimeoutSeconds != 15 {
		return fmt.Errorf("config_invalid: %s.timeout_seconds must be 15", path)
	}
	if strings.EqualFold(cfg.SpaceID, "stock_cn") {
		if cfg.InstrumentInvokeTimeoutSeconds == 0 {
			cfg.InstrumentInvokeTimeoutSeconds = DefaultStockCNInstrumentInvokeTimeoutSeconds
		}
		if cfg.InstrumentInvokeTimeoutSeconds < DefaultStockCNInstrumentInvokeTimeoutSeconds || cfg.InstrumentInvokeTimeoutSeconds > 900 {
			return fmt.Errorf("config_invalid: %s.instrument_invoke_timeout_seconds must be between %d and 900", path, DefaultStockCNInstrumentInvokeTimeoutSeconds)
		}
	} else if cfg.InstrumentInvokeTimeoutSeconds != 0 {
		return fmt.Errorf("config_invalid: %s.instrument_invoke_timeout_seconds is only valid for stock_cn", path)
	}
	if cfg.RealtimeBatchSize == 0 {
		cfg.RealtimeBatchSize = 30
	}
	if cfg.RealtimeBatchSize < 1 || cfg.RealtimeBatchSize > 30 {
		return fmt.Errorf("config_invalid: %s.realtime_batch_size must be between 1 and 30", path)
	}
	if cfg.RealtimeBarLimit == 0 {
		cfg.RealtimeBarLimit = 3
	}
	if cfg.RealtimeBarLimit != 3 {
		return fmt.Errorf("config_invalid: %s.realtime_bar_limit must be 3", path)
	}
	if cfg.CatchupBatchSize <= 0 {
		cfg.CatchupBatchSize = 1
	}
	if cfg.CatchupBatchSize != 1 {
		return fmt.Errorf("config_invalid: %s.catchup_batch_size must be 1", path)
	}
	if cfg.CatchupBarLimit == 0 {
		cfg.CatchupBarLimit = 1000
	}
	if cfg.CatchupBarLimit != 1000 {
		return fmt.Errorf("config_invalid: %s.catchup_bar_limit must be 1000", path)
	}
	if cfg.MaxInflightRequests <= 0 || cfg.MaxInflightRequests > 64 {
		return fmt.Errorf("config_invalid: %s.max_inflight_requests must be between 1 and 64", path)
	}
	if cfg.RequestTimeoutMS <= 0 || cfg.StorageMaxAttempts < 1 || cfg.StorageMaxAttempts > 3 || cfg.HTTPMaxAttempts != 4 {
		return fmt.Errorf("config_invalid: %s request/storage attempts are invalid", path)
	}
	if cfg.StorageTimeoutMS == 0 {
		cfg.StorageTimeoutMS = 5000
	}
	if cfg.MaxRetryAttempts == 0 {
		cfg.MaxRetryAttempts = 3
	}
	if cfg.StorageTimeoutMS != 5000 || cfg.MaxRetryAttempts != 3 {
		return fmt.Errorf("config_invalid: %s storage_timeout_ms must be 5000 and max_retry_attempts must be 3", path)
	}
	if len(cfg.RetryDelays) == 0 {
		cfg.RetryDelays = []string{"5s", "30s", "2m"}
	}
	if len(cfg.RetryDelays) != 3 || cfg.RetryDelays[0] != "5s" || cfg.RetryDelays[1] != "30s" || cfg.RetryDelays[2] != "2m" || cfg.StaggerEnabled {
		return fmt.Errorf("config_invalid: %s retry_delays must be [5s, 30s, 2m] and stagger_enabled must be false", path)
	}
	requestWaves := (cfg.RealtimeBatchSize + cfg.MaxInflightRequests - 1) / cfg.MaxInflightRequests
	// Standard config-driven publishing creates both the realtime Timer fleet
	// and one Invoke auxiliary per region. Validate the stricter Invoke budget
	// here, before uploading or submitting the Timer batch, so a bad manifest
	// cannot leave a partially published fleet behind.
	requestBudgetMS := requestWaves*cfg.RequestTimeoutMS + cfg.StorageTimeoutMS + SCFCompletionReserveMilliseconds + SCFCLSReserveMilliseconds + SCFFinalResponseReserveMilliseconds
	if requestBudgetMS >= cfg.TimeoutSeconds*1000 {
		return fmt.Errorf("config_invalid: %s realtime request waves + storage_timeout_ms + completion, CLS and final response reserves must be less than timeout", path)
	}
	seen := make(map[string]struct{}, len(cfg.Regions))
	enabledRegions := 0
	for i := range cfg.Regions {
		region := strings.TrimSpace(cfg.Regions[i].Region)
		if region == "" || cfg.Regions[i].FunctionCount < 0 || cfg.Regions[i].FunctionCount > 50 || (cfg.Regions[i].Enabled && strings.TrimSpace(cfg.Regions[i].CloudAccountID) == "") {
			return fmt.Errorf("config_invalid: %s.regions[%d] region, cloud_account_id, and function_count 0..50 are required (0 enables automatic allocation)", path, i)
		}
		if !supportedSCFRegion(region) {
			return fmt.Errorf("config_invalid: %s.regions[%d] region %q is not supported", path, i, region)
		}
		if _, ok := seen[region]; ok {
			return fmt.Errorf("config_invalid: %s region %q is duplicated", path, region)
		}
		seen[region] = struct{}{}
		cfg.Regions[i].Region = region
		cfg.Regions[i].CloudAccountID = strings.TrimSpace(cfg.Regions[i].CloudAccountID)
		cfg.Regions[i].CloudAccountName = strings.TrimSpace(cfg.Regions[i].CloudAccountName)
		cfg.Regions[i].CredentialSecretID = strings.TrimSpace(cfg.Regions[i].CredentialSecretID)
		cfg.Regions[i].AppID = strings.TrimSpace(cfg.Regions[i].AppID)
		cfg.Regions[i].COSBucket = strings.TrimSpace(cfg.Regions[i].COSBucket)
		registrationFields := []string{cfg.Regions[i].CloudAccountName, cfg.Regions[i].CredentialSecretID, cfg.Regions[i].AppID, cfg.Regions[i].COSBucket}
		registrationCount := 0
		for _, field := range registrationFields {
			if field != "" {
				registrationCount++
			}
		}
		if registrationCount != 0 && registrationCount != len(registrationFields) {
			return fmt.Errorf("config_invalid: %s.regions[%d] cloud account registration requires cloud_account_name, credential_secret_id, app_id, and cos_bucket together", path, i)
		}
		if cfg.Regions[i].Enabled {
			enabledRegions++
		}
	}
	if len(cfg.Regions) == 0 {
		return fmt.Errorf("config_invalid: %s.regions must not be empty", path)
	}
	if enabledRegions == 0 {
		return fmt.Errorf("config_invalid: %s.regions must contain at least one enabled region", path)
	}
	if err := resolveSCFTimerFunctionCounts(cfg, path); err != nil {
		return err
	}
	if cfg.CLSCloudAccountID == "" {
		// Keep the central log sink deterministic for the standard MooX fleet
		// while allowing a manifest to name a different dedicated log account.
		for _, region := range cfg.Regions {
			if region.Enabled && region.Region == "ap-guangzhou" {
				cfg.CLSCloudAccountID = region.CloudAccountID
				break
			}
		}
		if cfg.CLSCloudAccountID == "" {
			for _, region := range cfg.Regions {
				if region.Enabled {
					cfg.CLSCloudAccountID = region.CloudAccountID
					break
				}
			}
		}
	}
	return nil
}

func resolveSCFTimerFunctionCounts(cfg *SCFFetcherSpace, path string) error {
	if cfg == nil {
		return fmt.Errorf("config_invalid: %s is required", path)
	}
	explicitTotal := 0
	autoRegions := make([]int, 0)
	enabledRegions := make([]int, 0)
	for index, region := range cfg.Regions {
		if !region.Enabled {
			continue
		}
		enabledRegions = append(enabledRegions, index)
		if region.FunctionCount == 0 {
			autoRegions = append(autoRegions, index)
			continue
		}
		explicitTotal += region.FunctionCount
	}
	desired := cfg.TimerFunctionCount
	if desired <= 0 {
		if explicitTotal > 0 {
			// Preserve older manifests whose regional counts were already the
			// source of truth.
			desired = explicitTotal
		} else {
			desired = DefaultTimerFunctionCount(cfg.SpaceID)
		}
	}
	if desired <= 0 {
		return fmt.Errorf("config_invalid: %s.timer_function_count must be positive for Space %q", path, cfg.SpaceID)
	}
	if explicitTotal > desired {
		return fmt.Errorf("config_invalid: %s regional function_count total %d exceeds timer_function_count %d", path, explicitTotal, desired)
	}
	remaining := desired - explicitTotal
	if len(autoRegions) == 0 {
		if remaining != 0 {
			return fmt.Errorf("config_invalid: %s regional function_count total %d must equal timer_function_count %d", path, explicitTotal, desired)
		}
	} else {
		if remaining < len(autoRegions) {
			return fmt.Errorf("config_invalid: %s timer_function_count %d cannot assign at least one function to each automatic region", path, desired)
		}
		if remaining > len(autoRegions)*50 {
			return fmt.Errorf("config_invalid: %s timer_function_count %d exceeds the 50-function-per-region limit for %d automatic regions", path, desired, len(autoRegions))
		}
		base, extra := remaining/len(autoRegions), remaining%len(autoRegions)
		for order, index := range autoRegions {
			cfg.Regions[index].FunctionCount = base
			if order < extra {
				cfg.Regions[index].FunctionCount++
			}
		}
	}
	actualTotal := 0
	for _, index := range enabledRegions {
		count := cfg.Regions[index].FunctionCount
		if count < 1 || count > 50 {
			return fmt.Errorf("config_invalid: %s.regions[%d] automatic function_count resolved to %d; each enabled region must receive 1..50 functions", path, index, count)
		}
		actualTotal += count
	}
	if actualTotal != desired {
		return fmt.Errorf("config_invalid: %s resolved regional function_count total %d does not equal timer_function_count %d", path, actualTotal, desired)
	}
	cfg.TimerFunctionCount = desired
	return nil
}

func validateStorageRPCTarget(raw, path string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("config_invalid: %s is required", path)
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "ip" || parsed.Hostname() == "" || parsed.Port() == "" {
		return fmt.Errorf("config_invalid: %s must be an ip://host:port target", path)
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if host == "localhost" || host == "ip6-localhost" {
		return fmt.Errorf("config_invalid: %s must not point to loopback", path)
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return fmt.Errorf("config_invalid: %s must not point to loopback", path)
	}
	return nil
}

func includesSpaceIdentity(value, spaceID string) bool {
	return strings.Contains(value, spaceID) || strings.Contains(value, strings.ReplaceAll(spaceID, "_", "-"))
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

var (
	dnsLabelPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	hostNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,127}$`)
)

func validEventBusAddress(address string) bool {
	if address == "" {
		return false
	}
	if ip := net.ParseIP(address); ip != nil {
		return ip.To4() != nil
	}
	if len(address) > 253 || strings.Contains(address, "..") {
		return false
	}
	labels := strings.Split(address, ".")
	for _, label := range labels {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
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
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func hostConfigured(host Host) bool {
	return strings.TrimSpace(host.Name) != "" ||
		strings.TrimSpace(host.Address) != "" ||
		host.Port != 0 ||
		strings.TrimSpace(host.Username) != "" ||
		host.Password != ""
}

func validateHost(path string, host *Host, names, addresses map[string]struct{}, requirePassword bool) error {
	host.Name = strings.TrimSpace(host.Name)
	host.Address = strings.TrimSpace(host.Address)
	host.Username = strings.TrimSpace(host.Username)
	host.TLSMode = strings.ToLower(strings.TrimSpace(host.TLSMode))
	if host.TLSMode != "" && host.TLSMode != "auto" && host.TLSMode != "public" && host.TLSMode != "internal" {
		return fmt.Errorf("config_invalid: %s.tls_mode must be auto, public, or internal", path)
	}
	if host.Name == "" {
		return fmt.Errorf("config_invalid: %s.name is required", path)
	}
	if !hostNamePattern.MatchString(host.Name) {
		return fmt.Errorf("config_invalid: %s.name must use lowercase letters, digits, dash, or underscore", path)
	}
	if host.Address == "" {
		return fmt.Errorf("config_invalid: %s.address is required", path)
	}
	if host.Port < 1 || host.Port > 65535 {
		return fmt.Errorf("config_invalid: %s.port must be between 1 and 65535", path)
	}
	if host.Username == "" {
		return fmt.Errorf("config_invalid: %s.username is required", path)
	}
	if requirePassword && host.Password == "" {
		return fmt.Errorf("config_invalid: %s.password is required", path)
	}
	if names != nil {
		nameKey := strings.ToLower(host.Name)
		if _, exists := names[nameKey]; exists {
			return fmt.Errorf("config_invalid: duplicate host name %s", host.Name)
		}
		names[nameKey] = struct{}{}
	}
	if addresses != nil {
		addressKey := strings.ToLower(host.Address)
		if _, exists := addresses[addressKey]; exists {
			return fmt.Errorf("config_invalid: duplicate host address %s", host.Address)
		}
		addresses[addressKey] = struct{}{}
	}
	return nil
}
