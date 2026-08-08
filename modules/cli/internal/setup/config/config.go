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

	"github.com/BurntSushi/toml"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
)

const (
	manifestName = "custom.toml"
	maxFileSize  = 1 << 20
	// SCFCLSReserveMilliseconds is injected into every short-lived market SCF.
	// Keep setup validation aligned with the runtime's CLS flush reservation.
	SCFCLSReserveMilliseconds = 3000
	// SCFCompletionReserveMilliseconds leaves enough time for the durable
	// completion event after Storage has accepted the aggregate write.
	SCFCompletionReserveMilliseconds = 3000
	// SCFFinalResponseReserveMilliseconds keeps the SCF runtime enough time to
	// serialize and return its response after best-effort CLS logging.
	SCFFinalResponseReserveMilliseconds = 500
)

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

type Monitoring struct {
	WeComWebhook string `toml:"wecom_webhook"`
}

type Host struct {
	Name     string `toml:"name"`
	Address  string `toml:"address"`
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
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

// SCFFetcherSpace describes one separately packaged and deployed source
// collector fleet. PackageConfigDir is relative to modules/collector/configs.
type SCFFetcherSpace struct {
	SpaceID          string `toml:"space_id"`
	Entrypoint       string `toml:"entrypoint"`
	PackageConfigDir string `toml:"package_config_dir"`
	PackageName      string `toml:"package_name"`
	// CLSCloudAccountID owns the single regional CLS topic used by every
	// short-lived collector function in this space, regardless of its SCF region.
	CLSCloudAccountID       string             `toml:"cls_cloud_account_id"`
	Namespace               string             `toml:"namespace"`
	Runtime                 string             `toml:"runtime"`
	FunctionPrefix          string             `toml:"function_prefix"`
	StorageGatewayNodeID    string             `toml:"storage_gateway_node_id"`
	StorageRPCGatewayTarget string             `toml:"storage_rpc_gateway_target"`
	MemorySize              int                `toml:"memory_size"`
	TimeoutSeconds          int                `toml:"timeout_seconds"`
	RealtimeBatchSize       int                `toml:"realtime_batch_size"`
	RealtimeBarLimit        int                `toml:"realtime_bar_limit"`
	CatchupBatchSize        int                `toml:"catchup_batch_size"`
	CatchupBarLimit         int                `toml:"catchup_bar_limit"`
	MaxInflightRequests     int                `toml:"max_inflight_requests"`
	RequestTimeoutMS        int                `toml:"request_timeout_ms"`
	HTTPMaxAttempts         int                `toml:"http_max_attempts"`
	StorageMaxAttempts      int                `toml:"storage_max_attempts"`
	StorageTimeoutMS        int                `toml:"storage_timeout_ms"`
	MaxRetryAttempts        int                `toml:"max_retry_attempts"`
	RetryDelays             []string           `toml:"retry_delays"`
	StaggerEnabled          bool               `toml:"stagger_enabled"`
	Regions                 []SCFFetcherRegion `toml:"regions"`
}

type Manifest struct {
	Admin        Admin        `toml:"admin"`
	TencentCloud TencentCloud `toml:"tencent_cloud"`
	EventBus     EventBus     `toml:"eventbus"`
	Monitoring   Monitoring   `toml:"monitoring"`
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
	manifest.Monitoring.WeComWebhook = strings.TrimSpace(manifest.Monitoring.WeComWebhook)
	if manifest.Monitoring.WeComWebhook != "" && !validHTTPSWebhook(manifest.Monitoring.WeComWebhook) {
		return fmt.Errorf("config_invalid: monitoring.wecom_webhook must be a valid HTTPS URL")
	}
	if err := validateSCFFetcher(&manifest.SCFFetcher); err != nil {
		return err
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
		if region == "" || cfg.Regions[i].FunctionCount <= 0 || cfg.Regions[i].FunctionCount > 50 || (cfg.Regions[i].Enabled && strings.TrimSpace(cfg.Regions[i].CloudAccountID) == "") {
			return fmt.Errorf("config_invalid: %s.regions[%d] region, cloud_account_id, and function_count 1..50 are required for enabled regions", path, i)
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
