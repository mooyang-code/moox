package bootstrap

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"gopkg.in/yaml.v3"

	"github.com/mooyang-code/moox/modules/collector/internal/egressbalance"
	"github.com/mooyang-code/moox/modules/collector/internal/fetchplan"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
)

// SCFPlanConfig 是定时触发采集的函数部署参数。网络（VPC、子网）按地域放在 scf_regions，
// 每个空间与市场使用的地域放在 scf_markets，函数访问外部入口的地址放在 entry_routes（vpc 地域是私网地址，public 地域是公网地址）。
type SCFPlanConfig struct {
	CloudAccountID string `yaml:"cloud_account_id"`
	// PackageName 是代码包的包名：每轮取这个包名最新的可用代码包（上传新版本后函数自动升级）。
	PackageName string `yaml:"package_name"`
	Namespace   string `yaml:"namespace"`
	MemoryMB    int    `yaml:"memory_mb"`
	// BaseEnvironmentFile 是可选的额外非受管环境（JSON 对象），含密钥时权限应为 0600。空间身份由 Collector 自己写入，
	// 日志服务在创建或更新函数时补齐，所以通常不需要这个文件。
	BaseEnvironmentFile string `yaml:"base_environment_file"`
	SymbolsPerFunction  int    `yaml:"symbols_per_function"`
	// MaxConcurrentOperations 限制各空间同时进行的云函数操作（建函数、改配置、换代码、设定时器、删除）。
	MaxConcurrentOperations int `yaml:"max_concurrent_operations"`
	// AssumedEgressIPs 是函数出口 IP 个数的估计（用 moox-cli collector function probe-egress 实测），只用于频控估算与告警。
	AssumedEgressIPs int `yaml:"assumed_egress_ips"`
	// EntryRoutes 是采集计划函数访问外部入口的地址（按地域；vpc 地域是私网，public 地域是公网）。
	EntryRoutes map[string]SCFEntryEndpoint `yaml:"entry_routes"`
	Fetch       SCFPlanFetchConfig          `yaml:"fetch"`
	// Spaces 是按空间的覆盖项：每函数标的数与拉取参数（stockcn 与 crypto 的限流、取数时刻不同）。
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
	// StandbyMS 大于 0 时启用备援请求（行情请求超过这么多毫秒没有结果就并发再发一个备援请求，先成功的为准），默认关闭；
	// StandbyHourOnly 为真时只在整点那一分钟启用。
	StandbyMS       int  `yaml:"standby_ms"`
	StandbyHourOnly bool `yaml:"standby_hour_only"`
	// ConnReuse 为真时函数进程在多次调用之间复用到行情源的 HTTP 连接，默认关闭。
	ConnReuse bool `yaml:"conn_reuse"`
	// NotBeforeMS 是相对收盘的最早取数时刻（毫秒）：行情源 REST 要到收盘后一段时间才返回定稿值（币安合约约 8 秒）。
	NotBeforeMS int `yaml:"not_before_ms"`
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
		StorageTimeoutMS: pick(f.StorageTimeoutMS, base.StorageTimeoutMS),
		StandbyMS:        pick(f.StandbyMS, base.StandbyMS), StandbyHourOnly: f.StandbyHourOnly || base.StandbyHourOnly,
		ConnReuse: f.ConnReuse || base.ConnReuse, NotBeforeMS: pick(f.NotBeforeMS, base.NotBeforeMS),
	}
}

// SCFPlanMarketConfig 覆盖空间内某个市场（spot、swap 等）的拉取参数与 SCF 模式下的允许迟到时间；字段为零表示沿用空间值。
type SCFPlanMarketConfig struct {
	Fetch *SCFPlanFetchConfig `yaml:"fetch"`
	// LatenessS 是该市场数据集在拉取模式下的允许迟到时间默认值（任务显式指定的以任务为准）。
	LatenessS int `yaml:"lateness_s"`
}

// SCFPlanAutoSize 是按配置估算每函数标的数的参数（见 fetchplan.FleetSizing）。
type SCFPlanAutoSize struct {
	TargetFetchMS int `yaml:"target_fetch_ms"`
	RequestMS     int `yaml:"request_ms"`
	MinFunctions  int `yaml:"min_functions"`
}

