package bootstrap

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
	MaxConcurrentOperations int                `yaml:"max_concurrent_operations"`
	Fetch                   SCFPlanFetchConfig `yaml:"fetch"`
}

// SCFPlanFetchConfig 是下发给函数的拉取参数（按数据源）。
type SCFPlanFetchConfig struct {
	Concurrency      int `yaml:"concurrency"`
	RPS              int `yaml:"rps"`
	Burst            int `yaml:"burst"`
	FeedConcurrency  int `yaml:"feed_concurrency"`
	RequestTimeoutMS int `yaml:"request_timeout_ms"`
	StorageTimeoutMS int `yaml:"storage_timeout_ms"`
	StaggerMS        int `yaml:"stagger_ms"`
}

func defaultSCFPlan() SCFPlanConfig {
	return SCFPlanConfig{
		Namespace: "default", MemoryMB: 128, SymbolsPerFunction: marketfetch.DefaultSymbolsPerFunction, MaxConcurrentOperations: 20,
		Fetch: SCFPlanFetchConfig{Concurrency: 10, RPS: 50, Burst: 50, FeedConcurrency: 10, RequestTimeoutMS: 3000, StorageTimeoutMS: 2000, StaggerMS: 1500},
	}
}

func (c SCFPlanConfig) planFetch() marketfetch.PlanFetch {
	return marketfetch.PlanFetch{
		Concurrency: c.Fetch.Concurrency, RPS: c.Fetch.RPS, Burst: c.Fetch.Burst, FeedConcurrency: c.Fetch.FeedConcurrency,
		RequestTimeoutMS: c.Fetch.RequestTimeoutMS, StorageTimeoutMS: c.Fetch.StorageTimeoutMS, StaggerMS: c.Fetch.StaggerMS,
	}
}

// defaultLatenessSeconds 是空间的允许迟到时间默认值。
func defaultLatenessSeconds(spaceID string) int {
	switch strings.ToLower(strings.TrimSpace(spaceID)) {
	case "crypto":
		return 15
	case marketfetch.StockCNSpaceID:
		return 90
	default:
		return 60
	}
}

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
				if _, ok := c.SCFAccessRoutes[region]; !ok {
					return fmt.Errorf("scf_markets[%s][%s] region %s has no same-region Access route in scf_access_routes", space, market, region)
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
		BaseEnvironment: environment, Networks: c.SCFRegions,
	}, nil
}
