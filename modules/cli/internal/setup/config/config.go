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
	DeliverPolicy         string `toml:"deliver_policy"`
	DeliverPolicyExplicit bool   `toml:"-"`
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
// so defaults must cover every frequency.
type StorageRetention struct {
	ViewBars uint64                       `toml:"view_bars" json:"view_bars"`
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
	MaintenanceInterval          string `toml:"maintenance_interval" json:"maintenance_interval"`
	MaintenanceOffset            string `toml:"maintenance_offset" json:"maintenance_offset"`
	MaintenanceTimeout           string `toml:"maintenance_timeout" json:"maintenance_timeout"`
	MaxRowsPerPass               int    `toml:"max_rows_per_pass" json:"max_rows_per_pass"`
	ExecutionDetailRetention     string `toml:"execution_detail_retention" json:"execution_detail_retention"`
	ScheduledRunSummaryRetention string `toml:"scheduled_run_summary_retention" json:"scheduled_run_summary_retention"`
	TerminalRetryRetention       string `toml:"terminal_retry_retention" json:"terminal_retry_retention"`
	PeriodSnapshotRetention      string `toml:"period_snapshot_retention" json:"period_snapshot_retention"`
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
}

type SSHConfig struct {
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password" json:"-"`
}

type HostDefinition struct {
	Address        string    `toml:"address"`
	PrivateAddress string    `toml:"private_address"`
	Region         string    `toml:"region"`
	Provider       string    `toml:"provider"`
	TLSMode        string    `toml:"tls_mode"`
	SSH            SSHConfig `toml:"ssh"`
}

// CompileHostReference selects credentials from hosts without deploying a
// compiler-only host. An explicit placements entry also makes it a MooX host.
type CompileHostReference struct {
	Host string `toml:"host"`
}

type SCFFetcherRegion struct {
	Region        string `toml:"region"`
	DisplayName   string `toml:"display_name"`
	Enabled       bool   `toml:"enabled"`
	FunctionCount int    `toml:"function_count"`
	// AutoFunctionCount remembers that function_count was omitted/zero in the
	// manifest. Publication can then rebalance only automatic capacity after
	// discovering the live Storage region, without overriding explicit counts.
	AutoFunctionCount bool `toml:"-" json:"-"`

	// These fields are populated from SCFFetcher.CloudAccount during manifest
	// validation. Cloud account identity is global to the SCF fleet, not a
	// property of an individual Tencent region.
	CloudAccountID     string `toml:"-"`
	CloudAccountName   string `toml:"-"`
	CredentialSecretID string `toml:"-"`
	AppID              string `toml:"-"`
	COSRegion          string `toml:"-"`
	COSBucket          string `toml:"-"`
}

type SCFFetcherCloudAccount struct {
	AccountID          string `toml:"account_id"`
	AccountName        string `toml:"account_name"`
	CredentialSecretID string `toml:"credential_secret_id"`
	AppID              string `toml:"app_id"`
	COSRegion          string `toml:"cos_region"`
	COSBucket          string `toml:"cos_bucket"`
}

// TencentSCFLimits records the Tencent Cloud SCF default quotas relevant to
// function publication. Tencent may raise these values for an account or
// namespace, so they are configuration references rather than a substitute
// for a live quota query.
type TencentSCFLimits struct {
	MaxNamespacesPerRegion           int                              `toml:"max_namespaces_per_region"`
	MaxFunctionsPerNamespace         int                              `toml:"max_functions_per_namespace"`
	MaxBurstConcurrencyPerMinute     int                              `toml:"max_burst_concurrency_per_minute"`
	TotalConcurrencyMemoryMBByRegion map[string]int                   `toml:"total_concurrency_memory_mb_by_region"`
	RegionLimits                     map[string]TencentSCFRegionLimit `toml:"region_limits"`
	ReferenceURL                     string                           `toml:"reference_url"`
	ReferenceCheckedAt               string                           `toml:"reference_checked_at"`
}

// TencentSCFRegionLimit is the account-level default quota for one Tencent
// SCF region. The values are deliberately manifest data: Tencent can raise
// them per account, so deployment must be able to override them explicitly.
type TencentSCFRegionLimit struct {
	MaxNamespacesPerRegion   int `toml:"max_namespaces_per_region"`
	MaxFunctionsPerNamespace int `toml:"max_functions_per_namespace"`
	TotalConcurrencyMemoryMB int `toml:"total_concurrency_memory_mb"`
}

func defaultTencentSCFLimits() TencentSCFLimits {
	limits := TencentSCFLimits{
		MaxNamespacesPerRegion:       DefaultSCFMaxNamespacesPerRegion,
		MaxFunctionsPerNamespace:     DefaultSCFMaxFunctionsPerNamespace,
		MaxBurstConcurrencyPerMinute: DefaultSCFMaxBurstConcurrencyPerMinute,
		TotalConcurrencyMemoryMBByRegion: map[string]int{
			"ap-guangzhou":     128000,
			"ap-shanghai":      128000,
			"ap-beijing":       128000,
			"ap-chengdu":       128000,
			"ap-hongkong":      128000,
			"ap-singapore":     64000,
			"ap-tokyo":         64000,
			"na-siliconvalley": 64000,
			"eu-frankfurt":     64000,
			"ap-shanghai-fsi":  64000,
			"ap-shenzhen-fsi":  64000,
		},
		ReferenceURL:       "https://cloud.tencent.com/document/product/583/11637",
		ReferenceCheckedAt: "2026-09-17",
	}
	// Tencent's documentation lists the higher default concurrency quota for
	// Guangzhou, Shanghai, Beijing, Chengdu and Hong Kong. Regions not listed
	// there use the documented 64000MB default.
	primaryMemoryRegions := map[string]bool{
		"ap-guangzhou": true, "ap-shanghai": true, "ap-beijing": true,
		"ap-chengdu": true, "ap-hongkong": true,
	}
	limits.RegionLimits = make(map[string]TencentSCFRegionLimit, len(tencent.SCFRegions()))
	for _, region := range tencent.SCFRegions() {
		memory := 64000
		if primaryMemoryRegions[region.Code] {
			memory = 128000
		}
		limits.RegionLimits[region.Code] = TencentSCFRegionLimit{
			MaxNamespacesPerRegion:   limits.MaxNamespacesPerRegion,
			MaxFunctionsPerNamespace: limits.MaxFunctionsPerNamespace,
			TotalConcurrencyMemoryMB: memory,
		}
	}
	for region, memory := range limits.TotalConcurrencyMemoryMBByRegion {
		if item, ok := limits.RegionLimits[region]; ok {
			item.TotalConcurrencyMemoryMB = memory
			limits.RegionLimits[region] = item
		}
	}
	return limits
}