// SCFPlanEgressBalance 是函数出口 IP 去重的参数（见 egressbalance 包）：计划新建函数后，探测各函数的出口 IP，把共用同一个 IP 的函数删掉重建。
// 任何空间都可以配置；idle_start、idle_end（"15:04" 格式，按 timezone 的时钟，起点晚于终点表示跨午夜）限定允许删除重建的时段，都不写表示任何时候。
type SCFPlanEgressBalance struct {
	MaxPerIP  int    `yaml:"max_per_ip"`
	Rounds    int    `yaml:"rounds"`
	IdleStart string `yaml:"idle_start"`
	IdleEnd   string `yaml:"idle_end"`
	Timezone  string `yaml:"timezone"`
}

func (b SCFPlanEgressBalance) config() egressbalance.Config {
	return egressbalance.Config{MaxPerIP: b.MaxPerIP, Rounds: b.Rounds, IdleWindow: egressbalance.Window{Start: b.IdleStart, End: b.IdleEnd, Timezone: b.Timezone}}
}

// SCFPlanSpaceConfig 覆盖某个空间的每函数标的数与拉取参数；字段为零表示沿用全局值。
type SCFPlanSpaceConfig struct {
	// SymbolsPerFunction 大于 0 时固定每函数标的数；为 0 且配置了 AutoSize 时按源的权重与限速、标的数估算，函数个数随之调整。
	SymbolsPerFunction int              `yaml:"symbols_per_function"`
	AutoSize           *SCFPlanAutoSize `yaml:"auto_size"`
	// EgressBalance 非空时在收盘后的空闲时段探测函数出口 IP，并重建共用 IP 的函数；只对 A 股有效。
	EgressBalance *SCFPlanEgressBalance `yaml:"egress_balance"`
	Fetch         *SCFPlanFetchConfig   `yaml:"fetch"`
	// PackageName、Namespace、BaseEnvironmentFile 为空表示沿用全局值。代码包按空间构建（带各自的市场配置），
	// 同时管理多个空间时每个空间必须有自己的代码包。
	PackageName         string `yaml:"package_name"`
	Namespace           string `yaml:"namespace"`
	BaseEnvironmentFile string `yaml:"base_environment_file"`
	// BackfillRegion 是补采函数所在的地域；为空时用各市场的第一个地域。
	BackfillRegion string `yaml:"backfill_region"`
	// Markets 是按市场的覆盖项（现货、合约的取数时刻与截止不同）。
	Markets map[string]SCFPlanMarketConfig `yaml:"markets"`
}

// autoSizing 返回空间按配置估算每函数标的数的参数；空间固定了 symbols_per_function 或没有配置 auto_size 时返回 nil。
func (c SCFPlanConfig) autoSizing(spaceID string) *fetchplan.FleetSizing {
	space := c.Spaces[strings.ToLower(spaceID)]
	if space.SymbolsPerFunction > 0 || space.AutoSize == nil {
		return nil
	}
	return &fetchplan.FleetSizing{TargetFetchMS: space.AutoSize.TargetFetchMS, RequestMS: space.AutoSize.RequestMS, MinFunctions: space.AutoSize.MinFunctions}
}

// maxFunctionsPerNamespace 是腾讯云每个命名空间的函数数上限，超过后分到下一个命名空间。
const maxFunctionsPerNamespace = 50

func (c SCFPlanConfig) symbolsPerFunction(spaceID string) int {
	if override := c.Spaces[strings.ToLower(spaceID)]; override.SymbolsPerFunction > 0 {
		return override.SymbolsPerFunction
	}
	return c.SymbolsPerFunction
}

// fetchFor 返回某空间某市场的拉取参数：市场覆盖 → 空间覆盖 → 全局值，未写的字段逐级沿用。
func (c SCFPlanConfig) fetchFor(spaceID, marketType string) marketfetch.PlanFetch {
	space := c.Spaces[strings.ToLower(spaceID)]
	fetch := c.Fetch
	if space.Fetch != nil {
		fetch = space.Fetch.filled(c.Fetch)
	}
	if override := space.Markets[strings.ToLower(marketType)].Fetch; override != nil {
		fetch = override.filled(fetch)
	}
	return marketfetch.PlanFetch{
		Concurrency: fetch.Concurrency, RPS: fetch.RPS, Burst: fetch.Burst, FeedConcurrency: fetch.FeedConcurrency,
		RequestTimeoutMS: fetch.RequestTimeoutMS, StorageTimeoutMS: fetch.StorageTimeoutMS,
		StandbyMS: fetch.StandbyMS, StandbyHourOnly: fetch.StandbyHourOnly,
		ConnReuse: fetch.ConnReuse, NotBeforeMS: fetch.NotBeforeMS,
	}
}

