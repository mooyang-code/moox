package topology

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// SCFPlan 描述 SCF 定时触发采集（Timer 模式）：哪些空间、哪些市场在哪些地域运行，以及这些地域的函数网络。
// 部署据此渲染 Collector 的 scf_regions / scf_markets / scf_plan 配置；函数本身（数量、标的分配、定时器）
// 由 Collector 的对账协程按任务与标签成员自动创建和维护。
type SCFPlan struct {
	Enabled bool `toml:"enabled"`
	// CloudAccount 是 SCF 与 COS 使用的云账号。
	CloudAccount SCFCloudAccountConfig `toml:"cloud_account"`
	// MemoryMB 是每个函数的内存，默认 128。
	MemoryMB int `toml:"memory_mb"`
	// SymbolsPerFunction 是每个函数负责的标的数，默认 50；按空间可覆盖。
	SymbolsPerFunction int `toml:"symbols_per_function"`
	// AssumedEgressIPs 是函数出口 IP 个数的估计，只用于频控估算与告警，默认 1。
	AssumedEgressIPs int `toml:"assumed_egress_ips"`
	// Regions 是函数所在地域的网络（VPC 与子网），地域内必须有一台标了 external_entry、配置了 private_address 的主机。
	Regions map[string]SCFPlanRegion `toml:"regions"`
	Spaces  []SCFPlanSpace           `toml:"spaces"`
}

// SCFCloudAccountConfig 是 [scf_plan.cloud_account]：云函数账号的登记，credential_secret_id 引用管理台里的腾讯云密钥。
type SCFCloudAccountConfig struct {
	AccountID          string `toml:"account_id"`
	AccountName        string `toml:"account_name"`
	CredentialSecretID string `toml:"credential_secret_id"`
	AppID              string `toml:"app_id"`
	COSRegion          string `toml:"cos_region"`
	COSBucket          string `toml:"cos_bucket"`
}

// SCFPlanRegion 是某个地域的函数网络。
type SCFPlanRegion struct {
	VPCID    string `toml:"vpc_id"`
	SubnetID string `toml:"subnet_id"`
	// EntryHost 指定该地域用哪台主机的外部入口作为函数入口；同地域只有一台有私网地址的入口主机时可省略。
	EntryHost string `toml:"entry_host"`
}

// SCFPlanSpace 是参与 Timer 采集的一个空间。
type SCFPlanSpace struct {
	SpaceID string `toml:"space_id"`
	// Markets 是市场（spot、swap、equity……）到运行地域的映射。
	Markets map[string][]string `toml:"markets"`
	// PackageName 是空间的代码包名称，默认 moox-collector-<space>-plan；Collector 总是使用该名称的最新可用版本。
	PackageName string `toml:"package_name"`
	// Namespace 是函数所在命名空间，默认 moox-<space>-plan。
	Namespace          string `toml:"namespace"`
	SymbolsPerFunction int    `toml:"symbols_per_function"`
}

const (
	defaultSCFPlanMemoryMB           = 128
	defaultSCFPlanSymbolsPerFunction = 50
)

var scfPlanNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// SCFCloudAccount 返回 SCF 使用的云账号。
func (c *Config) SCFCloudAccount() SCFCloudAccountConfig { return c.SCFPlan.CloudAccount }

// SCFEnabled 表示是否启用 SCF 采集：云账号登记与日志服务开通共用。
func (c *Config) SCFEnabled() bool { return c.SCFPlan.Enabled }