func (l *TencentSCFLimits) normalize() {
	if l == nil {
		return
	}
	defaults := defaultTencentSCFLimits()
	if l.MaxNamespacesPerRegion == 0 {
		l.MaxNamespacesPerRegion = defaults.MaxNamespacesPerRegion
	}
	if l.MaxFunctionsPerNamespace == 0 {
		l.MaxFunctionsPerNamespace = defaults.MaxFunctionsPerNamespace
	}
	if l.MaxBurstConcurrencyPerMinute == 0 {
		l.MaxBurstConcurrencyPerMinute = defaults.MaxBurstConcurrencyPerMinute
	}
	if strings.TrimSpace(l.ReferenceURL) == "" {
		l.ReferenceURL = defaults.ReferenceURL
	}
	if strings.TrimSpace(l.ReferenceCheckedAt) == "" {
		l.ReferenceCheckedAt = defaults.ReferenceCheckedAt
	}
	normalized := make(map[string]int, len(defaults.TotalConcurrencyMemoryMBByRegion)+len(l.TotalConcurrencyMemoryMBByRegion))
	for region, memory := range defaults.TotalConcurrencyMemoryMBByRegion {
		normalized[region] = memory
	}
	for region, memory := range l.TotalConcurrencyMemoryMBByRegion {
		region = strings.ToLower(strings.TrimSpace(region))
		if region != "" {
			normalized[region] = memory
		}
	}
	l.TotalConcurrencyMemoryMBByRegion = normalized

	regionLimits := make(map[string]TencentSCFRegionLimit, len(defaults.RegionLimits)+len(l.RegionLimits))
	for region, item := range defaults.RegionLimits {
		regionLimits[region] = item
	}
	for region, memory := range normalized {
		if item, ok := regionLimits[region]; ok {
			item.TotalConcurrencyMemoryMB = memory
			regionLimits[region] = item
		}
	}
	for region, item := range l.RegionLimits {
		region = strings.ToLower(strings.TrimSpace(region))
		if region == "" {
			continue
		}
		base := regionLimits[region]
		if item.MaxNamespacesPerRegion > 0 {
			base.MaxNamespacesPerRegion = item.MaxNamespacesPerRegion
		}
		if item.MaxFunctionsPerNamespace > 0 {
			base.MaxFunctionsPerNamespace = item.MaxFunctionsPerNamespace
		}
		if item.TotalConcurrencyMemoryMB > 0 {
			base.TotalConcurrencyMemoryMB = item.TotalConcurrencyMemoryMB
		}
		regionLimits[region] = base
	}
	// Keep the legacy memory map and the explicit regional quota map coherent
	// for callers that still inspect the former field.
	for region, item := range regionLimits {
		l.TotalConcurrencyMemoryMBByRegion[region] = item.TotalConcurrencyMemoryMB
	}
	l.RegionLimits = regionLimits
}

// ForRegion returns the normalized quota for one supported region. It also
// provides a deterministic global fallback for callers constructing limits in
// tests or older manifests without calling normalize first.
func (l TencentSCFLimits) ForRegion(region string) TencentSCFRegionLimit {
	region = strings.ToLower(strings.TrimSpace(region))
	if item, ok := l.RegionLimits[region]; ok {
		return item
	}
	return TencentSCFRegionLimit{
		MaxNamespacesPerRegion:   l.MaxNamespacesPerRegion,
		MaxFunctionsPerNamespace: l.MaxFunctionsPerNamespace,
		TotalConcurrencyMemoryMB: l.TotalConcurrencyMemoryMBByRegion[region],
	}
}

// MaxFunctionsPerRegion is the Tencent default regional function ceiling:
// namespaces in the region multiplied by functions in each namespace.
func (l TencentSCFRegionLimit) MaxFunctionsPerRegion() int {
	if l.MaxNamespacesPerRegion < 1 || l.MaxFunctionsPerNamespace < 1 {
		return 0
	}
	return l.MaxNamespacesPerRegion * l.MaxFunctionsPerNamespace
}

// TimerCapacity is the number of Timer functions one Space may place in a
// region after reserving one Invoke canary. Overflow namespaces are included.
func (l TencentSCFRegionLimit) TimerCapacity(reservedAuxiliary int) int {
	capacity := l.MaxFunctionsPerRegion() - 1 - reservedAuxiliary
	if capacity < 0 {
		return 0
	}
	return capacity
}

// SCFNamespaceShard is one namespace slice of a Space/region fleet. The
// publisher creates these namespaces independently; Collector assignment
// treats the union as the regional Timer fleet.
type SCFNamespaceShard struct {
	Namespace string
	Timers    int
	Invokes   int
}

func (s SCFNamespaceShard) Functions() int {
	return s.Timers + s.Invokes
}

// OverflowSCFNamespace returns the Space namespace at overflow index.
// index 0 is the primary name (moox-crypto); index 1 is moox-crypto-ns2.
func OverflowSCFNamespace(base string, index int) string {
	base = strings.ToLower(strings.TrimSpace(base))
	if index <= 0 {
		return base
	}
	return fmt.Sprintf("%s-ns%d", base, index+1)
}

// PlanSCFNamespaceShards packs timer and invoke functions into namespaces of
// at most maxPerNS functions each. The Invoke canary stays in the first
// namespace so its identity remains stable.
func PlanSCFNamespaceShards(base string, timers, invokes, maxPerNS int) []SCFNamespaceShard {
	if maxPerNS < 1 {
		return nil
	}
	if timers < 0 {
		timers = 0
	}
	if invokes < 0 {
		invokes = 0
	}
	if timers == 0 && invokes == 0 {
		return nil
	}
	firstAux := invokes
	firstTimers := timers
	if firstAux+firstTimers > maxPerNS {
		firstTimers = maxPerNS - firstAux
		if firstTimers < 0 {
			firstTimers = 0
		}
	}
	shards := []SCFNamespaceShard{{
		Namespace: OverflowSCFNamespace(base, 0),
		Timers:    firstTimers,
		Invokes:   invokes,
	}}
	remaining := timers - firstTimers
	for index := 1; remaining > 0; index++ {
		n := remaining
		if n > maxPerNS {
			n = maxPerNS
		}
		shards = append(shards, SCFNamespaceShard{
			Namespace: OverflowSCFNamespace(base, index),
			Timers:    n,
		})
		remaining -= n
	}
	return shards
}

// PlanSCFInvokeNamespaceShards spreads a Scheduler-owned Invoke pool across
// namespaces without creating Timer-triggered functions. Crypto uses this
// model so every execution can publish an asynchronous completion event.
func PlanSCFInvokeNamespaceShards(base string, invokes, maxPerNS int) []SCFNamespaceShard {
	if maxPerNS < 1 || invokes <= 0 {
		return nil
	}
	shards := make([]SCFNamespaceShard, 0, (invokes+maxPerNS-1)/maxPerNS)
	for index, remaining := 0, invokes; remaining > 0; index++ {
		n := remaining
		if n > maxPerNS {
			n = maxPerNS
		}
		shards = append(shards, SCFNamespaceShard{Namespace: OverflowSCFNamespace(base, index), Invokes: n})
		remaining -= n
	}
	return shards
}

func spaceRegionNamespaceShards(space SCFFetcherSpace, region SCFFetcherRegion, limits TencentSCFLimits) []SCFNamespaceShard {
	namespace := strings.ToLower(strings.TrimSpace(space.Namespace))
	if namespace == "" {
		namespace = ExpectedSCFNamespace(space.SpaceID)
	}
	maxPerNS := limits.ForRegion(region.Region).MaxFunctionsPerNamespace
	if maxPerNS < 1 {
		maxPerNS = DefaultSCFMaxFunctionsPerNamespace
	}
	if strings.EqualFold(strings.TrimSpace(space.SpaceID), "crypto") {
		return PlanSCFInvokeNamespaceShards(namespace, region.FunctionCount, maxPerNS)
	}
	return PlanSCFNamespaceShards(namespace, region.FunctionCount, 1, maxPerNS)
}

// SpaceRegionNamespaceShards returns the namespace slices the publisher will
// create for one enabled regional fleet. Crypto is Invoke-only; stockcn keeps
// Timer functions plus one production Invoke slot.
func SpaceRegionNamespaceShards(space SCFFetcherSpace, region SCFFetcherRegion, limits TencentSCFLimits) []SCFNamespaceShard {
	if !region.Enabled || region.FunctionCount <= 0 || space.IsRegionBlacklisted(region.Region) {
		return nil
	}
	return spaceRegionNamespaceShards(space, region, limits)
}

// SpaceRegionReleaseCanaryNamespace returns the namespace reserved for the
// candidate-package release canary. It reuses spare per-namespace quota
// before allocating the next overflow namespace.
func SpaceRegionReleaseCanaryNamespace(space SCFFetcherSpace, region SCFFetcherRegion, limits TencentSCFLimits) (string, error) {
	spaceID := strings.ToLower(strings.TrimSpace(space.SpaceID))
	if spaceID != "crypto" && spaceID != "stockcn" {
		return "", fmt.Errorf("release canary namespace allocation is only defined for crypto and stockcn Invoke pools")
	}
	if !region.Enabled || region.FunctionCount <= 0 || space.IsRegionBlacklisted(region.Region) {
		return "", fmt.Errorf("release canary region must be enabled with a positive function_count")
	}
	limits.normalize()
	regionLimit := limits.ForRegion(region.Region)
	shards := spaceRegionNamespaceShards(space, region, limits)
	if len(shards) == 0 || regionLimit.MaxFunctionsPerNamespace < 1 || regionLimit.MaxNamespacesPerRegion < 1 {
		return "", fmt.Errorf("release canary region %s has no namespace capacity", region.Region)
	}
	for _, shard := range shards {
		if shard.Functions() < regionLimit.MaxFunctionsPerNamespace {
			return shard.Namespace, nil
		}
	}
	if len(shards) >= regionLimit.MaxNamespacesPerRegion {
		return "", fmt.Errorf("release canary region %s has no namespace capacity", region.Region)
	}
	base := strings.ToLower(strings.TrimSpace(space.Namespace))
	if base == "" {
		base = ExpectedSCFNamespace(space.SpaceID)
	}
	return OverflowSCFNamespace(base, len(shards)), nil
}

