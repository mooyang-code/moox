package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"

	"github.com/BurntSushi/toml"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/factorname"
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
	DefaultCryptoInvokeTimeoutSeconds     = 60
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
	// ArchiveRoot 是归档 worker 存放分区文件与暂存库的目录（在运行 worker 的主机上）。
	ArchiveRoot string `toml:"archive_root"`
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
	if strings.TrimSpace(p.ArchiveRoot) == "" {
		p.ArchiveRoot = filepath.Join(p.DeployRoot, "archive")
	}
	p.DeployRoot = filepath.Clean(strings.TrimSpace(p.DeployRoot))
	p.ControlRoot = filepath.Clean(strings.TrimSpace(p.ControlRoot))
	p.StorageRoot = filepath.Clean(strings.TrimSpace(p.StorageRoot))
	p.ArchiveRoot = filepath.Clean(strings.TrimSpace(p.ArchiveRoot))
	return p
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

// 与新格式共用的段直接用新包的类型（T11 起渲染函数也在新包里），旧包随旧部署代码在 T13 删除。
type (
	EgressProxy           = topology.EgressProxy
	StorageRetention      = topology.StorageRetention
	ViewMaxAge            = topology.ViewMaxAge
	StorageView           = topology.StorageView
	CollectorRetention    = topology.CollectorRetention
	ArchiveObjectStore    = topology.ArchiveObjectStore
	SCFPlan               = topology.SCFPlan
	SCFPlanRegion         = topology.SCFPlanRegion
	SCFPlanSpace          = topology.SCFPlanSpace
	SCFCloudAccountConfig = topology.SCFCloudAccountConfig
	Admin                 = topology.Admin
	Notification          = topology.Notification
	ExchangeKey           = topology.ExchangeKey
)

// DefaultEgressHTTPDomains 是 moox.toml 没写 [egress_proxy] http_domains 时使用的出口代理 HTTP 白名单。
func DefaultEgressHTTPDomains() []string { return topology.DefaultEgressHTTPDomains() }

// RecommendedSpaceRetention 是各空间推荐的保留期。
func RecommendedSpaceRetention() map[string]map[string]string {
	return topology.RecommendedSpaceRetention()
}

// StoragePolicy 是 storage-policy.json 的内容（storage-primary 与 storage-view 启动时读它）。
func (m Manifest) StoragePolicy() storagepolicy.Policy {
	return topology.StoragePolicyFor(m.StorageRetention, m.StorageView)
}

// Logging 是 [logging]：服务日志的去向。重要日志（Error 及以上、关键事件）总是发给 Monitor，不需要配置。
type Logging struct {
	// RetentionDays 是腾讯云日志服务里日志保留的天数（初始化时建的采集日志主题用它；服务日志的主题手工创建时填同样的天数），默认 2。
	// 只在创建日志主题时生效；已经建好的主题要到腾讯云控制台改。1～3600，3640 表示永久。
	RetentionDays int64      `toml:"retention_days"`
	CLS           LoggingCLS `toml:"cls"`
}

// DefaultLogRetentionDays 是日志服务里日志默认保留的天数。
const DefaultLogRetentionDays = 2

func (l *Logging) validate() error {
	if l.RetentionDays == 0 {
		l.RetentionDays = DefaultLogRetentionDays
	}
	if l.RetentionDays < 1 || (l.RetentionDays > 3600 && l.RetentionDays != 3640) {
		return fmt.Errorf("config_invalid: logging.retention_days must be 1..3600, or 3640 for permanent")
	}
	return l.CLS.validate()
}