// Normalize 去掉首尾空白、小写化地域与空间，并补上缺省的代码包名、命名空间、内存与每函数标的数。
func (p *SCFPlan) Normalize() {
	p.CloudAccount.AccountID = strings.TrimSpace(p.CloudAccount.AccountID)
	p.CloudAccount.AccountName = strings.TrimSpace(p.CloudAccount.AccountName)
	p.CloudAccount.CredentialSecretID = strings.TrimSpace(p.CloudAccount.CredentialSecretID)
	p.CloudAccount.AppID = strings.TrimSpace(p.CloudAccount.AppID)
	p.CloudAccount.COSRegion = strings.TrimSpace(p.CloudAccount.COSRegion)
	p.CloudAccount.COSBucket = strings.TrimSpace(p.CloudAccount.COSBucket)
	regions := make(map[string]SCFPlanRegion, len(p.Regions))
	for name, region := range p.Regions {
		region.VPCID, region.SubnetID = strings.TrimSpace(region.VPCID), strings.TrimSpace(region.SubnetID)
		region.EntryHost = strings.TrimSpace(region.EntryHost)
		regions[strings.ToLower(strings.TrimSpace(name))] = region
	}
	p.Regions = regions
	for i := range p.Spaces {
		space := &p.Spaces[i]
		space.SpaceID = strings.ToLower(strings.TrimSpace(space.SpaceID))
		markets := make(map[string][]string, len(space.Markets))
		for market, list := range space.Markets {
			normalized := make([]string, 0, len(list))
			for _, region := range list {
				normalized = append(normalized, strings.ToLower(strings.TrimSpace(region)))
			}
			markets[strings.ToLower(strings.TrimSpace(market))] = normalized
		}
		space.Markets = markets
		space.PackageName = strings.TrimSpace(space.PackageName)
		space.Namespace = strings.TrimSpace(space.Namespace)
		if space.PackageName == "" && space.SpaceID != "" {
			space.PackageName = "moox-collector-" + space.SpaceID + "-plan"
		}
		if space.Namespace == "" && space.SpaceID != "" {
			space.Namespace = "moox-" + space.SpaceID + "-plan"
		}
	}
	if p.MemoryMB == 0 {
		p.MemoryMB = defaultSCFPlanMemoryMB
	}
	if p.SymbolsPerFunction == 0 {
		p.SymbolsPerFunction = defaultSCFPlanSymbolsPerFunction
	}
}

// CheckSCFPlan 规整并校验 [scf_plan]；用到的地域要有网络和一台可用的入口主机。
func (c *Config) CheckSCFPlan() error {
	plan := &c.SCFPlan
	plan.Normalize()
	if !plan.Enabled {
		return nil
	}
	account := plan.CloudAccount
	if account.AccountID == "" || account.AccountName == "" || account.CredentialSecretID == "" || account.AppID == "" || account.COSRegion == "" || account.COSBucket == "" {
		return fmt.Errorf("config_invalid: scf_plan requires scf_plan.cloud_account with account_id, account_name, credential_secret_id, app_id, cos_region, and cos_bucket")
	}
	if !tencent.IsSCFRegion(account.COSRegion) {
		return fmt.Errorf("config_invalid: scf_plan cloud account cos_region %q is not supported", account.COSRegion)
	}
	if plan.MemoryMB < 64 || plan.MemoryMB > 3072 {
		return fmt.Errorf("config_invalid: scf_plan.memory_mb must be between 64 and 3072")
	}
	if plan.SymbolsPerFunction < 1 || plan.SymbolsPerFunction > 200 {
		return fmt.Errorf("config_invalid: scf_plan.symbols_per_function must be between 1 and 200")
	}
	if len(plan.Spaces) == 0 {
		return fmt.Errorf("config_invalid: scf_plan.spaces must not be empty when scf_plan is enabled")
	}
	seen := map[string]struct{}{}
	packages := map[string]string{}
	used := map[string]struct{}{}
	for i := range plan.Spaces {
		space := &plan.Spaces[i]
		path := fmt.Sprintf("scf_plan.spaces[%d]", i)
		if !scfPlanNamePattern.MatchString(space.SpaceID) {
			return fmt.Errorf("config_invalid: %s.space_id must be a lowercase identifier", path)
		}
		if _, dup := seen[space.SpaceID]; dup {
			return fmt.Errorf("config_invalid: %s.space_id %q is duplicated", path, space.SpaceID)
		}
		seen[space.SpaceID] = struct{}{}
		if other, dup := packages[space.PackageName]; dup {
			return fmt.Errorf("config_invalid: scf_plan spaces %s and %s share package_name %q; every space needs its own code package", other, space.SpaceID, space.PackageName)
		}
		packages[space.PackageName] = space.SpaceID
		if space.SymbolsPerFunction < 0 || space.SymbolsPerFunction > 200 {
			return fmt.Errorf("config_invalid: %s.symbols_per_function must be between 0 and 200", path)
		}
		if len(space.Markets) == 0 {
			return fmt.Errorf("config_invalid: %s.markets must not be empty", path)
		}
		for market, regions := range space.Markets {
			if !scfPlanNamePattern.MatchString(market) || len(regions) == 0 {
				return fmt.Errorf("config_invalid: %s.markets.%s needs a lowercase market name and at least one region", path, market)
			}
			for _, region := range regions {
				if !tencent.IsSCFRegion(region) {
					return fmt.Errorf("config_invalid: %s.markets.%s region %q is not supported", path, market, region)
				}
				used[region] = struct{}{}
			}
		}
	}
	for region := range used {
		network, ok := plan.Regions[region]
		if !ok || network.VPCID == "" || network.SubnetID == "" {
			return fmt.Errorf("config_invalid: scf_plan.regions.%s requires vpc_id and subnet_id (functions run inside the region's VPC)", region)
		}
		if _, _, err := c.SCFPlanEntry(region); err != nil {
			return err
		}
	}
	for region := range plan.Regions {
		if _, ok := used[region]; !ok {
			return fmt.Errorf("config_invalid: scf_plan.regions.%s is not used by any space market", region)
		}
	}
	return nil
}