func (l TencentSCFLimits) validate(path string) error {
	if l.MaxNamespacesPerRegion < 1 {
		return fmt.Errorf("config_invalid: %s.max_namespaces_per_region must be positive", path)
	}
	if l.MaxFunctionsPerNamespace < 1 {
		return fmt.Errorf("config_invalid: %s.max_functions_per_namespace must be positive", path)
	}
	if l.MaxBurstConcurrencyPerMinute < 0 {
		return fmt.Errorf("config_invalid: %s.max_burst_concurrency_per_minute must not be negative", path)
	}
	for region, memory := range l.TotalConcurrencyMemoryMBByRegion {
		if !supportedSCFRegion(region) {
			return fmt.Errorf("config_invalid: %s.total_concurrency_memory_mb_by_region contains unsupported region %q", path, region)
		}
		if memory <= 0 {
			return fmt.Errorf("config_invalid: %s.total_concurrency_memory_mb_by_region[%q] must be positive", path, region)
		}
	}
	for region, item := range l.RegionLimits {
		if !supportedSCFRegion(region) {
			return fmt.Errorf("config_invalid: %s.region_limits contains unsupported region %q", path, region)
		}
		if item.MaxNamespacesPerRegion < 1 || item.MaxFunctionsPerNamespace < 1 || item.TotalConcurrencyMemoryMB <= 0 {
			return fmt.Errorf("config_invalid: %s.region_limits[%q] must define positive namespace, function, and memory limits", path, region)
		}
	}
	return nil
}

// SCFFetcher is the manifest container for independent, space-scoped SCF
// fleets. A function may only consume tasks from its configured space.
type SCFFetcher struct {
	Enabled       bool                   `toml:"enabled"`
	CloudAccount  SCFFetcherCloudAccount `toml:"cloud_account"`
	TencentLimits TencentSCFLimits       `toml:"tencent_limits"`
	Spaces        []SCFFetcherSpace      `toml:"spaces"`
}

// FactorSetup describes the local Python factors, the dataset-backed sets and
// which sets each factor joins, imported by `moox-cli setup init` or
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

// FactorSetupDefinition is intentionally declarative: the source file remains
// the source of truth while this block supplies the runtime contract required
// by FactorMgr. A definition carries no dataset, frequency or status.
type FactorSetupDefinition struct {
	FactorType      string   `toml:"factor_type"`
	FactorID        string   `toml:"factor_id"`
	File            string   `toml:"file"`
	Name            string   `toml:"name"`
	InputColumns    []string `toml:"input_columns"`
	Outputs         []string `toml:"outputs"`
	ParamsJSON      string   `toml:"params_json"`
	LookbackPeriods int      `toml:"lookback_periods"`
}

// FactorSetupMember adds one definition to one set (identified by
// source_dataset_id and freq) with the target run status.
type FactorSetupMember struct {
	SourceDatasetID string `toml:"source_dataset_id"`
	Freq            string `toml:"freq"`
	FactorID        string `toml:"factor_id"`
	Status          string `toml:"status"`
}

// SCFFetcherSpace describes one separately packaged and deployed source
// collector fleet. PackageConfigDir is relative to modules/collector/configs.
type SCFFetcherSpace struct {
	RegionBlacklist []string `toml:"region_blacklist"`
	SpaceID         string   `toml:"space_id"`
	// CanaryTaskID selects a dedicated, disabled, single-series task whose
	// task-owned Dataset and View are used by publication business proofs.
	CanaryTaskID string `toml:"canary_task_id"`
	Entrypoint   string `toml:"entrypoint"`
	// Market-data functions carry this canonical identity in their static
	// environment. It is intentionally separate from the legacy crypto
	// market_type label so a function cannot infer an asset class from a
	// provider-specific spelling.
	MarketID          string `toml:"market_id"`
	InstrumentType    string `toml:"instrument_type"`
	ProviderID        string `toml:"provider_id"`
	SourceID          string `toml:"source_id"`
	SourceBindingMode string `toml:"source_binding_mode"`
	SeriesTag         string `toml:"series_tag"`
	// TDX routing is static, non-secret source configuration. TDXRoutes are
	// candidate IPs; the SCF probes them with the selected protocol before
	// opening the data connection. TDXRouteSnapshotJSON may carry a fresh
	// protocol-proven snapshot generated by an external route job.
	TDXHost              string   `toml:"tdx_host"`
	TDXPort              int      `toml:"tdx_port"`
	TDXRoutes            []string `toml:"tdx_routes"`
	TDXRouteSnapshotJSON string   `toml:"tdx_route_snapshot_json"`
	StorageAppID         string   `toml:"storage_app_id"`
	StorageAppKey        string   `toml:"storage_app_key"`
	StorageOperator      string   `toml:"storage_operator"`
	StorageRequestID     string   `toml:"storage_request_id"`
	PackageConfigDir     string   `toml:"package_config_dir"`
	PackageName          string   `toml:"package_name"`
	// CLSCloudAccountID owns the single regional CLS topic used by every
	// short-lived collector function in this space, regardless of its SCF region.
	CLSCloudAccountID string `toml:"cls_cloud_account_id"`
	Namespace         string `toml:"namespace"`
	Runtime           string `toml:"runtime"`
	FunctionPrefix    string `toml:"function_prefix"`
	// PublicNetStatus is rendered into the CloudNode SCF configuration. HTTP
	// market providers need explicit public egress; VPC/NAT remains a separate
	// deployment choice and must not be inferred from the region name.
	PublicNetStatus string `toml:"public_net_status"`
	// TimerFunctionCount is retained as the manifest field name for fleet capacity.
	// It is a Timer count for stockcn and an Invoke-pool count for crypto. stockcn
	// must set it explicitly; other Spaces may use the built-in default.
	TimerFunctionCount        int    `toml:"timer_function_count"`
	MeasuredSafeGroupSize     int    `toml:"measured_safe_group_size"`
	StaggerStartSecond        int    `toml:"stagger_start_second"`
	StaggerWindowSeconds      int    `toml:"stagger_window_seconds"`
	StaggerMaxStartsPerSecond int    `toml:"stagger_max_starts_per_second"`
	AccessID                  string `toml:"-"`
	AccessHost                string `toml:"-"`
	AccessAddress             string `toml:"-"`
	AccessPrivateHost         string `toml:"-"`
	AccessPrivateAddress      string `toml:"-"`
	// AccessAddresses contains regional candidates derived from placements.
	// Deployment verifies VPC membership before selecting private endpoints.
	AccessAddresses map[string]string `toml:"-"`
	// AccessIDs contains the canonical access@host-id identity per region.
	AccessIDs      map[string]string `toml:"-"`
	MemorySize     int               `toml:"memory_size"`
	TimeoutSeconds int               `toml:"timeout_seconds"`
	// InvokeTimeoutSeconds is used by deployment canary Invoke nodes. Timer
	// nodes retain TimeoutSeconds.
	InvokeTimeoutSeconds int                `toml:"invoke_timeout_seconds"`
	RealtimeBatchSize    int                `toml:"realtime_batch_size"`
	RealtimeBarLimit     int                `toml:"realtime_bar_limit"`
	CatchupBatchSize     int                `toml:"catchup_batch_size"`
	CatchupBarLimit      int                `toml:"catchup_bar_limit"`
	MaxInflightRequests  int                `toml:"max_inflight_requests"`
	RequestTimeoutMS     int                `toml:"request_timeout_ms"`
	HTTPMaxAttempts      int                `toml:"http_max_attempts"`
	StorageTimeoutMS     int                `toml:"storage_timeout_ms"`
	MaxRetryAttempts     int                `toml:"max_retry_attempts"`
	RetryDelays          []string           `toml:"retry_delays"`
	StaggerEnabled       bool               `toml:"stagger_enabled"`
	Regions              []SCFFetcherRegion `toml:"regions"`
}