// marketFetches 返回空间里写了覆盖项的各市场的拉取参数；没写的市场用 fetchFor(spaceID, "")。
func (c SCFPlanConfig) marketFetches(spaceID string) map[string]marketfetch.PlanFetch {
	result := map[string]marketfetch.PlanFetch{}
	for market := range c.Spaces[strings.ToLower(spaceID)].Markets {
		result[market] = c.fetchFor(spaceID, market)
	}
	return result
}

// marketLateness 返回空间里各市场在拉取模式下的允许迟到时间默认值（0 的不列出，沿用空间默认）。
func (c SCFPlanConfig) marketLateness(spaceID string) map[string]int {
	result := map[string]int{}
	for market, override := range c.Spaces[strings.ToLower(spaceID)].Markets {
		if override.LatenessS > 0 {
			result[market] = override.LatenessS
		}
	}
	return result
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
		case override.AutoSize == nil && base.AutoSize != nil:
			auto := *base.AutoSize
			override.AutoSize = &auto
		case override.AutoSize != nil && base.AutoSize != nil:
			auto := *override.AutoSize
			if auto.TargetFetchMS == 0 {
				auto.TargetFetchMS = base.AutoSize.TargetFetchMS
			}
			if auto.RequestMS == 0 {
				auto.RequestMS = base.AutoSize.RequestMS
			}
			if auto.MinFunctions == 0 {
				auto.MinFunctions = base.AutoSize.MinFunctions
			}
			override.AutoSize = &auto
		}
		if override.EgressBalance == nil && base.EgressBalance != nil {
			balance := *base.EgressBalance
			override.EgressBalance = &balance
		}
		switch {
		case override.Fetch == nil && base.Fetch != nil:
			fetch := *base.Fetch
			override.Fetch = &fetch
		case override.Fetch != nil && base.Fetch != nil:
			fetch := override.Fetch.filled(*base.Fetch)
			override.Fetch = &fetch
		}
		for market, defaults := range base.Markets {
			current, ok := override.Markets[market]
			if !ok {
				if override.Markets == nil {
					override.Markets = map[string]SCFPlanMarketConfig{}
				}
				override.Markets[market] = defaults
				continue
			}
			if current.LatenessS == 0 {
				current.LatenessS = defaults.LatenessS
			}
			switch {
			case current.Fetch == nil && defaults.Fetch != nil:
				fetch := *defaults.Fetch
				current.Fetch = &fetch
			case current.Fetch != nil && defaults.Fetch != nil:
				fetch := current.Fetch.filled(*defaults.Fetch)
				current.Fetch = &fetch
			}
			override.Markets[market] = current
		}
		c.Spaces[space] = override
	}
}

func defaultSCFPlan() SCFPlanConfig {
	return SCFPlanConfig{
		Namespace: "default", MemoryMB: 128, SymbolsPerFunction: fetchplan.DefaultSymbolsPerFunction, MaxConcurrentOperations: 20,
		// 写入阶段预算 4 秒：行情请求 3 秒 + 写入 4 秒仍在 1m 数据集 10 秒的迟到窗口内；2 秒太紧，
		// 跨境建连稍慢或整分钟突发时存储端稍慢就超时，也没有时间重试。
		Fetch: SCFPlanFetchConfig{Concurrency: 50, RPS: 50, Burst: 50, FeedConcurrency: 50, RequestTimeoutMS: 3000, StorageTimeoutMS: 4000},
		// 各空间的默认值写在市场的数据文件里（config/markets/<市场>/market.yaml 的 scf_plan 段）。
		Spaces: marketSCFPlanDefaults(),
	}
}

