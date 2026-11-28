// Package topology 读新格式的部署配置：明文的 moox.toml（拓扑与部署参数，可以放进自己的私有仓库）
// 和同目录加密保存的 moox.secrets.toml（秘密的初始值）。
//
// 只读拓扑的命令（collector、factor、data、host、doctor 等）只调 Load，不碰钥匙串；
// 真正要用秘密的命令再调 LoadSecrets。业务初值（因子任务、告警渠道等）不在这两份文件里。
package topology

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

const (
	// FileName 是拓扑文件的默认文件名；Load 接受任意路径。
	FileName = "moox.toml"
	// SecretsFileName 是秘密文件的文件名，总在 moox.toml 的同一目录。
	SecretsFileName = "moox.secrets.toml"
	// BackupFileName 是密钥与告警渠道加密备份的文件名，总在 moox.toml 的同一目录。
	BackupFileName = "moox.backup"
	maxFileSize     = 1 << 20
	// DefaultDeployRoot 是每台主机的部署根：放在挂载到 /data 的云盘上，不占系统盘。
	DefaultDeployRoot = "/data/moox"
	// DefaultLogRetentionDays 是日志服务里日志默认保留的天数。
	DefaultLogRetentionDays = 2
	defaultEventBusPort     = 4222
)

// Paths 是 [paths]：每台主机只有一个部署根。
type Paths struct {
	DeployRoot string `toml:"deploy_root"`
}

// EventBus 是 [eventbus]：EventBus 部署在哪台主机看 [deployments]，这里只有端口。EventBus 总是走 TLS。
type EventBus struct {
	Port int `toml:"port"`
}

// Config 是 moox.toml 的内容。字段都是拓扑与部署时渲染的参数，没有秘密。
type Config struct {
	Paths       Paths                     `toml:"paths"`
	EventBus    EventBus                  `toml:"eventbus"`
	HostCatalog map[string]HostDefinition `toml:"hosts"`
	Deployments map[string][]string       `toml:"deployments"`
	// DeploymentsDisabled 是与 [deployments] 同形的停用清单：列出的组件仍登记、仍装文件，但 Admin 里记为停用、systemd 单元停掉。
	// 每台主机的列表必须是它在 [deployments] 里那份的子集。
	DeploymentsDisabled map[string][]string `toml:"deployments_disabled"`
	StorageRetention    StorageRetention    `toml:"storage_retention"`
	StorageView         StorageView         `toml:"storage_view"`
	CollectorRetention  CollectorRetention  `toml:"collector_retention"`
	EgressProxy         EgressProxy         `toml:"egress_proxy"`
	Logging             Logging             `toml:"logging"`
	SCFPlan             SCFPlan             `toml:"scf_plan"`
	// ArchiveObjectStore 为 nil 表示归档仅保存在执行器本机磁盘；非 nil 时使用这里配置的外部 S3 副本。
	ArchiveObjectStore *ArchiveObjectStore `toml:"archive_object_store"`
	// Overrides 是手工调过的组件配置：组件 → 配置文件名 → 键路径 → 值，部署渲染之后逐项覆盖。
	Overrides Overrides `toml:"overrides"`

	path string
}

// Path 是 moox.toml 的绝对路径；Parse 得到的配置为空。
func (c *Config) Path() string { return c.path }

// SecretsPath 是同目录 moox.secrets.toml 的路径。
func (c *Config) SecretsPath() string { return SecretsPathFor(c.path) }

// SecretsPathFor 返回某个 moox.toml 同目录的秘密文件路径。
func SecretsPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), SecretsFileName)
}

// BackupPathFor 返回某个 moox.toml 同目录的备份文件路径。
func BackupPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), BackupFileName)
}

// Load 读取任意路径的 moox.toml 并校验。只读这一份明文文件，不读秘密文件。
func Load(path string) (*Config, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("config_invalid: 需要 moox.toml 的路径")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: 解析 %s 的路径失败", path)
	}
	raw, err := readRegularFile(absolute, false)
	if err != nil {
		return nil, err
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	cfg.path = absolute
	return &cfg, nil
}

