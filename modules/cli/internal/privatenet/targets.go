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
	AccessAddress   string   `json:"access_address,omitempty"`
}

func CollectTencentHosts(manifest setupconfig.Manifest) []HostTarget {
	byAddr := make(map[string]*HostTarget)
	add := func(host setupconfig.Host, role string) {
		if !strings.EqualFold(strings.TrimSpace(host.Provider), "tencent") {
			return
		}
		address := strings.TrimSpace(host.Address)
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
	add(manifest.ControlHost(), "control")
	if manifest.HasStrategyHost() {
		add(manifest.StrategyHost(), "strategy")
	}
	if manifest.HasStorageHost() {
		add(manifest.StorageHost(), "storage")
	}
	if manifest.HasViewHost() {
		add(manifest.ViewHost(), "view")
	}
	if manifest.HasCompileHost() {
		add(manifest.CompileHost(), "compile")
	}
	for _, host := range manifest.OtherHosts() {
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

func collectSCFTargets(manifest setupconfig.Manifest, _ bool) []SCFTarget {
	if !manifest.SCFPlan.Enabled {
		return nil
	}
	byKey := make(map[string]*SCFTarget)
	for _, space := range manifest.SCFPlan.Spaces {
		namespace := firstNonEmpty(strings.ToLower(strings.TrimSpace(space.Namespace)), "default")
		prefix := "moox-fetch-plan-" + space.SpaceID
		for _, regions := range space.Markets {
			for _, region := range regions {
				region = strings.ToLower(strings.TrimSpace(region))
				if region == "" {
					continue
				}
				key := region + "\x00" + namespace
				item, ok := byKey[key]
				if !ok {
					item = &SCFTarget{Region: region, Namespace: namespace, PublicNetStatus: "ENABLE"}
					if address, _, err := manifest.SCFPlanAccess(region); err == nil {
						item.AccessAddress = strings.TrimSpace(address)
					}
					byKey[key] = item
				}
				item.Prefixes = appendUnique(item.Prefixes, prefix)
				item.FunctionCount++
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

func PrivateServicePorts(eventBusPort int) []string {
	ports := []int{eventBusPort, 11003, 11012}
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