// SCFPlanEntry 返回某地域 SCF 函数用的外部入口：该地域内标了 external_entry、配置了私网地址的主机，返回它的私网入口地址与主机 ID。
// 函数在地域的 VPC 里运行，只能用私网地址访问。同地域有多台候选时必须用 scf_plan.regions.<region>.entry_host 指定。
func (c *Config) SCFPlanEntry(region string) (address, host string, err error) {
	region = strings.ToLower(strings.TrimSpace(region))
	var inRegion, usable []Host
	for _, host := range c.Hosts() {
		if host.Region != region || !host.ExternalEntry {
			continue
		}
		inRegion = append(inRegion, host)
		if host.PrivateAddress != "" {
			usable = append(usable, host)
		}
	}
	if len(inRegion) == 0 {
		return "", "", fmt.Errorf("config_invalid: scf_plan region %s needs a host in that region (hosts.<id>.region) with external_entry = true", region)
	}
	var chosen Host
	if want := c.SCFPlan.Regions[region].EntryHost; want != "" {
		for _, host := range inRegion {
			if host.Name == want {
				chosen = host
			}
		}
		if chosen.Name == "" {
			return "", "", fmt.Errorf("config_invalid: scf_plan.regions.%s.entry_host %q is not an external entry host in that region", region, want)
		}
	} else {
		switch len(usable) {
		case 1:
			chosen = usable[0]
		case 0:
			return "", "", fmt.Errorf("config_invalid: hosts.%s.private_address is required: scf_plan functions in %s reach the external entry over the VPC", inRegion[0].Name, region)
		default:
			names := make([]string, 0, len(usable))
			for _, host := range usable {
				names = append(names, host.Name)
			}
			return "", "", fmt.Errorf("config_invalid: scf_plan region %s has several external entry hosts with a private address (%s); set scf_plan.regions.%s.entry_host", region, strings.Join(names, ", "), region)
		}
	}
	if chosen.PrivateAddress == "" {
		return "", "", fmt.Errorf("config_invalid: hosts.%s.private_address is required: scf_plan functions in %s reach the external entry over the VPC", chosen.Name, region)
	}
	return net.JoinHostPort(chosen.PrivateAddress, strconv.Itoa(servicecatalog.HostGatewayExternalPort)), chosen.Name, nil
}

// SCFPlanRegionNames 返回计划用到的地域（排序）。
func (c *Config) SCFPlanRegionNames() []string {
	set := map[string]struct{}{}
	for _, space := range c.SCFPlan.Spaces {
		for _, regions := range space.Markets {
			for _, region := range regions {
				set[region] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(set))
	for name := range set {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
