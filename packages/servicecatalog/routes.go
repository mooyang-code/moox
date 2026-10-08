package servicecatalog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/gatewayroute"
)

// Compiled 是按「组件目录 × 部署」编译出的结果：全局服务目录，以及每台主机网关的路由和校验密钥范围。
type Compiled struct {
	Directory Directory
	Hosts     []HostConfig
}

// HostConfig 是一台主机网关需要的路由与校验密钥范围。
type HostConfig struct {
	HostID   string
	Disabled bool
	Routes   []gatewayroute.Route
	// Callers 是本机路由放行的全部调用方，即需要下发给这台主机网关的校验密钥范围。
	Callers []string
}

// Directory 是全局服务目录：哪个 tRPC 服务、哪个组件在哪些主机，以及主机地址。
type Directory struct {
	Version    string           `json:"version"`
	Services   []ServiceHosts   `json:"services"`
	Components []ComponentHosts `json:"components"`
	Hosts      []DirectoryHost  `json:"hosts"`
}

// ServiceHosts 是一个 tRPC 服务所在的主机，按主机 ID 排序。
type ServiceHosts struct {
	Path    string   `json:"path"`
	HostIDs []string `json:"host_ids"`
}

// ComponentHosts 是一个组件启用的部署所在的主机，按主机 ID 排序。
type ComponentHosts struct {
	ComponentID string   `json:"component_id"`
	HostIDs     []string `json:"host_ids"`
}

// DirectoryHost 是服务目录中的主机地址信息。
type DirectoryHost struct {
	ID             string `json:"id"`
	Address        string `json:"address"`
	PrivateAddress string `json:"private_address"`
	Region         string `json:"region"`
}

// Host 返回目录中的一台主机。
func (d Directory) Host(id string) (DirectoryHost, bool) {
	for _, host := range d.Hosts {
		if host.ID == id {
			return host, true
		}
	}
	return DirectoryHost{}, false
}

// ServiceHostIDs 返回服务所在的主机。
func (d Directory) ServiceHostIDs(path string) []string {
	for _, service := range d.Services {
		if service.Path == path {
			return append([]string(nil), service.HostIDs...)
		}
	}
	return nil
}

// ComponentHostIDs 返回组件启用的部署所在的主机。
func (d Directory) ComponentHostIDs(componentID string) []string {
	for _, component := range d.Components {
		if component.ComponentID == componentID {
			return append([]string(nil), component.HostIDs...)
		}
	}
	return nil
}

// HostConfig 返回某台主机的编译结果。
func (c Compiled) HostConfig(hostID string) (HostConfig, bool) {
	for _, host := range c.Hosts {
		if host.HostID == hostID {
			return host, true
		}
	}
	return HostConfig{}, false
}

// Compile 校验部署后，编译服务目录和每台主机的路由。停用的主机不出现在目录里，它的主机网关收到
// Disabled 快照、没有任何路由；停用的部署不产生路由，也不出现在目录里。
func (c *Catalog) Compile(deployment Deployment) (Compiled, error) {
	if err := c.ValidateDeployment(deployment); err != nil {
		return Compiled{}, err
	}
	hosts := append([]Host(nil), deployment.Hosts...)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].ID < hosts[j].ID })
	enabledHosts := map[string]bool{}
	gatewayCallers := make([]string, 0, len(hosts))
	for _, host := range hosts {
		enabledHosts[host.ID] = host.Enabled
		// 网关控制要接收所有已登记主机的拉取，停用的主机也要能拿到 Disabled 快照。
		gatewayCallers = append(gatewayCallers, HostGatewayIdentity(host.ID))
	}

	serviceHosts := map[string][]string{}
	componentHosts := map[string][]string{}
	perHost := map[string][]*Component{}
	for _, placement := range c.EffectivePlacements(deployment) {
		if !placement.Enabled || !enabledHosts[placement.HostID] {
			continue
		}
		component := c.components[placement.ComponentID]
		perHost[placement.HostID] = append(perHost[placement.HostID], component)
		componentHosts[component.ID] = append(componentHosts[component.ID], placement.HostID)
		for _, service := range component.Services {
			serviceHosts[service.Path] = append(serviceHosts[service.Path], placement.HostID)
		}
	}

	directory := Directory{}
	for _, path := range sortedMapKeys(serviceHosts) {
		directory.Services = append(directory.Services, ServiceHosts{Path: path, HostIDs: sortedUnique(serviceHosts[path])})
	}
	for _, id := range sortedMapKeys(componentHosts) {
		directory.Components = append(directory.Components, ComponentHosts{ComponentID: id, HostIDs: sortedUnique(componentHosts[id])})
	}
	for _, host := range hosts {
		if host.Enabled {
			directory.Hosts = append(directory.Hosts, DirectoryHost{ID: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region})
		}
	}
	version, err := DirectoryVersion(directory)
	if err != nil {
		return Compiled{}, err
	}
	directory.Version = version

	compiled := Compiled{Directory: directory}
	for _, host := range hosts {
		config := HostConfig{HostID: host.ID, Disabled: !host.Enabled}
		if host.Enabled {
			callerSet := map[string]bool{}
			for _, component := range perHost[host.ID] {
				for _, service := range component.Services {
					for _, route := range c.serviceRoutes(component, service, gatewayCallers) {
						for _, caller := range route.AllowedCallers {
							callerSet[caller] = true
						}
						config.Routes = append(config.Routes, route)
					}
				}
			}
			config.Callers = sortedKeys(callerSet)
		}
		compiled.Hosts = append(compiled.Hosts, config)
	}
	return compiled, nil
}