// marketSCFPlanDefaults 读各市场 market.yaml 的 scf_plan 段作为空间的默认值；没写这一段的市场没有空间默认值。
// market.yaml 编在二进制里，解析失败是编码错误，直接 panic。
func marketSCFPlanDefaults() map[string]SCFPlanSpaceConfig {
	spaces := map[string]SCFPlanSpaceConfig{}
	for _, marketID := range markets.IDs() {
		market, err := markets.ConfigFor(marketID)
		if err != nil {
			panic(err)
		}
		if market.SCFPlan.IsZero() {
			continue
		}
		raw, err := yaml.Marshal(&market.SCFPlan)
		if err != nil {
			panic(fmt.Errorf("市场 %s 的 scf_plan：%w", marketID, err))
		}
		var space SCFPlanSpaceConfig
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&space); err != nil {
			panic(fmt.Errorf("市场 %s 的 scf_plan：%w", marketID, err))
		}
		spaces[marketID] = space
	}
	return spaces
}

// defaultLatenessSeconds 是空间的允许迟到时间默认值（market.yaml 的 allowed_lateness_seconds）；市场未登记时报错。
func defaultLatenessSeconds(spaceID string) (int, error) {
	market, err := markets.ConfigFor(spaceID)
	if err != nil {
		return 0, err
	}
	return market.AllowedLatenessSeconds, nil
}

// scfSpaces 返回配置了云函数市场地域的空间（排序）。它只代表云函数空间：不用云函数的空间由启用的任务决定，见 managedSpaces。
func (c *Config) scfSpaces() []string {
	spaces := make([]string, 0, len(c.SCFMarkets))
	for spaceID, markets := range c.SCFMarkets {
		if len(markets) > 0 {
			spaces = append(spaces, strings.ToLower(spaceID))
		}
	}
	sort.Strings(spaces)
	return spaces
}

// mergeSpaces 合并空间列表：转小写、去空、去重、排序。
func mergeSpaces(lists ...[]string) []string {
	set := map[string]struct{}{}
	for _, list := range lists {
		for _, space := range list {
			if space = strings.ToLower(strings.TrimSpace(space)); space != "" {
				set[space] = struct{}{}
			}
		}
	}
	all := make([]string, 0, len(set))
	for space := range set {
		all = append(all, space)
	}
	sort.Strings(all)
	return all
}

// validateSCFPlan 检查采集计划配置：用到的每个地域要有合法的网络（见 PlanNetwork.Check）与入口路由。
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
		name := strings.TrimSpace(plan.packageNameFor(space))
		if name == "" {
			return fmt.Errorf("scf_plan: space %s needs package_name", space)
		}
		if other, dup := packages[name]; dup && len(c.SCFMarkets) > 1 {
			return fmt.Errorf("scf_plan: spaces %s and %s share code package %s; every space needs its own package_name in scf_plan.spaces", other, space, name)
		}
		packages[name] = space
	}
	for space, override := range plan.Spaces {
		if region := override.BackfillRegion; region != "" {
			if err := c.checkSCFRegion(region); err != nil {
				return fmt.Errorf("scf_plan.spaces[%s].backfill_region %s: %w", space, region, err)
			}
		}
		if auto := override.AutoSize; auto != nil && (auto.TargetFetchMS < 500 || auto.TargetFetchMS > 10000 || auto.RequestMS < 50 || auto.RequestMS > auto.TargetFetchMS || auto.MinFunctions < 1 || auto.MinFunctions > 50) {
			return fmt.Errorf("scf_plan.spaces[%s].auto_size 超出范围：target_fetch_ms 500～10000，request_ms 50～target_fetch_ms，min_functions 1～50", space)
		}
		if balance := override.EgressBalance; balance != nil {
			if _, err := egressbalance.New(balance.config()); err != nil {
				return fmt.Errorf("scf_plan.spaces[%s].egress_balance: %w", space, err)
			}
		}
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
				if err := c.checkSCFRegion(region); err != nil {
					return fmt.Errorf("scf_markets[%s][%s] region %s: %w", space, market, region, err)
				}
			}
		}
	}
	return nil
}

