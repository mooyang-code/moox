package bootstrap

import (
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/security"
)

// SCFPlanConfig 是定时触发采集的函数部署参数。网络（VPC、子网）按地域放在 scf_regions，
// 每个空间与市场使用的地域放在 scf_markets，函数访问 Access 的私网入口沿用 scf_access_routes。
type SCFPlanConfig struct {
	CloudAccountID string `yaml:"cloud_account_id"`
	// PackageName 非空时每轮解析该名称的最新可用代码包（上传新版本后函数自动升级）；PackageID 固定某个代码包，
	// 两者二选一，推荐 PackageName。
	PackageName string `yaml:"package_name"`
	PackageID   string `yaml:"package_id"`
	Namespace   string `yaml:"namespace"`
	MemoryMB    int    `yaml:"memory_mb"`
	// BaseEnvironmentFile 是可选的额外非受管环境（JSON 对象），含密钥时权限应为 0600。空间身份与 Storage 应用密钥
	// 由 Collector 自己生成（密钥由控制机身份里的 Storage 主密钥派生），日志服务由 CloudNode 创建函数时补齐，
	// 所以通常不需要这个文件。
	BaseEnvironmentFile string `yaml:"base_environment_file"`
	SymbolsPerFunction  int    `yaml:"symbols_per_function"`
	// MaxConcurrentOperations 限制各空间同时进行的 SCF 配置操作。
	MaxConcurrentOperations int `yaml:"max_concurrent_operations"`
	// AssumedEgressIPs 是函数出口 IP 个数的估计（用 moox-cli collector function probe-egress 实测），只用于频控估算与告警。
	AssumedEgressIPs int `yaml:"assumed_egress_ips"`
	// AccessRoutes 是采集计划函数访问 Access 的私网入口（按地域）。与旧链路的 scf_access_routes 分开：
	// 旧 Invoke 函数不在 VPC 内，仍走公网入口，直到 6b 清理。
	AccessRoutes map[string]SCFAccessEndpoint `yaml:"access_routes"`
	Fetch        SCFPlanFetchConfig           `yaml:"fetch"`
	// Spaces 是按空间的覆盖项：每函数标的数与拉取参数（stockcn 与 crypto 的限流、错峰不同）。
	Spaces map[string]SCFPlanSpaceConfig `yaml:"spaces"`
}

// SCFPlanFetchConfig 是下发给函数的拉取参数（按数据源）。
type SCFPlanFetchConfig struct {
	Concurrency      int `yaml:"concurrency"`
	RPS              int `yaml:"rps"`
	Burst            int `yaml:"burst"`
	FeedConcurrency  int `yaml:"feed_concurrency"`
	RequestTimeoutMS int `yaml:"request_timeout_ms"`
	StorageTimeoutMS int `yaml:"storage_timeout_ms"`
	StaggerBaseMS    int `yaml:"stagger_base_ms"`
	StaggerMS        int `yaml:"stagger_ms"`
	// StandbyMS 大于 0 时启用备援请求（行情请求超过这么多毫秒没有结果就并发再发一个备援请求，先成功的为准），默认关闭；
	// StandbyHourOnly 为真时只在整点那一分钟启用。
	StandbyMS       int  `yaml:"standby_ms"`
	StandbyHourOnly bool `yaml:"standby_hour_only"`
	// ConnReuse 为真时函数进程在多次调用之间复用到行情源的 HTTP 连接，默认关闭。
	ConnReuse bool `yaml:"conn_reuse"`
}

// filled 用 base 补齐未指定（为零）的字段。拉取参数里零值表示“沿用默认”，所以只写部分字段的覆盖项不会得到不完整的参数。
func (f SCFPlanFetchConfig) filled(base SCFPlanFetchConfig) SCFPlanFetchConfig {
	pick := func(value, fallback int) int {
		if value != 0 {
			return value
		}
		return fallback
	}
	return SCFPlanFetchConfig{
		Concurrency: pick(f.Concurrency, base.Concurrency), RPS: pick(f.RPS, base.RPS), Burst: pick(f.Burst, base.Burst),
		FeedConcurrency: pick(f.FeedConcurrency, base.FeedConcurrency), RequestTimeoutMS: pick(f.RequestTimeoutMS, base.RequestTimeoutMS),
		StorageTimeoutMS: pick(f.StorageTimeoutMS, base.StorageTimeoutMS), StaggerBaseMS: pick(f.StaggerBaseMS, base.StaggerBaseMS), StaggerMS: pick(f.StaggerMS, base.StaggerMS),
		StandbyMS: pick(f.StandbyMS, base.StandbyMS), StandbyHourOnly: f.StandbyHourOnly || base.StandbyHourOnly,
		ConnReuse: f.ConnReuse || base.ConnReuse,
	}
}