// LoggingCLS 是 [logging.cls]：把各服务警告及以上的日志和关键事件上报到腾讯云日志服务。
// 上报走明文 HTTP（SDK 写死），所以日志里不要写密钥和完整的 Webhook 地址。
type LoggingCLS struct {
	Enabled bool `toml:"enabled"`
	// Endpoint 是日志服务的上报域名，例如 ap-guangzhou.cls.tencentcs.com。
	Endpoint string `toml:"endpoint"`
	// TopicID 是日志主题 moox-service 的 ID（moox-cli ops tencent cls bootstrap --topic-name moox-service 创建并输出）。
	TopicID string `toml:"topic_id"`
	// WriterSecretID / WriterSecretKey 是只有这个主题上传权限的子账号密钥，会下发到每台主机；不要填云账号的管理密钥。
	WriterSecretID  string `toml:"writer_secret_id"`
	WriterSecretKey string `toml:"writer_secret_key"`
	// MinLevel 是上报的最低级别：info、warn（默认）、error。关键事件（进程启动、暂停与恢复等）不论级别都上报。
	// 没有 debug：服务的日志输出本身从 info 起。
	MinLevel string `toml:"min_level"`
}

// validate 规整并检查 [logging.cls]。没有启用时不检查其余字段。
func (c *LoggingCLS) validate() error {
	c.Endpoint, c.TopicID = strings.TrimSpace(c.Endpoint), strings.TrimSpace(c.TopicID)
	c.WriterSecretID, c.WriterSecretKey = strings.TrimSpace(c.WriterSecretID), strings.TrimSpace(c.WriterSecretKey)
	c.MinLevel = strings.ToLower(strings.TrimSpace(c.MinLevel))
	if c.MinLevel == "" {
		c.MinLevel = "warn"
	}
	if !slices.Contains([]string{"info", "warn", "error"}, c.MinLevel) {
		return fmt.Errorf("config_invalid: logging.cls.min_level must be info, warn or error")
	}
	if !c.Enabled {
		return nil
	}
	for name, value := range map[string]string{"endpoint": c.Endpoint, "topic_id": c.TopicID, "writer_secret_id": c.WriterSecretID, "writer_secret_key": c.WriterSecretKey} {
		if value == "" {
			return fmt.Errorf("config_invalid: logging.cls.%s is required when logging.cls.enabled is true", name)
		}
	}
	if strings.Contains(c.Endpoint, "/") || strings.ContainsAny(c.Endpoint, " \t") {
		return fmt.Errorf("config_invalid: logging.cls.endpoint 只填域名，例如 ap-guangzhou.cls.tencentcs.com")
	}
	return nil
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
	// Reachability 为空表示可直达，outbound 表示只能出站。
	Reachability string
	// Label 是主机的中文名（例如“海外机”），登记到 Admin 用于界面展示；配置里没写时按主机 ID 生成。
	Label string
	// PrivateKeyFile 与 PrivateKeyPassphrase 来自 ssh 段，只用于同步控制台终端的登录信息。
	PrivateKeyFile       string
	PrivateKeyPassphrase string `json:"-"`
}

// SSHConfig 是登录主机的方式。password 与 private_key_file 都用于 moox-cli 自己连接主机；
// 直达主机配了其中之一（私钥优先），才会同步给控制台用作终端登录，没配的主机在控制台里置灰。
type SSHConfig struct {
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password" json:"-"`
	// PrivateKeyFile 是运行 moox-cli 的机器上的私钥文件路径，支持以 ~ 开头。
	PrivateKeyFile       string `toml:"private_key_file"`
	PrivateKeyPassphrase string `toml:"private_key_passphrase" json:"-"`
}

type HostDefinition struct {
	// Label 是主机的中文名，例如“控制机”“存储机”“海外机”“因子机”；不写时按主机 ID 生成（overseas-2 → 海外机 2）。
	Label          string `toml:"label"`
	Address        string `toml:"address"`
	PrivateAddress string `toml:"private_address"`
	Region         string `toml:"region"`
	Provider       string `toml:"provider"`
	// Reachability：direct（默认）或 outbound（只能出站，例如家庭网络，经 control 网关反向连接）。
	Reachability string    `toml:"reachability"`
	SSH          SSHConfig `toml:"ssh"`
}

