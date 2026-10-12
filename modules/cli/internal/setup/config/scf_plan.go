package config

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// SCFPlan 描述 SCF 定时触发采集（Timer 模式）：哪些空间、哪些市场在哪些地域运行，以及这些地域的函数网络。
// 初始化与部署据此渲染 Collector 的 scf_regions / scf_markets / scf_plan 配置，上传代码包；函数本身
// （数量、标的分配、定时器）由 Collector 的对账协程按任务与标签成员自动创建和维护。
type SCFPlan struct {
	Enabled bool `toml:"enabled"`
	// CloudAccount 为空时沿用 [scf_fetcher.cloud_account]（旧段在清理前仍可提供云账号）。
	CloudAccount SCFFetcherCloudAccount `toml:"cloud_account"`
	// MemoryMB 是每个函数的内存，默认 128。
	MemoryMB int `toml:"memory_mb"`
	// SymbolsPerFunction 是每个函数负责的标的数，默认 50；按空间可覆盖。
	SymbolsPerFunction int `toml:"symbols_per_function"`
	// Regions 是函数所在地域的网络（VPC 与子网），地域内必须有一台带 access 组件、配置了 private_address 的主机。
	Regions map[string]SCFPlanRegion `toml:"regions"`
	Spaces  []SCFPlanSpace           `toml:"spaces"`
}

// SCFPlanRegion 是某个地域的函数网络。
type SCFPlanRegion struct {
	VPCID    string `toml:"vpc_id"`
	SubnetID string `toml:"subnet_id"`
	// AccessHost 指定该地域用哪台主机的 Access 作为函数入口；同地域只有一台有私网地址的 Access 主机时可省略，
	// 有多台时必须显式指定（不同 VPC 的入口函数未必可达）。
	AccessHost string `toml:"access_host"`
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
	// scfPlanAccessPort 是 Access 的网关端口。
	scfPlanAccessPort = "11004"
)

var scfPlanNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// SCFCloudAccount 返回 SCF 使用的云账号：[scf_plan.cloud_account] 优先，否则沿用 [scf_fetcher.cloud_account]。
func (m Manifest) SCFCloudAccount() SCFFetcherCloudAccount {
	if m.SCFPlan.CloudAccount.AccountID != "" {
		return m.SCFPlan.CloudAccount
	}
	return m.SCFFetcher.CloudAccount
}

// SCFEnabled 表示是否使用任何 SCF 采集（Timer 计划或旧的 Invoke 函数）：云账号登记与日志服务开通共用。
func (m Manifest) SCFEnabled() bool { return m.SCFPlan.Enabled || m.SCFFetcher.Enabled }

func normalizeSCFPlan(plan *SCFPlan) {
	plan.CloudAccount.AccountID = strings.TrimSpace(plan.CloudAccount.AccountID)
	plan.CloudAccount.AccountName = strings.TrimSpace(plan.CloudAccount.AccountName)
	plan.CloudAccount.CredentialSecretID = strings.TrimSpace(plan.CloudAccount.CredentialSecretID)
	plan.CloudAccount.AppID = strings.TrimSpace(plan.CloudAccount.AppID)
	plan.CloudAccount.COSRegion = strings.TrimSpace(plan.CloudAccount.COSRegion)
	plan.CloudAccount.COSBucket = strings.TrimSpace(plan.CloudAccount.COSBucket)
	regions := make(map[string]SCFPlanRegion, len(plan.Regions))
	for name, region := range plan.Regions {
		region.VPCID, region.SubnetID = strings.TrimSpace(region.VPCID), strings.TrimSpace(region.SubnetID)
		region.AccessHost = strings.TrimSpace(region.AccessHost)
		regions[strings.ToLower(strings.TrimSpace(name))] = region
	}
	plan.Regions = regions
	for i := range plan.Spaces {
		space := &plan.Spaces[i]
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
	if plan.MemoryMB == 0 {
		plan.MemoryMB = defaultSCFPlanMemoryMB
	}
	if plan.SymbolsPerFunction == 0 {
		plan.SymbolsPerFunction = defaultSCFPlanSymbolsPerFunction
	}
}

func validateSCFPlan(manifest *Manifest) error {
	plan := &manifest.SCFPlan
	normalizeSCFPlan(plan)
	if !plan.Enabled {
		return nil
	}
	account := manifest.SCFCloudAccount()
	if account.AccountID == "" || account.AccountName == "" || account.CredentialSecretID == "" || account.AppID == "" || account.COSRegion == "" || account.COSBucket == "" {
		return fmt.Errorf("config_invalid: scf_plan requires a cloud account (scf_plan.cloud_account or scf_fetcher.cloud_account) with account_id, account_name, credential_secret_id, app_id, cos_region, and cos_bucket")
	}
	if !supportedSCFRegion(account.COSRegion) {
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
				if !supportedSCFRegion(region) {
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
		if _, _, err := manifest.SCFPlanAccess(region); err != nil {
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

// SCFPlanAccess 返回某地域 SCF 函数访问 Access 的私网入口：该地域内带 access 组件、配置了私网地址的主机。
// 函数在地域的 VPC 里运行，只能用私网地址访问。同地域有多台候选时必须用 scf_plan.regions.<region>.access_host 指定。
func (m Manifest) SCFPlanAccess(region string) (address, id string, err error) {
	region = strings.ToLower(strings.TrimSpace(region))
	var inRegion, usable []Host
	for _, host := range m.Hosts() {
		if host.Region != region || !slices.Contains(m.Placements[host.Name], "access") {
			continue
		}
		inRegion = append(inRegion, host)
		if host.PrivateAddress != "" {
			usable = append(usable, host)
		}
	}
	if len(inRegion) == 0 {
		return "", "", fmt.Errorf("config_invalid: scf_plan region %s needs a host in that region (hosts.<id>.region) placed with the access component", region)
	}
	var chosen Host
	if want := m.SCFPlan.Regions[region].AccessHost; want != "" {
		for _, host := range inRegion {
			if host.Name == want {
				chosen = host
			}
		}
		if chosen.Name == "" {
			return "", "", fmt.Errorf("config_invalid: scf_plan.regions.%s.access_host %q is not an access host in that region", region, want)
		}
	} else {
		switch len(usable) {
		case 1:
			chosen = usable[0]
		case 0:
			return "", "", fmt.Errorf("config_invalid: hosts.%s.private_address is required: scf_plan functions in %s reach Access over the VPC", inRegion[0].Name, region)
		default:
			names := make([]string, 0, len(usable))
			for _, host := range usable {
				names = append(names, host.Name)
			}
			return "", "", fmt.Errorf("config_invalid: scf_plan region %s has several access hosts with a private address (%s); set scf_plan.regions.%s.access_host", region, strings.Join(names, ", "), region)
		}
	}
	if chosen.PrivateAddress == "" {
		return "", "", fmt.Errorf("config_invalid: hosts.%s.private_address is required: scf_plan functions in %s reach Access over the VPC", chosen.Name, region)
	}
	return net.JoinHostPort(chosen.PrivateAddress, scfPlanAccessPort), "access@" + chosen.Name, nil
}

// SCFPlanRegionNames 返回计划用到的地域（排序）。
func (m Manifest) SCFPlanRegionNames() []string {
	set := map[string]struct{}{}
	for _, space := range m.SCFPlan.Spaces {
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
