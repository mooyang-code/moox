package privatenet

import (
	"fmt"
	"net"
	"sort"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

const DefaultCCNName = "moox-private-network"

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

func CollectTencentHosts(manifest setupconfig.Manifest) []HostTarget {
	byAddr := make(map[string]*HostTarget)
	add := func(host setupconfig.Host, role string) {
		if !strings.EqualFold(strings.TrimSpace(host.Provider), "tencent") {
			return
		}
		address := strings.TrimSpace(host.Address)
		if address == "" {
			address = strings.TrimSpace(host.Host)
		}
		if address == "" {
			return
		}
		key := strings.ToLower(address)
		if ip := net.ParseIP(key); ip != nil {
			key = ip.String()
		}
		item, ok := byAddr[key]
		if !ok {
			item = &HostTarget{
				Name: strings.TrimSpace(host.Name), Address: address,
				Provider: "tencent",
			}
			byAddr[key] = item
		}
		if strings.TrimSpace(role) != "" {
			item.Roles = appendUnique(item.Roles, role)
		}
		if item.Name == "" {
			item.Name = strings.TrimSpace(host.Name)
		}
	}
	add(manifest.ControlHost, "control")
	if manifest.HasStrategyHost() {
		add(manifest.StrategyHost, "strategy")
	}
	if manifest.HasStorageHost() {
		add(manifest.StorageHost, "storage")
	}
	if manifest.HasViewHost() {
		add(manifest.ViewHost, "view")
	}
	if manifest.HasCompileHost() {
		add(manifest.CompileHost, "compile")
	}
	for _, host := range manifest.OtherHosts {
		role := "other"
		if name := strings.TrimSpace(host.Name); name != "" {
			role = name
		}
		add(host, role)
	}
	out := make([]HostTarget, 0, len(byAddr))
	for _, item := range byAddr {
		sort.Strings(item.Roles)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

func CollectSCFTargets(manifest setupconfig.Manifest) []SCFTarget {
	return collectSCFTargets(manifest, false)
}

func CollectSCFRestoreTargets(manifest setupconfig.Manifest) []SCFTarget {
	return collectSCFTargets(manifest, true)
}

func collectSCFTargets(manifest setupconfig.Manifest, includeIdle bool) []SCFTarget {
	if !manifest.SCFFetcher.Enabled {
		return nil
	}
	byRegion := make(map[string]*SCFTarget)
	add := func(region, namespace, prefix, publicNet string, count int) {
		region = strings.ToLower(strings.TrimSpace(region))
		if region == "" {
			return
		}
		item, ok := byRegion[region]
		if !ok {
			item = &SCFTarget{Region: region, Namespace: firstNonEmpty(namespace, "default"), PublicNetStatus: firstNonEmpty(publicNet, "ENABLE")}
			byRegion[region] = item
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
			if !includeIdle && (!region.Enabled || region.FunctionCount <= 0 || space.IsRegionBlacklisted(region.Region)) {
				continue
			}
			add(region.Region, namespace, space.FunctionPrefix, publicNet, region.FunctionCount)
		}
		if prefix := strings.TrimSpace(space.InstrumentSnapshotFunctionPrefix); prefix != "" && strings.TrimSpace(space.InstrumentSnapshotRegion) != "" {
			if includeIdle || !space.IsRegionBlacklisted(space.InstrumentSnapshotRegion) {
				add(space.InstrumentSnapshotRegion, namespace, prefix, publicNet, 1)
			}
		}
	}
	out := make([]SCFTarget, 0, len(byRegion))
	for _, item := range byRegion {
		sort.Strings(item.Prefixes)
		out = append(out, *item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Region < out[j].Region })
	return out
}

func PrivateServicePorts(eventBusPort int) []string {
	ports := []int{eventBusPort, 11003, 11012, 20100, 20200, 20201, 20202}
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

func StoragePublicIP(hosts []HostTarget) string {
	for _, host := range hosts {
		for _, role := range host.Roles {
			if role == "storage" {
				return host.Address
			}
		}
	}
	return ""
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

func replaceIP(value, from, to string) string {
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)
	if from == "" || to == "" || from == to || !strings.Contains(value, from) {
		return value
	}
	return strings.ReplaceAll(value, from, to)
}

func hostHasRole(host HostTarget, role string) bool {
	for _, item := range host.Roles {
		if item == role {
			return true
		}
	}
	return false
}