// AccessAddressForRegion returns the normalized regional Access target for one
// SCF region, if the manifest explicitly configured one.
func (s SCFFetcherSpace) AccessAddressForRegion(region string) string {
	region = strings.ToLower(strings.TrimSpace(region))
	for configuredRegion, target := range s.AccessAddresses {
		if strings.EqualFold(strings.TrimSpace(configuredRegion), region) {
			return strings.TrimSpace(target)
		}
	}
	return ""
}

// AccessIDForRegion returns the target-node identity for one SCF
// region. Regional Access nodes use distinct replay namespaces; callers that
// do not configure a regional identity retain the legacy central node.
func (s SCFFetcherSpace) AccessIDForRegion(region string) string {
	region = strings.ToLower(strings.TrimSpace(region))
	for configuredRegion, node := range s.AccessIDs {
		if strings.EqualFold(strings.TrimSpace(configuredRegion), region) {
			return strings.TrimSpace(node)
		}
	}
	return strings.TrimSpace(s.AccessID)
}

// DefaultTimerFunctionCount returns the built-in Timer capacity for a known
// Space. Unknown Spaces must provide regional function_count values or an
// explicit timer_function_count.
func DefaultTimerFunctionCount(spaceID string) int {
	switch strings.ToLower(strings.TrimSpace(spaceID)) {
	case "crypto":
		return DefaultCryptoMarketTimerFunctionCount
	case "stockcn":
		return 0
	default:
		return 0
	}
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
	SCFFetcher         SCFFetcher                `toml:"scf_fetcher"`
	HostCatalog        map[string]HostDefinition `toml:"hosts"`
	CompileHostRef     CompileHostReference      `toml:"compile_host"`
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
			if strings.Contains(first, "access_") || strings.Contains(first, "gateway") {
				return fmt.Errorf("config_invalid: SCF access/gateway settings are derived from [hosts.<host-id>] and [placements]; remove the retired field")
			}
		case "eventbus":
			if first == "eventbus.host" {
				return fmt.Errorf("config_invalid: eventbus.host is derived from the eventbus component in [placements]")
			}
		case "dns_resolver":
			return fmt.Errorf("config_invalid: dns_resolver is retired; use [egress_proxy.dns] and remove trade_node")
		case "compile_host":
			return fmt.Errorf("config_invalid: compile_host accepts only host = <host-id>; SSH credentials belong to hosts.<host-id>.ssh")
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
		definition.Address = strings.TrimSpace(definition.Address)
		definition.PrivateAddress = strings.TrimSpace(definition.PrivateAddress)
		definition.Region = strings.ToLower(strings.TrimSpace(definition.Region))
		definition.Provider = strings.ToLower(strings.TrimSpace(definition.Provider))
		definition.TLSMode = strings.ToLower(strings.TrimSpace(definition.TLSMode))
		definition.SSH.Username = strings.TrimSpace(definition.SSH.Username)
		if !md.IsDefined("hosts", id, "ssh", "port") {
			definition.SSH.Port = 22
		}
		out.HostCatalog[id] = definition
	}
	out.CompileHostRef.Host = strings.TrimSpace(out.CompileHostRef.Host)
	out.Paths = out.Paths.Resolved()
	if !md.IsDefined("factors", "enabled") {
		out.Factors.Enabled = false
	}
	recommended := storagepolicy.Default()
	if !md.IsDefined("storage_retention", "view_bars") {
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
		out.StorageView.MaxViewFileBytes = 1 << 30
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
	if !md.IsDefined("collector_retention", "execution_detail_retention") {
		out.CollectorRetention.ExecutionDetailRetention = "6h"
	}
	if !md.IsDefined("collector_retention", "scheduled_run_summary_retention") {
		out.CollectorRetention.ScheduledRunSummaryRetention = "720h"
	}
	if !md.IsDefined("collector_retention", "terminal_retry_retention") {
		out.CollectorRetention.TerminalRetryRetention = "168h"
	}
	if !md.IsDefined("collector_retention", "period_snapshot_retention") {
		out.CollectorRetention.PeriodSnapshotRetention = "720h"
	}
	if !md.IsDefined("local_logs", "max_size_mb") {
		out.LocalLogs.MaxSizeMB = 50
	}
	out.Observability.DeliverPolicyExplicit = md.IsDefined("observability", "deliver_policy")
	if !out.Observability.DeliverPolicyExplicit {
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
	if err := validateSCFFetcher(&manifest.SCFFetcher); err != nil {
		return err
	}
	if err := validateFactorSetup(&manifest.Factors); err != nil {
		return err
	}
	policy := manifest.StoragePolicy()
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("config_invalid: storage policy: %w", err)
	}
	// Session-market estimates are only warnings; `moox-cli config plan`
	// reports them. A 7×24 retention that cannot hold view_bars is an error.
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
	return validatePlacements(manifest)
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
	if cfg.MaxRowsPerPass < 9 || cfg.MaxRowsPerPass > 50000 {
		return fmt.Errorf("config_invalid: collector_retention.max_rows_per_pass must be between 9 and 50000")
	}
	for field, raw := range map[string]string{
		"execution_detail_retention":      cfg.ExecutionDetailRetention,
		"scheduled_run_summary_retention": cfg.ScheduledRunSummaryRetention,
		"terminal_retry_retention":        cfg.TerminalRetryRetention,
		"period_snapshot_retention":       cfg.PeriodSnapshotRetention,
	} {
		duration, err := time.ParseDuration(strings.TrimSpace(raw))
		if err != nil || duration <= 0 || duration > 365*24*time.Hour {
			return fmt.Errorf("config_invalid: collector_retention.%s must be greater than 0 and at most 365 days", field)
		}
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
		item.FactorType = strings.TrimSpace(item.FactorType)
		if item.FactorType != "timeseries" && item.FactorType != "cross_section" {
			return fmt.Errorf("config_invalid: %s.factor_type must be timeseries or cross_section", path)
		}
		item.FactorID = strings.TrimSpace(item.FactorID)
		item.File = filepath.ToSlash(filepath.Clean(strings.TrimSpace(item.File)))
		item.Name = strings.TrimSpace(item.Name)
		if item.FactorID == "" || item.File == "" {
			return fmt.Errorf("config_invalid: %s requires factor_id and file", path)
		}
		if filepath.IsAbs(item.File) || item.File == ".." || strings.HasPrefix(item.File, "../") {
			return fmt.Errorf("config_invalid: %s.file must stay under factors.source_dir", path)
		}
		if _, ok := seen[item.FactorID]; ok {
			return fmt.Errorf("config_invalid: factors definition %q is duplicated", item.FactorID)
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

func validateSCFFetcher(cfg *SCFFetcher) error {
	if cfg == nil {
		return nil
	}
	cfg.TencentLimits.normalize()
	if !cfg.Enabled {
		return nil
	}
	if err := cfg.TencentLimits.validate("scf_fetcher.tencent_limits"); err != nil {
		return err
	}
	account := &cfg.CloudAccount
	account.AccountID = strings.TrimSpace(account.AccountID)
	account.AccountName = strings.TrimSpace(account.AccountName)
	account.CredentialSecretID = strings.TrimSpace(account.CredentialSecretID)
	account.AppID = strings.TrimSpace(account.AppID)
	account.COSRegion = strings.TrimSpace(account.COSRegion)
	account.COSBucket = strings.TrimSpace(account.COSBucket)
	if account.AccountID == "" || account.AccountName == "" || account.CredentialSecretID == "" || account.AppID == "" || account.COSRegion == "" || account.COSBucket == "" {
		return fmt.Errorf("config_invalid: scf_fetcher.cloud_account requires account_id, account_name, credential_secret_id, app_id, cos_region, and cos_bucket")
	}
	if !supportedSCFRegion(account.COSRegion) {
		return fmt.Errorf("config_invalid: scf_fetcher.cloud_account.cos_region %q is not supported", account.COSRegion)
	}
	if len(cfg.Spaces) == 0 {
		return fmt.Errorf("config_invalid: scf_fetcher.spaces must not be empty when enabled")
	}
	seenSpaces := make(map[string]struct{}, len(cfg.Spaces))
	if err := validateSCFSharedNamespaceAutoAllocation(cfg); err != nil {
		return err
	}
	for index := range cfg.Spaces {
		spaceID := strings.TrimSpace(cfg.Spaces[index].SpaceID)
		if spaceID == "" {
			return fmt.Errorf("config_invalid: scf_fetcher.spaces[%d].space_id is required", index)
		}
		if _, exists := seenSpaces[spaceID]; exists {
			return fmt.Errorf("config_invalid: scf_fetcher space %q is duplicated", spaceID)
		}
		seenSpaces[spaceID] = struct{}{}
		if err := validateSCFNamespace(&cfg.Spaces[index], fmt.Sprintf("scf_fetcher.spaces[%d]", index)); err != nil {
			return err
		}
		for regionIndex := range cfg.Spaces[index].Regions {
			region := &cfg.Spaces[index].Regions[regionIndex]
			region.CloudAccountID = account.AccountID
			region.CloudAccountName = account.AccountName
			region.CredentialSecretID = account.CredentialSecretID
			region.AppID = account.AppID
			region.COSRegion = account.COSRegion
			region.COSBucket = account.COSBucket
		}
		cfg.Spaces[index].SpaceID = spaceID
		if err := validateSCFFetcherSpaceWithLimits(&cfg.Spaces[index], fmt.Sprintf("scf_fetcher.spaces[%d]", index), cfg.TencentLimits); err != nil {
			return err
		}
	}
	for index := range cfg.Spaces {
		accountID := cfg.Spaces[index].CLSCloudAccountID
		if accountID == "" {
			cfg.Spaces[index].CLSCloudAccountID = account.AccountID
			continue
		}
		if accountID != account.AccountID {
			return fmt.Errorf("config_invalid: scf_fetcher.spaces[%d].cls_cloud_account_id must match scf_fetcher.cloud_account.account_id", index)
		}
	}
	return nil
}

// ExpectedSCFNamespace returns the primary namespace assigned to one MooX
// Space. Additional overflow namespaces are derived from this name when a
// region needs more than one namespace of functions. Unrelated Spaces still
// keep dedicated primary names so they do not share a per-namespace quota.
func ExpectedSCFNamespace(spaceID string) string {
	spaceID = strings.ToLower(strings.TrimSpace(spaceID))
	spaceID = strings.ReplaceAll(spaceID, "_", "-")
	return "moox-" + spaceID
}

func validateSCFNamespace(cfg *SCFFetcherSpace, path string) error {
	if cfg == nil {
		return fmt.Errorf("config_invalid: %s is required", path)
	}
	expected := ExpectedSCFNamespace(cfg.SpaceID)
	namespace := strings.ToLower(strings.TrimSpace(cfg.Namespace))
	if namespace == "" {
		namespace = expected
	}
	if namespace != expected || namespace == "default" {
		return fmt.Errorf("config_invalid: %s.namespace must be %q (primary namespace per Space; default is not allowed)", path, expected)
	}
	if len(namespace) > 60 || !regexp.MustCompile(`^moox-[a-z0-9]+(?:-[a-z0-9]+)*$`).MatchString(namespace) {
		return fmt.Errorf("config_invalid: %s.namespace %q must match moox-<space-id>", path, namespace)
	}
	cfg.Namespace = namespace
	return nil
}

// Automatic regional allocation is performed per Space. Sharing a namespace
// between Spaces would make each allocator unaware of the other's fixed and
// auxiliary functions, so a locally feasible allocation could still exceed
// the aggregate Tencent quota. Require explicit counts for that uncommon
// layout instead of silently producing an invalid plan.
func validateSCFSharedNamespaceAutoAllocation(cfg *SCFFetcher) error {
	owners := make(map[string][]int)
	for index, space := range cfg.Spaces {
		namespace := strings.ToLower(strings.TrimSpace(space.Namespace))
		if namespace == "" {
			namespace = "default"
		}
		owners[namespace] = append(owners[namespace], index)
	}
	for namespace, indexes := range owners {
		if len(indexes) < 2 {
			continue
		}
		for _, index := range indexes {
			for _, region := range cfg.Spaces[index].Regions {
				if region.Enabled && region.FunctionCount == 0 {
					return fmt.Errorf("config_invalid: scf namespace %q is shared by multiple Spaces; automatic function allocation requires dedicated namespaces or explicit function_count values", namespace)
				}
			}
		}
	}
	return nil
}

// ValidateSCFCapacities accounts for production Timer/Invoke functions and
// one isolated release-canary function per active Crypto/StockCN region. A
// Space may occupy multiple namespaces when one namespace's quota is full.
func ValidateSCFCapacities(cfg *SCFFetcher, limits TencentSCFLimits) error {
	if cfg == nil {
		return nil
	}
	limits.normalize()
	type regionNamespace struct {
		region    string
		namespace string
	}
	namespaces := make(map[string]map[string]struct{})
	functions := make(map[regionNamespace]int)
	add := func(region, namespace string, count int) {
		region = strings.ToLower(strings.TrimSpace(region))
		namespace = strings.ToLower(strings.TrimSpace(namespace))
		if region == "" || namespace == "" || count <= 0 {
			return
		}
		if namespaces[region] == nil {
			namespaces[region] = make(map[string]struct{})
		}
		namespaces[region][namespace] = struct{}{}
		functions[regionNamespace{region: region, namespace: namespace}] += count
	}
	for _, space := range cfg.Spaces {
		for _, region := range space.Regions {
			if !region.Enabled || region.FunctionCount <= 0 || space.IsRegionBlacklisted(region.Region) {
				continue
			}
			for _, shard := range spaceRegionNamespaceShards(space, region, limits) {
				add(region.Region, shard.Namespace, shard.Functions())
			}
			if strings.EqualFold(strings.TrimSpace(space.SpaceID), "crypto") || strings.EqualFold(strings.TrimSpace(space.SpaceID), "stockcn") {
				namespace, err := SpaceRegionReleaseCanaryNamespace(space, region, limits)
				if err != nil {
					return fmt.Errorf("config_invalid: Space %s region %s release canary: %w", space.SpaceID, region.Region, err)
				}
				add(region.Region, namespace, 1)
			}
		}
	}
	for region, regionNamespaces := range namespaces {
		regionLimit := limits.ForRegion(region)
		if len(regionNamespaces) > regionLimit.MaxNamespacesPerRegion {
			return fmt.Errorf("config_invalid: scf region %s uses %d namespaces, above max_namespaces_per_region %d", region, len(regionNamespaces), regionLimit.MaxNamespacesPerRegion)
		}
	}
	for key, count := range functions {
		regionLimit := limits.ForRegion(key.region)
		if count > regionLimit.MaxFunctionsPerNamespace {
			return fmt.Errorf("config_invalid: scf region %s namespace %s uses %d functions including publisher auxiliaries, above max_functions_per_namespace %d", key.region, key.namespace, count, regionLimit.MaxFunctionsPerNamespace)
		}
	}
	return nil
}

func validateSCFFetcherSpaceWithLimits(cfg *SCFFetcherSpace, path string, limits TencentSCFLimits) error {
	if cfg == nil {
		return fmt.Errorf("config_invalid: %s is required", path)
	}
	limits.normalize()
	if err := limits.validate("scf_fetcher.tencent_limits"); err != nil {
		return err
	}
	if err := validateSCFNamespace(cfg, path); err != nil {
		return err
	}
	if err := normalizeSCFRegionBlacklist(cfg, path); err != nil {
		return err
	}
	if cfg.PackageConfigDir == "" {
		cfg.PackageConfigDir = filepath.ToSlash(filepath.Join("scf", cfg.SpaceID))
	}
	if cfg.Entrypoint == "" {
		cfg.Entrypoint = cfg.SpaceID
	}
	if strings.EqualFold(cfg.Entrypoint, "market_data") {
		cfg.PublicNetStatus = strings.ToUpper(strings.TrimSpace(cfg.PublicNetStatus))
		if cfg.PublicNetStatus == "" {
			cfg.PublicNetStatus = "ENABLE"
		}
		if cfg.PublicNetStatus != "ENABLE" && cfg.PublicNetStatus != "DISABLE" {
			return fmt.Errorf("config_invalid: %s.public_net_status must be ENABLE or DISABLE", path)
		}
		cfg.SourceBindingMode = strings.ToLower(strings.TrimSpace(cfg.SourceBindingMode))
		if cfg.SourceBindingMode != "" && cfg.SourceBindingMode != "deterministic" {
			return fmt.Errorf("config_invalid: %s.source_binding_mode must be deterministic", path)
		}
		deterministicStockSources := strings.EqualFold(cfg.SpaceID, "stockcn") && cfg.SourceBindingMode == "deterministic"
		requiredFields := map[string]string{
			"market_id":       cfg.MarketID,
			"instrument_type": cfg.InstrumentType,
		}
		for field, value := range requiredFields {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("config_invalid: %s.%s is required for market_data entrypoint", path, field)
			}
		}
		if !deterministicStockSources {
			for field, value := range map[string]string{
				"provider_id": cfg.ProviderID,
				"source_id":   cfg.SourceID,
			} {
				if strings.TrimSpace(value) == "" {
					return fmt.Errorf("config_invalid: %s.%s is required for market_data entrypoint", path, field)
				}
			}
		} else if strings.TrimSpace(cfg.ProviderID) != "" || strings.TrimSpace(cfg.SourceID) != "" {
			return fmt.Errorf("config_invalid: %s.provider_id/source_id must be omitted for deterministic stock sources", path)
		}
		if strings.EqualFold(strings.TrimSpace(cfg.ProviderID), "tdx") {
			cfg.TDXHost = strings.TrimSpace(cfg.TDXHost)
			if cfg.TDXHost == "" {
				return fmt.Errorf("config_invalid: %s.tdx_host is required for tdx market_data", path)
			}
			if cfg.TDXPort == 0 {
				cfg.TDXPort = 7709
			}
			if cfg.TDXPort < 1 || cfg.TDXPort > 65535 {
				return fmt.Errorf("config_invalid: %s.tdx_port must be between 1 and 65535", path)
			}
			sourceID := strings.ToLower(strings.TrimSpace(cfg.SourceID))
			if sourceID == "normal_7709" && cfg.TDXPort != 7709 {
				return fmt.Errorf("config_invalid: %s.tdx_port must be 7709 for normal_7709", path)
			}
			if (sourceID == "ex_classic_7727" || sourceID == "ex_mac_7727") && cfg.TDXPort != 7727 {
				return fmt.Errorf("config_invalid: %s.tdx_port must be 7727 for %s", path, sourceID)
			}
			seenRoutes := make(map[string]struct{}, len(cfg.TDXRoutes))
			if len(cfg.TDXRoutes) > 64 {
				return fmt.Errorf("config_invalid: %s.tdx_routes must contain at most 64 IPs", path)
			}
			for index, rawRoute := range cfg.TDXRoutes {
				ip := net.ParseIP(strings.TrimSpace(rawRoute))
				if ip == nil {
					return fmt.Errorf("config_invalid: %s.tdx_routes[%d] must be an IP address", path, index)
				}
				canonical := ip.String()
				if _, exists := seenRoutes[canonical]; exists {
					return fmt.Errorf("config_invalid: %s.tdx_routes contains duplicate IP %s", path, canonical)
				}
				seenRoutes[canonical] = struct{}{}
				cfg.TDXRoutes[index] = canonical
			}
			cfg.TDXRouteSnapshotJSON = strings.TrimSpace(cfg.TDXRouteSnapshotJSON)
		}
	}
	cfg.PackageConfigDir = filepath.ToSlash(filepath.Clean(cfg.PackageConfigDir))
	if cfg.PackageConfigDir == "." || strings.HasPrefix(cfg.PackageConfigDir, "../") || filepath.IsAbs(cfg.PackageConfigDir) {
		return fmt.Errorf("config_invalid: %s.package_config_dir must stay under collector configs", path)
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	cfg.AccessAddress = strings.TrimSpace(cfg.AccessAddress)
	if err := validateAccessAddress(cfg.AccessAddress, path+".access_address"); err != nil {
		return err
	}
	if len(cfg.AccessAddresses) > 0 {
		normalizedAccessTargets := make(map[string]string, len(cfg.AccessAddresses))
		for rawRegion, rawTarget := range cfg.AccessAddresses {
			region := strings.ToLower(strings.TrimSpace(rawRegion))
			if !supportedSCFRegion(region) {
				return fmt.Errorf("config_invalid: %s.access_addresses[%q] uses unsupported SCF region", path, rawRegion)
			}
			target := strings.TrimSpace(rawTarget)
			if err := validateAccessAddress(target, fmt.Sprintf("%s.access_addresses[%s]", path, region)); err != nil {
				return err
			}
			if _, exists := normalizedAccessTargets[region]; exists {
				return fmt.Errorf("config_invalid: %s.access_addresses contains duplicate region %q", path, region)
			}
			normalizedAccessTargets[region] = target
		}
		cfg.AccessAddresses = normalizedAccessTargets
	}
	if len(cfg.AccessIDs) > 0 {
		normalizedAccessNodes := make(map[string]string, len(cfg.AccessIDs))
		for rawRegion, rawNode := range cfg.AccessIDs {
			region := strings.ToLower(strings.TrimSpace(rawRegion))
			if !supportedSCFRegion(region) {
				return fmt.Errorf("config_invalid: %s.access_ids[%q] uses unsupported SCF region", path, rawRegion)
			}
			node := strings.ToLower(strings.TrimSpace(rawNode))
			if !validAccessInstanceID(node) {
				return fmt.Errorf("config_invalid: %s.access_ids[%s] must be access@host", path, region)
			}
			if _, exists := normalizedAccessNodes[region]; exists {
				return fmt.Errorf("config_invalid: %s.access_ids contains duplicate region %q", path, region)
			}
			normalizedAccessNodes[region] = node
		}
		cfg.AccessIDs = normalizedAccessNodes
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
	// Legacy manifests used the pre-Claim 15-second Timer budget. Upgrade that
	// value at the trusted configuration boundary rather than deploying it.
	if cfg.TimeoutSeconds == 0 || cfg.TimeoutSeconds == 15 {
		cfg.TimeoutSeconds = DefaultSCFTimerTimeoutSeconds
	}
	if cfg.TimeoutSeconds != DefaultSCFTimerTimeoutSeconds {
		return fmt.Errorf("config_invalid: %s.timeout_seconds must be %d", path, DefaultSCFTimerTimeoutSeconds)
	}
	stockCN := strings.EqualFold(cfg.SpaceID, "stockcn")
	if stockCN {
		if cfg.TimerFunctionCount <= 0 {
			return fmt.Errorf("config_invalid: %s.timer_function_count must be an explicit positive value for stockcn", path)
		}
		if cfg.MeasuredSafeGroupSize < 1 || cfg.MeasuredSafeGroupSize > StockCNMaxRealtimeItems {
			return fmt.Errorf("config_invalid: %s.measured_safe_group_size must be between 1 and %d for stockcn", path, StockCNMaxRealtimeItems)
		}
		if cfg.StaggerStartSecond == 0 && cfg.StaggerWindowSeconds == 0 && cfg.StaggerMaxStartsPerSecond == 0 {
			cfg.StaggerStartSecond = DefaultStockCNStaggerStartSecond
			cfg.StaggerWindowSeconds = DefaultStockCNStaggerWindowSeconds
			cfg.StaggerMaxStartsPerSecond = DefaultStockCNStaggerMaxStartsPerSecond
		}
		if cfg.StaggerStartSecond < 0 || cfg.StaggerStartSecond > 59 {
			return fmt.Errorf("config_invalid: %s.stagger_start_second must be between 0 and 59", path)
		}
		if cfg.StaggerWindowSeconds <= 0 || cfg.StaggerWindowSeconds > 60 || cfg.StaggerStartSecond+cfg.StaggerWindowSeconds > 60 {
			return fmt.Errorf("config_invalid: %s.stagger_window_seconds must fit between second %d and 59", path, cfg.StaggerStartSecond)
		}
		if cfg.StaggerMaxStartsPerSecond <= 0 {
			return fmt.Errorf("config_invalid: %s.stagger_max_starts_per_second must be positive", path)
		}
		startsPerSecond := (cfg.TimerFunctionCount + cfg.StaggerWindowSeconds - 1) / cfg.StaggerWindowSeconds
		if startsPerSecond > cfg.StaggerMaxStartsPerSecond {
			return fmt.Errorf("config_invalid: %s timer_function_count %d requires up to %d starts per second, above stagger_max_starts_per_second %d", path, cfg.TimerFunctionCount, startsPerSecond, cfg.StaggerMaxStartsPerSecond)
		}
		if cfg.InvokeTimeoutSeconds == 0 {
			cfg.InvokeTimeoutSeconds = DefaultStockCNInvokeTimeoutSeconds
		}
		if cfg.InvokeTimeoutSeconds < DefaultStockCNInvokeTimeoutSeconds || cfg.InvokeTimeoutSeconds > 900 {
			return fmt.Errorf("config_invalid: %s.invoke_timeout_seconds must be between %d and 900", path, DefaultStockCNInvokeTimeoutSeconds)
		}
	} else if cfg.MeasuredSafeGroupSize != 0 {
		return fmt.Errorf("config_invalid: %s.measured_safe_group_size is only valid for stockcn", path)
	} else if strings.EqualFold(cfg.SpaceID, "crypto") {
		if cfg.InvokeTimeoutSeconds == 0 {
			cfg.InvokeTimeoutSeconds = DefaultCryptoInvokeTimeoutSeconds
		}
		if cfg.InvokeTimeoutSeconds < DefaultCryptoInvokeTimeoutSeconds || cfg.InvokeTimeoutSeconds > 900 {
			return fmt.Errorf("config_invalid: %s.invoke_timeout_seconds must be between %d and 900", path, DefaultCryptoInvokeTimeoutSeconds)
		}
	} else if cfg.InvokeTimeoutSeconds != 0 {
		return fmt.Errorf("config_invalid: %s.invoke_timeout_seconds is only valid for stockcn or crypto", path)
	} else if cfg.StaggerStartSecond != 0 || cfg.StaggerWindowSeconds != 0 || cfg.StaggerMaxStartsPerSecond != 0 {
		return fmt.Errorf("config_invalid: %s stagger settings are only valid for stockcn", path)
	}
	if cfg.RealtimeBatchSize == 0 {
		cfg.RealtimeBatchSize = 30
	}
	if cfg.RealtimeBatchSize < 1 || cfg.RealtimeBatchSize > 30 {
		return fmt.Errorf("config_invalid: %s.realtime_batch_size must be between 1 and 30", path)
	}
	if cfg.RealtimeBarLimit == 0 {
		cfg.RealtimeBarLimit = 10
	}
	if cfg.RealtimeBarLimit != 10 {
		return fmt.Errorf("config_invalid: %s.realtime_bar_limit must be 10", path)
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
	if cfg.RequestTimeoutMS <= 0 || cfg.HTTPMaxAttempts != 4 {
		return fmt.Errorf("config_invalid: %s request timeout or HTTP attempts are invalid", path)
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
	if stockCN {
		// Invoke can traverse every active provider; Timer is source-bound and
		// capped by both measured group capacity and the runtime request limit.
		invokeBudget := MarketFetchBudgetMS(cfg.RealtimeBatchSize, cfg.MaxInflightRequests, StockCNInvokeProviderChainLength, cfg.HTTPMaxAttempts, cfg.RequestTimeoutMS, cfg.StorageTimeoutMS, false)
		if invokeBudget >= cfg.InvokeTimeoutSeconds*1000 {
			return fmt.Errorf("config_invalid: %s Invoke request waves across active providers + storage and completion/CLS reserves must be less than invoke_timeout_seconds", path)
		}
		timerItems := min(cfg.MeasuredSafeGroupSize, StockCNMaxRealtimeItems)
		timerBudget := MarketFetchBudgetMS(timerItems, cfg.MaxInflightRequests, 1, cfg.HTTPMaxAttempts, cfg.RequestTimeoutMS, cfg.StorageTimeoutMS, true)
		if timerBudget >= cfg.TimeoutSeconds*1000 {
			return fmt.Errorf("config_invalid: %s Timer request waves at measured_safe_group_size + Claim/storage/completion/CLS reserves must be less than timeout_seconds", path)
		}
	} else if MarketFetchBudgetMS(cfg.RealtimeBatchSize, cfg.MaxInflightRequests, 1, cfg.HTTPMaxAttempts, cfg.RequestTimeoutMS, cfg.StorageTimeoutMS, true) >= cfg.TimeoutSeconds*1000 {
		return fmt.Errorf("config_invalid: %s realtime request waves + storage_timeout_ms + completion, CLS and final response reserves must be less than timeout", path)
	}
	seen := make(map[string]struct{}, len(cfg.Regions))
	enabledRegions := 0
	for i := range cfg.Regions {
		region := strings.TrimSpace(cfg.Regions[i].Region)
		regionLimit := limits.ForRegion(region)
		maxFunctions := regionLimit.TimerCapacity(0)
		if strings.EqualFold(strings.TrimSpace(cfg.SpaceID), "crypto") {
			maxFunctions = regionLimit.MaxFunctionsPerRegion()
		}
		if maxFunctions < 1 && regionLimit.MaxFunctionsPerNamespace > 0 {
			maxFunctions = regionLimit.MaxFunctionsPerNamespace
		}
		if region == "" || cfg.Regions[i].FunctionCount < 0 || (maxFunctions > 0 && cfg.Regions[i].FunctionCount > maxFunctions) || (cfg.Regions[i].Enabled && strings.TrimSpace(cfg.Regions[i].CloudAccountID) == "") {
			return fmt.Errorf("config_invalid: %s.regions[%d] region and function_count 0..%d are required (0 enables automatic allocation)", path, i, maxFunctions)
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
		cfg.Regions[i].COSRegion = strings.TrimSpace(cfg.Regions[i].COSRegion)
		cfg.Regions[i].COSBucket = strings.TrimSpace(cfg.Regions[i].COSBucket)
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
	if err := resolveSCFTimerFunctionCountsWithRegionalCapacities(cfg, path, limits, nil); err != nil {
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

func resolveSCFTimerFunctionCountsWithRegionalCapacities(cfg *SCFFetcherSpace, path string, limits TencentSCFLimits, reservedByRegion map[string]int) error {
	limits.normalize()
	return resolveSCFTimerFunctionCountsWithCapacityFunc(cfg, path, func(region string) int {
		reserved := reservedByRegion[strings.ToLower(strings.TrimSpace(region))]
		limit := limits.ForRegion(region)
		if cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.SpaceID), "crypto") {
			capacity := limit.MaxFunctionsPerRegion() - 1 - reserved
			if capacity < 0 {
				return 0
			}
			return capacity
		}
		return limit.TimerCapacity(reserved)
	})
}

func resolveSCFTimerFunctionCountsWithCapacityFunc(cfg *SCFFetcherSpace, path string, capacity func(string) int) error {
	if cfg == nil {
		return fmt.Errorf("config_invalid: %s is required", path)
	}
	if capacity == nil {
		return fmt.Errorf("config_invalid: max_functions_per_namespace must be positive")
	}
	if err := normalizeSCFRegionBlacklist(cfg, path); err != nil {
		return err
	}
	if strings.EqualFold(strings.TrimSpace(cfg.SpaceID), "stockcn") && cfg.StaggerStartSecond == 0 && cfg.StaggerWindowSeconds == 0 && cfg.StaggerMaxStartsPerSecond == 0 {
		cfg.StaggerStartSecond = DefaultStockCNStaggerStartSecond
		cfg.StaggerWindowSeconds = DefaultStockCNStaggerWindowSeconds
		cfg.StaggerMaxStartsPerSecond = DefaultStockCNStaggerMaxStartsPerSecond
	}
	regionCapacity := capacity
	explicitTotal := 0
	autoRegions := make([]int, 0)
	enabledRegions := make([]int, 0)
	for index, region := range cfg.Regions {
		if cfg.IsRegionBlacklisted(region.Region) {
			if region.FunctionCount != 0 {
				return fmt.Errorf("config_invalid: %s region %q is blacklisted but has explicit function_count %d", path, region.Region, region.FunctionCount)
			}
			continue
		}
		if !region.Enabled {
			continue
		}
		if regionCapacity(region.Region) < 1 {
			return fmt.Errorf("config_invalid: %s region %q has no Timer capacity after publisher auxiliary functions", path, region.Region)
		}
		enabledRegions = append(enabledRegions, index)
		if region.FunctionCount == 0 {
			cfg.Regions[index].AutoFunctionCount = true
			autoRegions = append(autoRegions, index)
			continue
		}
		cfg.Regions[index].AutoFunctionCount = false
		explicitTotal += region.FunctionCount
	}
	desired := cfg.TimerFunctionCount
	if len(enabledRegions) == 0 {
		return fmt.Errorf("config_invalid: %s has no enabled region outside region_blacklist", path)
	}
	if desired <= 0 {
		if strings.EqualFold(strings.TrimSpace(cfg.SpaceID), "stockcn") {
			return fmt.Errorf("config_invalid: %s.timer_function_count must be an explicit positive value for stockcn", path)
		}
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
		capacity := 0
		for _, index := range autoRegions {
			capacity += regionCapacity(cfg.Regions[index].Region)
		}
		if remaining > capacity {
			return fmt.Errorf("config_invalid: %s timer_function_count %d exceeds the available Timer capacity %d after publisher auxiliary functions", path, desired, capacity+explicitTotal)
		}
		allocateSCFAutoRegionCountsWithCapacities(cfg, autoRegions, remaining, regionCapacity)
	}
	actualTotal := 0
	for _, index := range enabledRegions {
		count := cfg.Regions[index].FunctionCount
		capacity := regionCapacity(cfg.Regions[index].Region)
		if count < 1 || count > capacity {
			return fmt.Errorf("config_invalid: %s.regions[%d] automatic function_count resolved to %d; Timer capacity is 1..%d after publisher auxiliary functions", path, index, count, capacity)
		}
		actualTotal += count
	}
	if actualTotal != desired {
		return fmt.Errorf("config_invalid: %s resolved regional function_count total %d does not equal timer_function_count %d", path, actualTotal, desired)
	}
	cfg.TimerFunctionCount = desired
	return nil
}

// RebalanceSCFTimerFunctionCounts applies the same quota-aware allocator after
// the publisher has discovered the actual Storage region. Only regions whose
// function_count was automatic are changed; explicit regional counts remain
// operator-owned. The preferred region is filled to its regional capacity
// (namespaces × functions per namespace, minus auxiliaries) before any
// remaining automatic functions are assigned elsewhere.
func RebalanceSCFTimerFunctionCounts(cfg *SCFFetcherSpace, storageRegion string, limits TencentSCFLimits) error {
	if cfg == nil {
		return fmt.Errorf("config_invalid: scf space is required")
	}
	limits.normalize()
	capacity := func(region string) int {
		limit := limits.ForRegion(region)
		if strings.EqualFold(strings.TrimSpace(cfg.SpaceID), "crypto") {
			return max(0, limit.MaxFunctionsPerRegion()-1)
		}
		return limit.TimerCapacity(0)
	}
	autoRegions := make([]int, 0)
	explicitTotal := 0
	for index := range cfg.Regions {
		region := &cfg.Regions[index]
		if cfg.IsRegionBlacklisted(region.Region) || !region.Enabled {
			continue
		}
		if capacity(region.Region) < 1 {
			return fmt.Errorf("config_invalid: scf region %s has no Timer capacity after publisher auxiliary functions", region.Region)
		}
		if region.AutoFunctionCount {
			autoRegions = append(autoRegions, index)
			region.FunctionCount = 0
		} else {
			explicitTotal += region.FunctionCount
		}
	}
	if len(autoRegions) == 0 {
		return nil
	}
	desired := cfg.TimerFunctionCount
	if desired <= 0 {
		desired = explicitTotal
		for range autoRegions {
			desired += 1
		}
	}
	remaining := desired - explicitTotal
	if remaining < len(autoRegions) {
		return fmt.Errorf("config_invalid: %s timer_function_count %d cannot assign at least one function to each automatic region", cfg.SpaceID, desired)
	}
	totalCapacity := 0
	for _, index := range autoRegions {
		totalCapacity += capacity(cfg.Regions[index].Region)
	}
	if remaining > totalCapacity {
		return fmt.Errorf("config_invalid: %s timer_function_count %d exceeds automatic regional capacity %d", cfg.SpaceID, desired, totalCapacity+explicitTotal)
	}
	preferred := make([]int, 0, 1)
	other := make([]int, 0, len(autoRegions))
	storageRegion = strings.ToLower(strings.TrimSpace(storageRegion))
	for _, index := range autoRegions {
		if strings.EqualFold(strings.TrimSpace(cfg.Regions[index].Region), storageRegion) {
			preferred = append(preferred, index)
		} else {
			other = append(other, index)
		}
	}
	groups := make([][]int, 0, 2)
	if len(preferred) > 0 {
		groups = append(groups, preferred)
	}
	if len(other) > 0 {
		groups = append(groups, other)
	}
	allocateSCFAutoRegionCountsWithGroups(cfg, groups, remaining, capacity)
	return nil
}

func allocateSCFAutoRegionCountsWithCapacities(cfg *SCFFetcherSpace, autoRegions []int, remaining int, capacity func(string) int) {
	groups := [][]int{autoRegions}
	if strings.EqualFold(strings.TrimSpace(cfg.SpaceID), "crypto") {
		overseas := make([]int, 0, len(autoRegions))
		domestic := make([]int, 0, len(autoRegions))
		for _, index := range autoRegions {
			if isOverseasSCFRegion(cfg.Regions[index].Region) {
				overseas = append(overseas, index)
			} else {
				domestic = append(domestic, index)
			}
		}
		groups = [][]int{overseas, domestic}
	}
	allocateSCFAutoRegionCountsWithGroups(cfg, groups, remaining, capacity)
}

func allocateSCFAutoRegionCountsWithGroups(cfg *SCFFetcherSpace, groups [][]int, remaining int, capacity func(string) int) {
	for groupIndex, group := range groups {
		if len(group) == 0 {
			continue
		}
		laterRegionCount := 0
		for _, later := range groups[groupIndex+1:] {
			laterRegionCount += len(later)
		}
		groupCapacity := 0
		for _, index := range group {
			groupCapacity += capacity(cfg.Regions[index].Region)
		}
		assign := remaining - laterRegionCount
		if assign > groupCapacity {
			assign = groupCapacity
		}
		for _, index := range group {
			cfg.Regions[index].FunctionCount = 1
		}
		remainingExtra := assign - len(group)
		for remainingExtra > 0 {
			progress := false
			for _, index := range group {
				if cfg.Regions[index].FunctionCount >= capacity(cfg.Regions[index].Region) {
					continue
				}
				cfg.Regions[index].FunctionCount++
				remainingExtra--
				progress = true
				if remainingExtra == 0 {
					break
				}
			}
			if !progress {
				break
			}
		}
		remaining -= assign
	}
}

func isOverseasSCFRegion(code string) bool {
	for _, region := range tencent.SCFRegions() {
		if strings.EqualFold(region.Code, strings.TrimSpace(code)) {
			return region.Tag == "海外"
		}
	}
	return false
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