// SCFPlanSpaceConfig 覆盖某个空间的每函数标的数与拉取参数；字段为零表示沿用全局值。
type SCFPlanSpaceConfig struct {
	SymbolsPerFunction int                 `yaml:"symbols_per_function"`
	Fetch              *SCFPlanFetchConfig `yaml:"fetch"`
	// PackageName、PackageID、Namespace、BaseEnvironmentFile 为空表示沿用全局值。代码包按空间构建（带各自的市场配置），
	// 同时管理多个空间时每个空间必须有自己的代码包。
	PackageName         string `yaml:"package_name"`
	PackageID           string `yaml:"package_id"`
	Namespace           string `yaml:"namespace"`
	BaseEnvironmentFile string `yaml:"base_environment_file"`
}

// maxFunctionsPerNamespace 是腾讯云每个命名空间的函数数上限，超过后分到下一个命名空间。
const maxFunctionsPerNamespace = 50

func (c SCFPlanConfig) symbolsPerFunction(spaceID string) int {
	if override := c.Spaces[strings.ToLower(spaceID)]; override.SymbolsPerFunction > 0 {
		return override.SymbolsPerFunction
	}
	return c.SymbolsPerFunction
}

func (c SCFPlanConfig) fetchFor(spaceID string) marketfetch.PlanFetch {
	fetch := c.Fetch
	if override := c.Spaces[strings.ToLower(spaceID)].Fetch; override != nil {
		fetch = override.filled(c.Fetch)
	}
	return marketfetch.PlanFetch{
		Concurrency: fetch.Concurrency, RPS: fetch.RPS, Burst: fetch.Burst, FeedConcurrency: fetch.FeedConcurrency,
		RequestTimeoutMS: fetch.RequestTimeoutMS, StorageTimeoutMS: fetch.StorageTimeoutMS, StaggerBaseMS: fetch.StaggerBaseMS, StaggerMS: fetch.StaggerMS,
		StandbyMS: fetch.StandbyMS, StandbyHourOnly: fetch.StandbyHourOnly,
		ConnReuse: fetch.ConnReuse,
	}
}

// mergeSpaceDefaults 用内置的空间默认值补齐配置里未指定的字段（每函数标的数、拉取参数）。
func (c *SCFPlanConfig) mergeSpaceDefaults(defaults map[string]SCFPlanSpaceConfig) {
	for space, base := range defaults {
		override, ok := c.Spaces[space]
		if !ok {
			continue
		}
		if override.SymbolsPerFunction == 0 {
			override.SymbolsPerFunction = base.SymbolsPerFunction
		}
		switch {
		case override.Fetch == nil && base.Fetch != nil:
			fetch := *base.Fetch
			override.Fetch = &fetch
		case override.Fetch != nil && base.Fetch != nil:
			fetch := override.Fetch.filled(*base.Fetch)
			override.Fetch = &fetch
		}
		c.Spaces[space] = override
	}
}

func defaultSCFPlan() SCFPlanConfig {
	return SCFPlanConfig{
		Namespace: "default", MemoryMB: 128, SymbolsPerFunction: marketfetch.DefaultSymbolsPerFunction, MaxConcurrentOperations: 20,
		// 写入阶段预算 4 秒：错峰 1.5 秒 + 行情请求 3 秒 + 写入 4 秒仍在 1m 数据集 10 秒的迟到窗口内；2 秒太紧，
		// 跨境建连稍慢或整分钟突发时存储端稍慢就超时，也没有时间重试。
		Fetch: SCFPlanFetchConfig{Concurrency: 50, RPS: 50, Burst: 50, FeedConcurrency: 50, RequestTimeoutMS: 3000, StorageTimeoutMS: 4000, StaggerMS: 1500},
		// stockcn：每函数 40 个标的；错峰窗口第 5～40 秒；国内数据源限流较保守。
		Spaces: map[string]SCFPlanSpaceConfig{"stockcn": {SymbolsPerFunction: 40, Fetch: &SCFPlanFetchConfig{Concurrency: 8, RPS: 20, Burst: 20, FeedConcurrency: 8, RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, StaggerBaseMS: 5000, StaggerMS: 35000}}},
	}
}

// defaultLatenessSeconds 是空间的允许迟到时间默认值。
func defaultLatenessSeconds(spaceID string) int { return domain.DefaultAllowedLatenessSeconds(spaceID) }