// checkSCFRegion 校验某地域的函数网络与入口路由：网络见 PlanNetwork.Check；vpc 地域的入口是同地域入口主机的私网地址，
// public 地域的入口是入口主机的公网地址，两种都要在 scf_plan.entry_routes 里。
func (c *Config) checkSCFRegion(region string) error {
	network, ok := c.SCFRegions[region]
	if !ok {
		return fmt.Errorf("has no network in scf_regions")
	}
	if err := network.Check(); err != nil {
		return fmt.Errorf("scf_regions: %w", err)
	}
	if _, ok := c.SCFPlan.EntryRoutes[region]; !ok {
		return fmt.Errorf("has no entry route in scf_plan.entry_routes")
	}
	return nil
}

func (c SCFPlanConfig) packageNameFor(spaceID string) string {
	if override := c.Spaces[strings.ToLower(spaceID)]; override.PackageName != "" {
		return override.PackageName
	}
	return c.PackageName
}

// fetchPlanDeploy 读取某个空间的非受管环境文件并生成部署参数。空间身份由对账器所属空间强制写入，
// 不信任环境文件里的取值，避免一个空间的函数拿到另一个空间的身份。
func (c *Config) fetchPlanDeploy(spaceID string) (fetchplan.PlanDeploy, error) {
	override := c.SCFPlan.Spaces[strings.ToLower(spaceID)]
	environment := map[string]string{}
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
			return fetchplan.PlanDeploy{}, fmt.Errorf("read scf_plan.base_environment_file for space %s: %w", spaceID, readErr)
		}
		extra := map[string]string{}
		if err := json.Unmarshal(raw, &extra); err != nil {
			return fetchplan.PlanDeploy{}, fmt.Errorf("parse scf_plan.base_environment_file for space %s: %w", spaceID, err)
		}
		// 文件里的额外环境原样带上，空间身份仍由对账器强制写入。
		for key, value := range extra {
			environment[key] = value
		}
	}
	environment["MOOX_SPACE_ID"] = strings.ToLower(spaceID)
	namespace := c.SCFPlan.Namespace
	if override.Namespace != "" {
		namespace = override.Namespace
	}
	return fetchplan.PlanDeploy{
		CloudAccountID: c.SCFPlan.CloudAccountID, PackageName: c.SCFPlan.packageNameFor(spaceID), Namespace: namespace, MemoryMB: c.SCFPlan.MemoryMB,
		BaseEnvironment: environment, Networks: c.SCFRegions, NamespaceCapacity: maxFunctionsPerNamespace,
	}, nil
}

// scfPlanEntryEnvironmentRoute 返回某地域的外部入口地址配置。
func (c *Config) scfPlanEntryEnvironmentRoute(region string) (SCFEntryEndpoint, error) {
	route, ok := c.SCFPlan.EntryRoutes[strings.ToLower(strings.TrimSpace(region))]
	if !ok {
		return SCFEntryEndpoint{}, fmt.Errorf("region %s has no entry route in scf_plan.entry_routes", region)
	}
	return route, nil
}

// scfPlanEntryEnvironment 返回采集计划函数在某地域使用的外部入口环境：入口地址与调用方密钥。
func (c *Config) scfPlanEntryEnvironment(region string) (map[string]string, error) {
	route, err := c.scfPlanEntryEnvironmentRoute(region)
	if err != nil {
		return nil, err
	}
	config := c.SCFEntry
	config.Address, config.Host = route.Address, route.Host
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("scf_plan.entry_routes[%s]: %w", region, err)
	}
	keyPath := config.KeyFile
	if !filepath.IsAbs(keyPath) {
		keyPath = filepath.Join(filepath.Dir(c.sourcePath), keyPath)
	}
	// 每次都重读私钥文件：控制机重新签发 scf-collector 的密钥后，下一轮对账就把新密钥写进函数环境。
	key, err := gatewayauth.ReadSigningKey(keyPath)
	if err != nil {
		return nil, fmt.Errorf("SCF entry signing key: %w", err)
	}
	// MOOX_CALLER_KEY_ID 是公钥的十六进制，对账用它看出云上函数还带着旧密钥。
	public := gatewayauth.Credentials{Key: key}.PublicKey()
	return map[string]string{"MOOX_ENTRY_ADDRESS": config.Address, "MOOX_ENTRY_HOST": config.Host, "MOOX_CALLER": config.Caller, "MOOX_CALLER_KEY_ID": hex.EncodeToString(public), "MOOX_CALLER_KEY": gatewayauth.EncodeSigningKey(key)}, nil
}