// Parse 按与 Load 相同的严格解码和校验解析 moox.toml 的内容，不读文件。生成配置的工具用它保证写出的文件能被 CLI 接受。
func Parse(raw []byte) (Config, error) {
	if len(raw) > maxFileSize {
		return Config{}, errors.New("config_invalid: moox.toml 超过 1 MiB")
	}
	if configcrypt.IsEncrypted(raw) {
		return Config{}, errors.New("config_invalid: 这是旧格式的 moox.toml（整份加密）：新格式拆成明文的 moox.toml 和加密的 moox.secrets.toml，请用 moox-cli config init 重新生成")
	}
	var cfg Config
	md, err := toml.NewDecoder(bytes.NewReader(raw)).Decode(&cfg)
	if err != nil {
		return Config{}, decodeError("moox.toml", err)
	}
	if keys := md.Undecoded(); len(keys) != 0 {
		return Config{}, undecodedError(keys)
	}
	cfg.applyDefaults(md)
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// decodeError 只给出行号和键名，不带解析器的原文：原文可能含有那一行的内容（秘密文件里就是密钥）。
func decodeError(name string, err error) error {
	var parse toml.ParseError
	if errors.As(err, &parse) {
		where := fmt.Sprintf("第 %d 行", parse.Position.Line)
		if parse.LastKey != "" {
			where += "（" + parse.LastKey + " 附近）"
		}
		return fmt.Errorf("config_invalid: %s 不是合法的 TOML：%s", name, where)
	}
	return fmt.Errorf("config_invalid: %s 的值类型不对或不是合法的 TOML", name)
}

// undecodedError 先找已删除或放错文件的段，指明是哪一段、该放到哪里；都不是时报第一个不认识的键。
func undecodedError(keys []toml.Key) error {
	// [tencent_cloud] 下只有 region 时是旧写法里的地域，不是放错文件的密钥。
	onlyRegion := false
	for _, key := range keys {
		if len(key) >= 2 && key[0] == "tencent_cloud" {
			if key[1] != "region" {
				onlyRegion = false
				break
			}
			onlyRegion = true
		}
	}
	if onlyRegion {
		return errors.New("config_invalid: " + removedInConfig(toml.Key{"tencent_cloud", "region"}))
	}
	for _, key := range keys {
		if message := removedInConfig(key); message != "" {
			return errors.New("config_invalid: " + message)
		}
	}
	return fmt.Errorf("config_invalid: moox.toml 里有不认识的键 %s", keys[0].String())
}

// secretSections 是只能写在 moox.secrets.toml 里的段。
var secretSections = map[string]string{
	"admin": "[admin]", "tencent_cloud": "[tencent_cloud]", "notification": "[notification]", "exchange_keys": "[[exchange_keys]]",
}

func removedInConfig(key toml.Key) string {
	if len(key) == 0 {
		return ""
	}
	at := func(i int) string {
		if i < len(key) {
			return key[i]
		}
		return ""
	}
	switch key[0] {
	case "factors":
		section := "[factors]"
		switch at(1) {
		case "tasks":
			section = "[[factors.tasks]]"
		case "definitions":
			section = "[[factors.definitions]]"
		}
		return "moox.toml 里出现了 " + section + "：因子段（[factors]、[[factors.tasks]]、[[factors.definitions]]）已移出配置文件。" +
			"默认因子任务在仓库的 config/setup/factor-tasks.yaml，由 setup start 导入；自己的任务剪到单独的 yaml 文件（键名不变），用 moox-cli factor task apply --file 导入"
	case "admin", "tencent_cloud", "notification", "exchange_keys":
		if key[0] == "tencent_cloud" && at(1) == "region" {
			return "tencent_cloud.region 已删除：腾讯云的默认地域取控制机 [hosts.<控制机>] 的 region"
		}
		return fmt.Sprintf("moox.toml 里出现了 %s：它是秘密，要写在同目录的 %s 里（moox.toml 只放拓扑与部署参数）", secretSections[key[0]], SecretsFileName)
	case "local_logs":
		return "[local_logs] 已删除：日志轮转的大小与份数写在各组件的配置模板里"
	case "observability":
		return "[observability] 已删除"
	case "eventbus":
		if at(1) == "tls_enabled" {
			return "eventbus.tls_enabled 已删除：EventBus 总是走 TLS"
		}
	case "paths":
		switch at(1) {
		case "control_root", "storage_root", "archive_root":
			return "paths." + at(1) + " 已删除：每台主机只有一个部署根 paths.deploy_root"
		}
	case "logging":
		if at(1) == "cls" && (at(2) == "writer_secret_id" || at(2) == "writer_secret_key") {
			return "logging.cls." + at(2) + " 是秘密，要写在同目录的 " + SecretsFileName + " 的 [logging.cls] 里"
		}
	case "scf_plan":
		if at(1) == "assumed_egress_ips" {
			return "scf_plan.assumed_egress_ips 已删除（限速预算估算已删除），请从 moox.toml 删掉这一行"
		}
	case "hosts":
		if at(2) == "ssh" && (at(3) == "password" || at(3) == "private_key_passphrase") {
			return fmt.Sprintf("hosts.%s.ssh.%s 已删除：只支持免密的密钥登录，私钥不带口令（带口令的私钥交给 ssh-agent）", at(1), at(3))
		}
	}
	return ""
}

func (c *Config) applyDefaults(md toml.MetaData) {
	if !md.IsDefined("paths", "deploy_root") {
		c.Paths.DeployRoot = DefaultDeployRoot
	}
	if !md.IsDefined("eventbus", "port") {
		c.EventBus.Port = defaultEventBusPort
	}
	for id, definition := range c.HostCatalog {
		definition.normalize()
		if !md.IsDefined("hosts", id, "ssh", "port") {
			definition.SSH.Port = 22
		}
		c.HostCatalog[id] = definition
	}
	recommended := storagepolicy.Default()
	if !md.IsDefined("storage_retention", "view_rows_per_series") {
		c.StorageRetention.ViewBars = recommended.View.Bars
	}
	if !md.IsDefined("storage_retention", "defaults") {
		c.StorageRetention.Defaults = recommended.Retention.Defaults
	}
	if !md.IsDefined("storage_retention", "spaces") {
		c.StorageRetention.Spaces = RecommendedSpaceRetention()
	}
	if !md.IsDefined("storage_view", "max_age", "defaults") {
		c.StorageView.MaxAge.Defaults = recommended.View.MaxAge.Defaults
	}
	if !md.IsDefined("storage_view", "max_age", "spaces") {
		c.StorageView.MaxAge.Spaces = recommended.View.MaxAge.Spaces
	}
	if !md.IsDefined("storage_view", "maintenance_check_interval") {
		c.StorageView.MaintenanceCheckInterval = "1m"
	}
	if !md.IsDefined("storage_view", "max_view_file_bytes") {
		c.StorageView.MaxViewFileBytes = storagepolicy.DefaultMaxViewFileBytes
	}
	c.CollectorRetention.applyDefaults(md)
	if !md.IsDefined("egress_proxy", "http_domains") {
		c.EgressProxy.HTTPDomains = DefaultEgressHTTPDomains()
	}
}

func (c *Config) validate() error {
	if err := validateDeployRoot(&c.Paths); err != nil {
		return err
	}
	if c.EventBus.Port < 1 || c.EventBus.Port > 65535 {
		return errors.New("config_invalid: eventbus.port must be between 1 and 65535")
	}
	if err := validateHostCatalog(c.HostCatalog); err != nil {
		return err
	}
	if err := c.validateDeployments(); err != nil {
		return err
	}
	if c.EventBusHost().Name == "" {
		return errors.New("config_invalid: deployments must include eventbus")
	}
	if err := c.Logging.validate(); err != nil {
		return err
	}
	if c.ArchiveObjectStore != nil {
		if err := c.ArchiveObjectStore.Validate(); err != nil {
			return err
		}
	}
	policy := c.StoragePolicy()
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("config_invalid: storage policy: %w", err)
	}
	// 默认保留期装不下 view_rows_per_series 时报错；空间覆盖只提醒。
	if errs, _ := policy.Coverage(); len(errs) > 0 {
		return fmt.Errorf("config_invalid: storage policy: %s", strings.Join(errs, "; "))
	}
	if err := c.CollectorRetention.Validate(); err != nil {
		return err
	}
	if err := c.EgressProxy.Validate(); err != nil {
		return err
	}
	if err := c.Overrides.validate(); err != nil {
		return err
	}
	return c.CheckSCFPlan()
}