// fetchPlanSpaces 返回配置了 SCF 市场地域、由采集计划对账管理的空间（排序）。空间集合以 scf_markets 为准，
// 不再与进程的旧调度空间环境变量取交集：Timer 采集可以管理旧调度之外的空间（例如只配置 stockcn）。
func (c *Config) fetchPlanSpaces() []string {
	spaces := make([]string, 0, len(c.SCFMarkets))
	for spaceID, markets := range c.SCFMarkets {
		if len(markets) > 0 {
			spaces = append(spaces, strings.ToLower(spaceID))
		}
	}
	sort.Strings(spaces)
	return spaces
}

// withStoredSpaces 把库里有过采集计划、但已不在配置里的空间并入：配置被移除后，这些空间的定时器必须先停用，
// 不能留下无人管理却仍在采集的函数。
func withStoredSpaces(configured, stored []string) []string {
	set := make(map[string]struct{}, len(configured)+len(stored))
	for _, space := range configured {
		set[strings.ToLower(space)] = struct{}{}
	}
	for _, space := range stored {
		if space = strings.ToLower(strings.TrimSpace(space)); space != "" {
			set[space] = struct{}{}
		}
	}
	all := make([]string, 0, len(set))
	for space := range set {
		all = append(all, space)
	}
	sort.Strings(all)
	return all
}

// validateSCFPlan 检查采集计划配置：每个地域必须有 VPC、子网与同地域 Access 私网入口。
func (c *Config) validateSCFPlan() error {
	if len(c.SCFMarkets) == 0 {
		return nil
	}
	plan := c.SCFPlan
	if plan.CloudAccountID == "" {
		return fmt.Errorf("scf_plan requires cloud_account_id when scf_markets is configured")
	}
	if plan.SymbolsPerFunction < 1 || plan.SymbolsPerFunction > 200 || plan.MemoryMB < 64 || plan.MaxConcurrentOperations < 1 {
		return fmt.Errorf("scf_plan has an out-of-range symbols_per_function, memory_mb or max_concurrent_operations")
	}
	packages := map[string]string{}
	for space := range c.SCFMarkets {
		key, err := plan.packageKeyFor(space)
		if err != nil {
			return err
		}
		if other, dup := packages[key]; dup && len(c.SCFMarkets) > 1 {
			return fmt.Errorf("scf_plan: spaces %s and %s share code package %s; every space needs its own package_name or package_id in scf_plan.spaces", other, space, key)
		}
		packages[key] = space
	}
	for space, markets := range c.SCFMarkets {
		if space != strings.ToLower(space) || len(markets) == 0 {
			return fmt.Errorf("scf_markets[%s] requires a lowercase space id and at least one market", space)
		}
		for market, regions := range markets {
			if market != strings.ToLower(market) || len(regions) == 0 {
				return fmt.Errorf("scf_markets[%s][%s] requires a lowercase market and at least one region", space, market)
			}
			for _, region := range regions {
				network, ok := c.SCFRegions[region]
				if !ok || network.VPCID == "" || network.SubnetID == "" {
					return fmt.Errorf("scf_markets[%s][%s] region %s has no VPC and subnet in scf_regions", space, market, region)
				}
				if _, ok := plan.AccessRoutes[region]; !ok {
					return fmt.Errorf("scf_markets[%s][%s] region %s has no same-region Access route in scf_plan.access_routes", space, market, region)
				}
			}
		}
	}
	return nil
}

func (c SCFPlanConfig) packageIDFor(spaceID string) string {
	if override := c.Spaces[strings.ToLower(spaceID)]; override.PackageID != "" || override.PackageName != "" {
		return override.PackageID
	}
	return c.PackageID
}

func (c SCFPlanConfig) packageNameFor(spaceID string) string {
	if override := c.Spaces[strings.ToLower(spaceID)]; override.PackageID != "" || override.PackageName != "" {
		return override.PackageName
	}
	return c.PackageName
}

// packageKeyFor 返回空间使用的代码包标识（包名或固定 ID），二者必须恰有一个。
func (c SCFPlanConfig) packageKeyFor(spaceID string) (string, error) {
	name, id := strings.TrimSpace(c.packageNameFor(spaceID)), strings.TrimSpace(c.packageIDFor(spaceID))
	switch {
	case name != "" && id != "":
		return "", fmt.Errorf("scf_plan: space %s sets both package_name and package_id; keep exactly one", spaceID)
	case name != "":
		return "name:" + name, nil
	case id != "":
		return "id:" + id, nil
	default:
		return "", fmt.Errorf("scf_plan: space %s needs package_name (recommended) or package_id", spaceID)
	}
}

// storageAuthSecretEnv 是控制机 Collector 进程里 Storage 主密钥的环境变量名。
const storageAuthSecretEnv = "MOOX_STORAGE_PRIMARY_AUTH_SECRET"

