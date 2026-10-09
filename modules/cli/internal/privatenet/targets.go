package privatenet

import (
	"fmt"
	"net"
	"sort"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type HostTarget struct {
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Roles    []string `json:"roles"`
	Provider string   `json:"provider"`
}

type SCFTarget struct {
	Region          string   `json:"region"`
	Namespace       string   `json:"namespace"`
	Prefixes        []string `json:"prefixes"`
	PublicNetStatus string   `json:"public_net_status"`
	FunctionCount   int      `json:"function_count"`
}

// CollectTencentHosts 返回腾讯云上的 MooX 主机和编译主机；Roles 是主机上部署的组件，编译主机为 compile。
// 同一地址只出现一次。
func CollectTencentHosts(manifest setupconfig.Manifest) []HostTarget {
	byAddr := make(map[string]*HostTarget)
	add := func(name, address, provider string, roles []string) {
		address = strings.TrimSpace(address)
		if !strings.EqualFold(strings.TrimSpace(provider), "tencent") || address == "" {
			return
		}
		key := strings.ToLower(address)
		if ip := net.ParseIP(key); ip != nil {
			key = ip.String()
		}
		item, ok := byAddr[key]
		if !ok {
			item = &HostTarget{Name: name, Address: address, Provider: "tencent"}
			byAddr[key] = item
		}
		for _, role := range roles {
			item.Roles = appendUnique(item.Roles, role)
		}
	}
	for _, host := range manifest.HostList() {
		roles := append(servicecatalog.Default().HostComponents(), manifest.Components(host.ID)...)
		add(host.ID, host.Address, host.Provider, roles)
	}
	if manifest.CompileHost.Configured() {
		add("compile", manifest.CompileHost.Address, manifest.CompileHost.Provider, []string{"compile"})
	}
	out := make([]HostTarget, 0, len(byAddr))
	for _, item := range byAddr {
		sort.Strings(item.Roles)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// CollectSCFTargets 返回启用的 SCF 采集函数所在的地域和命名空间。
func CollectSCFTargets(manifest setupconfig.Manifest) []SCFTarget {
	if !manifest.SCFFetcher.Enabled {
		return nil
	}
	byKey := make(map[string]*SCFTarget)
	add := func(region, namespace, prefix, publicNet string, count int) {
		region = strings.ToLower(strings.TrimSpace(region))
		if region == "" {
			return
		}
		namespace = firstNonEmpty(strings.ToLower(strings.TrimSpace(namespace)), "default")
		key := region + "\x00" + namespace
		item, ok := byKey[key]
		if !ok {
			item = &SCFTarget{Region: region, Namespace: namespace, PublicNetStatus: firstNonEmpty(publicNet, "ENABLE")}
			byKey[key] = item
		}
		item.Prefixes = appendUnique(item.Prefixes, prefix)
		item.FunctionCount += count
		if strings.TrimSpace(publicNet) != "" {
			item.PublicNetStatus = publicNet
		}
	}
	for _, space := range manifest.SCFFetcher.Spaces {
		namespace := strings.TrimSpace(space.Namespace)
		publicNet := strings.ToUpper(strings.TrimSpace(space.PublicNetStatus))
		for _, region := range space.Regions {
			if !region.Enabled || region.FunctionCount <= 0 || space.IsRegionBlacklisted(region.Region) {
				continue
			}
			shards := setupconfig.SpaceRegionNamespaceShards(space, region, manifest.SCFFetcher.TencentLimits)
			if len(shards) == 0 {
				add(region.Region, namespace, space.FunctionPrefix, publicNet, region.FunctionCount)
				continue
			}
			for _, shard := range shards {
				poolCount := shard.Timers
				if strings.EqualFold(strings.TrimSpace(space.SpaceID), "crypto") {
					poolCount = shard.Invokes
				}
				add(region.Region, shard.Namespace, space.FunctionPrefix, publicNet, poolCount)
			}
			if strings.EqualFold(strings.TrimSpace(space.SpaceID), "crypto") {
				if namespace, err := setupconfig.SpaceRegionReleaseCanaryNamespace(space, region, manifest.SCFFetcher.TencentLimits); err == nil {
					add(region.Region, namespace, space.FunctionPrefix, publicNet, 1)
				}
			}
		}
	}
	out := make([]SCFTarget, 0, len(byKey))
	for _, item := range byKey {
		sort.Strings(item.Prefixes)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Region != out[j].Region {
			return out[i].Region < out[j].Region
		}
		return out[i].Namespace < out[j].Namespace
	})
	return out
}

// SCFRegions 返回这些采集函数所在的地域，去重并排序。
func SCFRegions(targets []SCFTarget) []string {
	var regions []string
	for _, target := range targets {
		regions = appendUnique(regions, target.Region)
	}
	sort.Strings(regions)
	return regions
}

// PrivateServicePorts 是主机之间、SCF 到主机经私网访问的端口：消息总线、跨主机入口和外部接入。
func PrivateServicePorts(eventBusPort int) []string {
	ports := []int{eventBusPort, 11003, 11004}
	out := make([]string, 0, len(ports))
	seen := map[string]struct{}{}
	for _, port := range ports {
		if port <= 0 {
			continue
		}
		value := fmt.Sprintf("%d", port)
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func appendUnique(dst []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return dst
	}
	for _, existing := range dst {
		if existing == value {
			return dst
		}
	}
	return append(dst, value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func hostHasRole(host HostTarget, role string) bool {
	for _, item := range host.Roles {
		if item == role {
			return true
		}
	}
	return false
}