func validateDeployRoot(paths *Paths) error {
	paths.DeployRoot = filepath.Clean(strings.TrimSpace(paths.DeployRoot))
	value := paths.DeployRoot
	if value == "" || !filepath.IsAbs(value) || value == "/" || strings.ContainsAny(value, "\x00\r\n") {
		return errors.New("config_invalid: paths.deploy_root must be a non-root absolute path")
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("/._-", r) {
			continue
		}
		return errors.New("config_invalid: paths.deploy_root contains an unsupported character")
	}
	return nil
}

// ReadFile 用读配置时的同一套校验读一个文件：只读普通文件（不跟随符号链接）、不超过 1 MiB；
// private 为 true（秘密文件）时还要求 0600、属于当前用户，读取时没有被替换。config get/set/edit/backup/encrypt 都用它。
func ReadFile(path string, private bool) ([]byte, error) { return readRegularFile(path, private) }

// readRegularFile 只读普通文件（不跟随符号链接），大小不超过 1 MiB。private 为 true 时（秘密文件）还要求 0600 且属于当前用户。
func readRegularFile(path string, private bool) ([]byte, error) {
	name := filepath.Base(path)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: 读不到 %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("config_insecure: %s 必须是普通文件", name)
	}
	if private {
		if !ownedByCurrentUser(info) {
			return nil, fmt.Errorf("config_insecure: %s 必须属于当前用户", name)
		}
		if permissionsChecked && info.Mode().Perm() != 0o600 {
			return nil, fmt.Errorf("config_insecure: %s 的权限必须是 0600", name)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config_invalid: 读不到 %s", path)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("config_insecure: %s 在读取时被替换", name)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("config_invalid: 读不到 %s", path)
	}
	if len(raw) > maxFileSize {
		return nil, fmt.Errorf("config_invalid: %s 超过 1 MiB", name)
	}
	return raw, nil
}