// serviceRoutes 把一个服务按「调用方集合」分组成路由；没有任何调用方的方法不经路由。
func (c *Catalog) serviceRoutes(component *Component, service Service, gatewayCallers []string) []gatewayroute.Route {
	groups := map[string][]string{}
	groupCallers := map[string][]string{}
	for _, method := range service.RPCs {
		callers := c.expandCallers(c.acl[service.Path][method], gatewayCallers)
		if len(callers) == 0 {
			continue
		}
		key := strings.Join(callers, ",")
		groups[key] = append(groups[key], method)
		groupCallers[key] = callers
	}
	routes := make([]gatewayroute.Route, 0, len(groups))
	for key, methods := range groups {
		sort.Strings(methods)
		routes = append(routes, gatewayroute.Route{
			ServiceID:      component.ID,
			Address:        "127.0.0.1:" + strconv.Itoa(service.Port),
			ServicePath:    service.Path,
			TimeoutMS:      service.TimeoutMS,
			MaxBodyBytes:   service.MaxBodyBytes,
			AllowedMethods: methods,
			AllowedCallers: groupCallers[key],
		})
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].AllowedMethods[0] < routes[j].AllowedMethods[0] })
	return routes
}

// expandCallers 把 ACL 中的 host-gateway 展开成每台已登记主机的 host-gateway@<主机>。
func (c *Catalog) expandCallers(callers, gatewayCallers []string) []string {
	out := make([]string, 0, len(callers)+len(gatewayCallers))
	for _, caller := range callers {
		if caller == HostGatewayCaller {
			out = append(out, gatewayCallers...)
			continue
		}
		out = append(out, caller)
	}
	return sortedUnique(out)
}

// DirectoryVersion 计算服务目录的版本号：目录内容（不含版本号本身）的 sha256。主机网关据此校验收到的目录。
func DirectoryVersion(directory Directory) (string, error) {
	encoded, err := json.Marshal(canonicalDirectory(directory))
	if err != nil {
		return "", fmt.Errorf("序列化服务目录: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// canonicalDirectory 把空切片统一成非 nil，保证经过 proto 往返后算出的版本号不变。
func canonicalDirectory(directory Directory) Directory {
	out := Directory{Services: []ServiceHosts{}, Components: []ComponentHosts{}, Hosts: []DirectoryHost{}}
	for _, service := range directory.Services {
		out.Services = append(out.Services, ServiceHosts{Path: service.Path, HostIDs: append([]string{}, service.HostIDs...)})
	}
	for _, component := range directory.Components {
		out.Components = append(out.Components, ComponentHosts{ComponentID: component.ComponentID, HostIDs: append([]string{}, component.HostIDs...)})
	}
	out.Hosts = append(out.Hosts, directory.Hosts...)
	return out
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedUnique(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return sortedKeys(set)
}