// collectorStorageAppID 是函数写 Storage 使用的应用身份（Storage 绑定里允许的固定值）。
const collectorStorageAppID = "moox-collector"

// builtinPlanEnvironment 是 Collector 自己能生成的函数非受管环境：空间身份与 Storage 应用密钥。
// 应用密钥由 Storage 主密钥按应用 ID 派生，与 Storage 校验方式一致；密钥不落配置文件。
func builtinPlanEnvironment(getenv func(string) string) (map[string]string, error) {
	secret := strings.TrimSpace(getenv(storageAuthSecretEnv))
	if secret == "" {
		return nil, fmt.Errorf("%s is not set; the Collector cannot derive the SCF Storage app key", storageAuthSecretEnv)
	}
	keys, err := json.Marshal(map[string]string{collectorStorageAppID: security.HMACSHA256Hex(secret, []byte(collectorStorageAppID))})
	if err != nil {
		return nil, err
	}
	return map[string]string{"MOOX_STORAGE_PRIMARY_AUTH_APP_KEYS_JSON": string(keys)}, nil
}

// fetchPlanDeploy 读取某个空间的非受管环境文件并生成部署参数。空间身份由对账器所属空间强制写入，
// 不信任环境文件里的取值，避免一个空间的函数拿到另一个空间的身份。
func (c *Config) fetchPlanDeploy(spaceID string) (marketfetch.PlanDeploy, error) {
	override := c.SCFPlan.Spaces[strings.ToLower(spaceID)]
	environment, err := builtinPlanEnvironment(os.Getenv)
	if err != nil {
		return marketfetch.PlanDeploy{}, err
	}
	path := c.SCFPlan.BaseEnvironmentFile
	if override.BaseEnvironmentFile != "" {
		path = override.BaseEnvironmentFile
	}
	if path != "" {
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(c.sourcePath), path)
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return marketfetch.PlanDeploy{}, fmt.Errorf("read scf_plan.base_environment_file for space %s: %w", spaceID, readErr)
		}
		extra := map[string]string{}
		if err := json.Unmarshal(raw, &extra); err != nil {
			return marketfetch.PlanDeploy{}, fmt.Errorf("parse scf_plan.base_environment_file for space %s: %w", spaceID, err)
		}
		// 文件里的额外环境覆盖内置值（运维显式指定优先），空间身份仍由对账器强制写入。
		for key, value := range extra {
			environment[key] = value
		}
	}
	environment["MOOX_SPACE_ID"] = strings.ToLower(spaceID)
	namespace := c.SCFPlan.Namespace
	if override.Namespace != "" {
		namespace = override.Namespace
	}
	return marketfetch.PlanDeploy{
		CloudAccountID: c.SCFPlan.CloudAccountID, PackageID: c.SCFPlan.packageIDFor(spaceID), PackageName: c.SCFPlan.packageNameFor(spaceID), Namespace: namespace, MemoryMB: c.SCFPlan.MemoryMB,
		BaseEnvironment: environment, Networks: c.SCFRegions, NamespaceCapacity: maxFunctionsPerNamespace,
	}, nil
}

// scfPlanAccessEnvironmentRoute 返回某地域的 Access 私网入口配置。
func (c *Config) scfPlanAccessEnvironmentRoute(region string) (SCFAccessEndpoint, error) {
	route, ok := c.SCFPlan.AccessRoutes[strings.ToLower(strings.TrimSpace(region))]
	if !ok {
		return SCFAccessEndpoint{}, fmt.Errorf("region %s has no Access route in scf_plan.access_routes", region)
	}
	return route, nil
}

// scfPlanAccessEnvironment 返回采集计划函数在某地域使用的 Access 环境：私网入口与调用方密钥。
func (c *Config) scfPlanAccessEnvironment(region string) (map[string]string, error) {
	route, err := c.scfPlanAccessEnvironmentRoute(region)
	if err != nil {
		return nil, err
	}
	config := c.SCFAccess
	config.Address, config.InstanceID = route.Address, route.InstanceID
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("scf_plan.access_routes[%s]: %w", region, err)
	}
	keyPath := config.KeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(c.sourcePath), keyPath)
	}
	secret, err := gatewayauth.ReadSigningSecret(keyPath)
	if err != nil {
		return nil, fmt.Errorf("SCF Access signing key: %w", err)
	}
	return map[string]string{"MOOX_ACCESS_ADDRESS": config.Address, "MOOX_ACCESS_ID": config.InstanceID, "MOOX_CALLER": config.Caller, "MOOX_CALLER_KEY_ID": config.KeyID, "MOOX_CALLER_KEY": secret}, nil
}
