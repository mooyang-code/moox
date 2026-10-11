package bootstrap

import (
	"encoding/json"
	"fmt"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
)

// SCFPlanConfig 是定时触发采集的函数部署参数。网络（VPC、子网）按地域放在 scf_regions，
// 每个空间与市场使用的地域放在 scf_markets，函数访问 Access 的私网入口沿用 scf_access_routes。
type SCFPlanConfig struct {
	CloudAccountID string `yaml:"cloud_account_id"`
	PackageID      string `yaml:"package_id"`
	Namespace      string `yaml:"namespace"`
	MemoryMB       int    `yaml:"memory_mb"`
	// BaseEnvironmentFile 是函数的非受管环境（JSON 对象：空间、Storage 应用密钥、日志服务等），含密钥，权限应为 0600。
	BaseEnvironmentFile string `yaml:"base_environment_file"`
	SymbolsPerFunction  int    `yaml:"symbols_per_function"`
	// MaxConcurrentOperations 限制各空间同时进行的 SCF 配置操作。
	MaxConcurrentOperations int `yaml:"max_concurrent_operations"`
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
}

// SCFPlanSpaceConfig 覆盖某个空间的每函数标的数与拉取参数；字段为零表示沿用全局值。
type SCFPlanSpaceConfig struct {
	SymbolsPerFunction int                 `yaml:"symbols_per_function"`
	Fetch              *SCFPlanFetchConfig `yaml:"fetch"`
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
		fetch = *override
	}
	return marketfetch.PlanFetch{
		Concurrency: fetch.Concurrency, RPS: fetch.RPS, Burst: fetch.Burst, FeedConcurrency: fetch.FeedConcurrency,
		RequestTimeoutMS: fetch.RequestTimeoutMS, StorageTimeoutMS: fetch.StorageTimeoutMS, StaggerBaseMS: fetch.StaggerBaseMS, StaggerMS: fetch.StaggerMS,
	}
}

func defaultSCFPlan() SCFPlanConfig {
	return SCFPlanConfig{
		Namespace: "default", MemoryMB: 128, SymbolsPerFunction: marketfetch.DefaultSymbolsPerFunction, MaxConcurrentOperations: 20,
		Fetch: SCFPlanFetchConfig{Concurrency: 10, RPS: 50, Burst: 50, FeedConcurrency: 10, RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, StaggerMS: 1500},
		// stockcn：每函数 40 个标的；错峰窗口第 5～40 秒；国内数据源限流较保守。
		Spaces: map[string]SCFPlanSpaceConfig{"stockcn": {SymbolsPerFunction: 40, Fetch: &SCFPlanFetchConfig{Concurrency: 8, RPS: 20, Burst: 20, FeedConcurrency: 8, RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, StaggerBaseMS: 5000, StaggerMS: 35000}}},
	}
}

// defaultLatenessSeconds 是空间的允许迟到时间默认值。
func defaultLatenessSeconds(spaceID string) int { return domain.DefaultAllowedLatenessSeconds(spaceID) }

// fetchPlanSpaces 返回配置了 SCF 市场地域、由采集计划对账管理的空间。
func (c *Config) fetchPlanSpaces(spaceIDs []string) []string {
	var spaces []string
	for _, spaceID := range spaceIDs {
		if len(c.SCFMarkets[strings.ToLower(spaceID)]) > 0 {
			spaces = append(spaces, spaceID)
		}
	}
	sort.Strings(spaces)
	return spaces
}

// validateSCFPlan 检查采集计划配置：每个地域必须有 VPC、子网与同地域 Access 私网入口。
func (c *Config) validateSCFPlan() error {
	if len(c.SCFMarkets) == 0 {
		return nil
	}
	plan := c.SCFPlan
	if plan.CloudAccountID == "" || plan.PackageID == "" || plan.BaseEnvironmentFile == "" {
		return fmt.Errorf("scf_plan requires cloud_account_id, package_id and base_environment_file when scf_markets is configured")
	}
	if plan.SymbolsPerFunction < 1 || plan.SymbolsPerFunction > 200 || plan.MemoryMB < 64 || plan.MaxConcurrentOperations < 1 {
		return fmt.Errorf("scf_plan has an out-of-range symbols_per_function, memory_mb or max_concurrent_operations")
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

// fetchPlanDeploy 读取非受管环境文件并生成部署参数。
func (c *Config) fetchPlanDeploy() (marketfetch.PlanDeploy, error) {
	path := c.SCFPlan.BaseEnvironmentFile
	if !filepath.IsAbs(path) {
		path = filepath.Join(filepath.Dir(c.sourcePath), path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return marketfetch.PlanDeploy{}, fmt.Errorf("read scf_plan.base_environment_file: %w", err)
	}
	environment := map[string]string{}
	if err := json.Unmarshal(raw, &environment); err != nil {
		return marketfetch.PlanDeploy{}, fmt.Errorf("parse scf_plan.base_environment_file: %w", err)
	}
	return marketfetch.PlanDeploy{
		CloudAccountID: c.SCFPlan.CloudAccountID, PackageID: c.SCFPlan.PackageID, Namespace: c.SCFPlan.Namespace, MemoryMB: c.SCFPlan.MemoryMB,
		BaseEnvironment: environment, Networks: c.SCFRegions, NamespaceCapacity: maxFunctionsPerNamespace,
	}, nil
}

// scfPlanAccessEnvironment 返回采集计划函数在某地域使用的 Access 环境：私网入口与调用方密钥。
func (c *Config) scfPlanAccessEnvironment(region string) (map[string]string, error) {
	route, ok := c.SCFPlan.AccessRoutes[strings.ToLower(strings.TrimSpace(region))]
	if !ok {
		return nil, fmt.Errorf("region %s has no Access route in scf_plan.access_routes", region)
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
	compression := config.Compression
	if compression == "" {
		compression = gatewayclient.CompressionNone
	}
	return map[string]string{"MOOX_ACCESS_ADDRESS": config.Address, "MOOX_ACCESS_ID": config.InstanceID, "MOOX_CALLER": config.Caller, "MOOX_CALLER_KEY_ID": config.KeyID, "MOOX_CALLER_KEY": secret, "MOOX_GATEWAY_COMPRESSION": compression}, nil
}