// FactorSetup 是旧格式 moox.toml 的 [factors]：新代码不再读它（因子任务改由 config/setup/factor-tasks.yaml 与 factor task apply 导入），只为旧文件还能解析而保留；定义来自 YAML 因子库文件（全局），
// 因子任务内嵌自己的成员（设计 12）。
type FactorSetup struct {
	Enabled     bool                    `toml:"enabled"`
	SourceDir   string                  `toml:"source_dir"`
	Tasks       []FactorSetupTask       `toml:"tasks"`
	Definitions []FactorSetupDefinition `toml:"definitions"`
}

// FactorSetupTask 是一个因子任务：id 是任务 ID（ftask_ 加后缀），name 是展示名（直接用作结果数据集的名称），
// 源数据集 + 频率决定它读哪份数据；一个源数据集可以有多个任务。成员内嵌在任务里。
// 任务的标的就是源数据集当前的标的，没有单独的对象范围。
type FactorSetupTask struct {
	ID              string              `toml:"id"`
	Name            string              `toml:"name"`
	SpaceID         string              `toml:"space_id"`
	SourceDatasetID string              `toml:"source_dataset_id"`
	Freq            string              `toml:"freq"`
	Members         []FactorSetupMember `toml:"members"`
}

// FactorSetupDefinition 引用 YAML 因子库文件里的一个定义：
// 展示名、表达式都来自库文件，类型、输入列、输出列与回看由 FactorMgr 编译得到。定义不含数据集、频率和状态。
type FactorSetupDefinition struct {
	FactorID string `toml:"factor_id"`
	File     string `toml:"file"`
}

// FactorSetupMember 把一个定义加入任务。Params 是固定成员的固定参数（没给的参数取定义的默认值），Var 为真表示可变成员：
// 参数由关联的策略实例逐期提交，Params 是它的默认参数。成员键由参数推导（见 packages/factorname），不在配置里写。
// 同一个定义可以用不同的固定参数多次加入同一个任务。Status 缺省为 enabled。
type FactorSetupMember struct {
	Factor string             `toml:"factor"`
	Params map[string]float64 `toml:"params"`
	Var    bool               `toml:"var"`
	Status string             `toml:"status"`
}

type Manifest struct {
	Admin              Admin                     `toml:"admin"`
	TencentCloud       TencentCloud              `toml:"tencent_cloud"`
	EventBus           EventBus                  `toml:"eventbus"`
	Paths              Paths                     `toml:"paths"`
	EgressProxy        EgressProxy               `toml:"egress_proxy"`
	Deployments        map[string][]string       `toml:"deployments"`
	StorageRetention   StorageRetention          `toml:"storage_retention"`
	StorageView        StorageView               `toml:"storage_view"`
	CollectorRetention CollectorRetention        `toml:"collector_retention"`
	LocalLogs          LocalLogs                 `toml:"local_logs"`
	Observability      Observability             `toml:"observability"`
	Notification       Notification              `toml:"notification"`
	Logging            Logging                   `toml:"logging"`
	ExchangeKeys       []ExchangeKey             `toml:"exchange_keys"`
	Factors            FactorSetup               `toml:"factors"`
	SCFPlan            SCFPlan                   `toml:"scf_plan"`
	HostCatalog        map[string]HostDefinition `toml:"hosts"`
	// ArchiveObjectStore 为 nil 表示归档用因子机上内置的 SeaweedFS；非 nil 时用这里配置的外部 S3。
	ArchiveObjectStore *ArchiveObjectStore `toml:"archive_object_store"`
}

type Snapshot struct {
	Manifest Manifest
	// Encrypted 表示磁盘上的 moox.toml 是加密保存的。
	Encrypted bool
	path      string
	info      os.FileInfo
	digest    [sha256.Size]byte
}

// deploymentFields 列出 DeploymentDigest 覆盖的字段。
func (s *Snapshot) deploymentFields() any {
	type host struct {
		Name, Address, PrivateAddress, Username, Reachability string
		Port                                                  int
	}
	hosts := []host{}
	for _, item := range s.Manifest.Hosts() {
		hosts = append(hosts, host{item.Name, item.Address, item.PrivateAddress, item.Username, item.Reachability, item.Port})
	}
	return struct {
		Hosts         []host
		Deployments   map[string][]string
		Paths         Paths
		EventBusPort  int
		AdminUser     string
		AdminPassword string
	}{hosts, s.Manifest.Deployments, s.Manifest.Paths, s.Manifest.EventBus.Port, s.Manifest.Admin.Username, s.Manifest.Admin.Password}
}

// DeploymentDigest 是决定部署身份的配置摘要：这些字段进了首次安装的软件、证书或账号，装到一半不能改。
// 其他配置只影响后面的步骤，改了可以直接重跑。
func (s *Snapshot) DeploymentDigest() string {
	raw, err := json.Marshal(s.deploymentFields())
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// Path 是配置文件的绝对路径。
func (s *Snapshot) Path() string { return s.path }

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

	if _, err := os.Lstat(topology.SecretsPathFor(resolvedPath)); err == nil {
		// 新格式拆成两份文件，旧包不读：setup 下的旧部署命令只认单份加密的旧 moox.toml。
		return nil, fmt.Errorf("config_invalid: 同目录有 %s，这是新格式的配置；这条 setup 命令还只认旧格式（单份加密的 moox.toml）", topology.SecretsFileName)
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
		first := keys[0].String()
		if len(keys[0]) == 3 && keys[0][0] == "hosts" && keys[0][2] == "tls_mode" {
			return fmt.Errorf("config_invalid: %s 已删除，管理台只用内部证书，删掉它", first)
		}
		if len(keys[0]) >= 2 && keys[0][0] == "egress_proxy" && keys[0][1] == "dns" {
			return fmt.Errorf("config_invalid: [egress_proxy.dns] 已删除，DNS 快照不再使用，删掉它")
		}
		if last := keys[0][len(keys[0])-1]; keys[0][0] == "scf_plan" && (last == "default_ingest" || last == "default_executor") {
			return fmt.Errorf("config_invalid: %s 已删除：采集方式记在采集任务上，默认值在各市场的 market.yaml，用 moox-cli collector ingest-mode set 切换，删掉它", first)
		}
		section := string(keys[0][0])
		switch section {
		case "control_host", "storage_host", "view_host", "strategy_host", "other_hosts":
			return fmt.Errorf("config_invalid: %s is retired; use [hosts.<host-id>] with address and ssh, and [deployments]", section)
		case "hosts":
			return fmt.Errorf("config_invalid: invalid hosts field; use [hosts.<host-id>] with address and ssh = { port, username, password }")
		case "scf_fetcher":
			return fmt.Errorf("config_invalid: [scf_fetcher] is retired; use [scf_plan] (cloud_account, regions, spaces)")
		case "eventbus":
			if first == "eventbus.host" {
				return fmt.Errorf("config_invalid: eventbus.host is derived from the eventbus component in [deployments]")
			}
		case "compile_host":
			return fmt.Errorf("config_invalid: [compile_host] is retired; builds run on the local machine (see docs/构建手册.md); remove [compile_host] and any host that was only used for compiling")
		case "placements":
			return fmt.Errorf("config_invalid: [placements] 已改名为 [deployments]")
		case "factor_engine":
			return fmt.Errorf("config_invalid: [factor_engine] 已取消：因子机是普通主机，请在 [hosts.<主机ID>] 设置 reachability = \"outbound\"，并在 [deployments] 里为它部署 factor-engine 与 archive-worker")
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
		definition.Reachability = strings.ToLower(strings.TrimSpace(definition.Reachability))
		definition.SSH.Username = strings.TrimSpace(definition.SSH.Username)
		definition.SSH.PrivateKeyFile = strings.TrimSpace(definition.SSH.PrivateKeyFile)
		if !md.IsDefined("hosts", id, "ssh", "port") {
			definition.SSH.Port = 22
		}
		out.HostCatalog[id] = definition
	}
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
		out.StorageRetention.Spaces = RecommendedSpaceRetention()
	}
	if !md.IsDefined("storage_view", "max_age", "defaults") {
		out.StorageView.MaxAge.Defaults = recommended.View.MaxAge.Defaults
	}
	if !md.IsDefined("storage_view", "max_age", "spaces") {
		out.StorageView.MaxAge.Spaces = recommended.View.MaxAge.Spaces
	}
	if !md.IsDefined("storage_view", "maintenance_check_interval") {
		out.StorageView.MaintenanceCheckInterval = "1m"
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
		out.EgressProxy.HTTPDomains = DefaultEgressHTTPDomains()
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
	if err := manifest.Admin.Validate(); err != nil {
		return err
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
	if err := manifest.Logging.validate(); err != nil {
		return err
	}
	if err := manifest.Notification.Validate(); err != nil {
		return err
	}
	if err := topology.ValidateExchangeKeys(manifest.ExchangeKeys); err != nil {
		return err
	}
	if err := validateFactorSetup(&manifest.Factors); err != nil {
		return err
	}
	if manifest.ArchiveObjectStore != nil {
		if err := manifest.ArchiveObjectStore.Validate(); err != nil {
			return err
		}
	}
	policy := manifest.StoragePolicy()
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("config_invalid: storage policy: %w", err)
	}
	// 默认保留期装不下 view_rows_per_series 时报错；空间覆盖只提醒。
	if errs, _ := policy.Coverage(); len(errs) > 0 {
		return fmt.Errorf("config_invalid: storage policy: %s", strings.Join(errs, "; "))
	}
	if err := manifest.CollectorRetention.Validate(); err != nil {
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

	if err := manifest.EgressProxy.Validate(); err != nil {
		return err
	}
	if err := validateDeployments(manifest); err != nil {
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
	paths.ArchiveRoot = filepath.Clean(strings.TrimSpace(paths.ArchiveRoot))
	for name, value := range map[string]string{
		"deploy_root": paths.DeployRoot, "control_root": paths.ControlRoot, "storage_root": paths.StorageRoot, "archive_root": paths.ArchiveRoot,
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
	if paths.ArchiveRoot == paths.DeployRoot || pathsOverlap(paths.ArchiveRoot, paths.ControlRoot) || pathsOverlap(paths.ArchiveRoot, paths.StorageRoot) {
		return fmt.Errorf("config_invalid: paths.archive_root must not overlap deploy_root, control_root or storage_root")
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

// ComponentHost returns the selected host for a single component. Multi-copy
// components are addressed through the compiled Directory instead.
func (m Manifest) ComponentHost(component string) string {
	selected := ""
	for host, components := range m.Deployments {
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
func validateDeployments(m *Manifest) error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	for host := range m.Deployments {
		if _, ok := m.HostCatalog[host]; !ok {
			return fmt.Errorf("config_invalid: deployments host ID %q is unknown", host)
		}
		for _, id := range m.Deployments[host] {
			component, ok := catalog.Component(id)
			if !ok {
				return fmt.Errorf("config_invalid: [deployments] 主机 %s 的组件 %q 不在服务目录里", host, id)
			}
			if component.Scope == servicecatalog.ScopeHost {
				return fmt.Errorf("config_invalid: [deployments] 主机 %s 的组件 %q 由系统自动部署到每台主机，不要写在 [deployments] 里", host, id)
			}
		}
	}
	topology, err := m.Topology()
	if err != nil {
		return err
	}
	if err := catalog.ValidateTopology(topology); err != nil {
		return fmt.Errorf("config_invalid: deployments: %w", err)
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
	taskIDs := make(map[string]struct{}, len(cfg.Tasks))
	taskNames := make(map[string]struct{}, len(cfg.Tasks))
	for index := range cfg.Tasks {
		task := &cfg.Tasks[index]
		path := fmt.Sprintf("factors.tasks[%d]", index)
		task.ID, task.Name = strings.TrimSpace(task.ID), strings.TrimSpace(task.Name)
		task.SpaceID, task.SourceDatasetID, task.Freq = strings.TrimSpace(task.SpaceID), strings.TrimSpace(task.SourceDatasetID), strings.TrimSpace(task.Freq)
		if task.ID == "" || task.Name == "" || task.SpaceID == "" || task.SourceDatasetID == "" || task.Freq == "" {
			return fmt.Errorf("config_invalid: %s requires id, name, space_id, source_dataset_id and freq", path)
		}
		if _, err := factorname.ParseTaskID(task.ID); err != nil {
			return fmt.Errorf("config_invalid: %s.id is invalid: %v", path, err)
		}
		if err := validateFactorTaskName(task.Name); err != nil {
			return fmt.Errorf("config_invalid: %s.name is invalid: %v", path, err)
		}
		if _, ok := taskIDs[task.ID]; ok {
			return fmt.Errorf("config_invalid: factor task id %q is duplicated", task.ID)
		}
		taskIDs[task.ID] = struct{}{}
		// 任务名直接用作结果数据集名称，空间内唯一。
		nameKey := task.SpaceID + "\x00" + task.Name
		if _, ok := taskNames[nameKey]; ok {
			return fmt.Errorf("config_invalid: factor task name %q is duplicated in space %q", task.Name, task.SpaceID)
		}
		taskNames[nameKey] = struct{}{}
		if err := validateFactorSetupMembers(path, task.Members, seen); err != nil {
			return err
		}
	}
	return nil
}

// validateFactorSetupMembers 检查任务内嵌的成员：引用的定义已声明、参数有限、状态合法；同一定义的可变成员最多一个，
// 固定成员的参数相同（完全相同的写法）算重复。成员键由服务端按定义的默认参数推导，写法不同但推导出同一个键的情况由 setup 时报告。
func validateFactorSetupMembers(path string, members []FactorSetupMember, definitions map[string]struct{}) error {
	keys := make(map[string]struct{}, len(members))
	for index := range members {
		member := &members[index]
		memberPath := fmt.Sprintf("%s.members[%d]", path, index)
		member.Factor, member.Status = strings.TrimSpace(member.Factor), strings.TrimSpace(member.Status)
		if member.Factor == "" {
			return fmt.Errorf("config_invalid: %s requires factor", memberPath)
		}
		if _, ok := definitions[member.Factor]; !ok {
			return fmt.Errorf("config_invalid: %s references unknown factor %q; declare it in [[factors.definitions]]", memberPath, member.Factor)
		}
		for name, value := range member.Params {
			if strings.TrimSpace(name) == "" || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("config_invalid: %s.params has an empty name or a non-finite value", memberPath)
			}
		}
		kind := "fixed"
		if member.Var {
			kind = "var"
		}
		key := member.Factor + "\x00" + kind
		if !member.Var {
			key += "\x00" + canonicalParams(member.Params)
		}
		if _, ok := keys[key]; ok {
			return fmt.Errorf("config_invalid: %s repeats factor %q (%s) with the same params", memberPath, member.Factor, kind)
		}
		keys[key] = struct{}{}
		if member.Status == "" {
			member.Status = "enabled"
		}
		if member.Status != "enabled" && member.Status != "disabled" {
			return fmt.Errorf("config_invalid: %s.status must be enabled or disabled", memberPath)
		}
	}
	return nil
}

// validateFactorTaskName 与 FactorMgr 的任务名规则一致：1～10 个字、含汉字、首尾无空白、无控制字符。
func validateFactorTaskName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 10 {
		return fmt.Errorf("must be 1-10 characters without surrounding whitespace")
	}
	hasHan := false
	for _, r := range name {
		if unicode.IsControl(r) {
			return fmt.Errorf("must not contain control characters")
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if !hasHan {
		return fmt.Errorf("must contain Chinese characters")
	}
	return nil
}

// supportedSCFRegion keeps an operator typo from creating a partial fleet.
// It intentionally covers the standard Tencent Cloud SCF regions used by MooX.
func supportedSCFRegion(region string) bool {
	return tencent.IsSCFRegion(region)
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

// canonicalParams 把固定参数写成键有序的文本，用来判断两个成员的参数是否相同。
func canonicalParams(params map[string]float64) string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name + "=" + strconv.FormatFloat(params[name], 'f', -1, 64) + ";")
	}
	return b.String()
}
