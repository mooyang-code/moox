package topology

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// DefaultCloudRegion 是控制机没写 region 时腾讯云接口使用的默认地域。
const DefaultCloudRegion = "ap-guangzhou"

// SSHConfig 是 moox-cli 登录主机的方式：只支持免密的密钥登录。
type SSHConfig struct {
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	// PrivateKeyFile 是运行 moox-cli 的机器上的私钥文件路径，支持以 ~ 开头；不写时用 ssh-agent 与 ~/.ssh 下的默认私钥。
	PrivateKeyFile string `toml:"private_key_file"`
}

// HostDefinition 是一段 [hosts.<主机ID>]。
type HostDefinition struct {
	// Label 是主机的中文名；不写时按主机 ID 生成（overseas-2 → 海外机 2）。
	Label          string `toml:"label"`
	Address        string `toml:"address"`
	PrivateAddress string `toml:"private_address"`
	Region         string `toml:"region"`
	Provider       string `toml:"provider"`
	// Reachability：direct（默认）或 outbound（只能出站，经控制机网关反向连接）。
	Reachability string    `toml:"reachability"`
	SSH          SSHConfig `toml:"ssh"`
}

func (d *HostDefinition) normalize() {
	d.Label = strings.TrimSpace(d.Label)
	d.Address = strings.TrimSpace(d.Address)
	d.PrivateAddress = strings.TrimSpace(d.PrivateAddress)
	d.Region = strings.ToLower(strings.TrimSpace(d.Region))
	d.Provider = strings.ToLower(strings.TrimSpace(d.Provider))
	d.Reachability = strings.ToLower(strings.TrimSpace(d.Reachability))
	d.SSH.Username = strings.TrimSpace(d.SSH.Username)
	d.SSH.PrivateKeyFile = strings.TrimSpace(d.SSH.PrivateKeyFile)
}

// Host 是从一段主机定义投影出的执行目标，不是独立的配置项。
type Host struct {
	Name           string
	Label          string
	Address        string
	PrivateAddress string
	Region         string
	Provider       string
	// Reachability 为空表示可直达，outbound 表示只能出站。
	Reachability   string
	Port           int
	Username       string
	PrivateKeyFile string
}

// IdentityFiles 是 SSH 登录用的私钥文件：配了 private_key_file 就只用它（加上 ssh-agent），没配时返回 nil，用默认私钥。
func (h Host) IdentityFiles() []string {
	if h.PrivateKeyFile == "" {
		return nil
	}
	return []string{ExpandHome(h.PrivateKeyFile)}
}

// ExpandHome 把以 ~ 开头的路径展开到当前用户的主目录。
func ExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func (c *Config) HostByID(id string) Host {
	definition, ok := c.HostCatalog[id]
	if !ok {
		return Host{}
	}
	reachability, _ := servicecatalog.ParseReachability(definition.Reachability)
	label := definition.Label
	if label == "" {
		label = DefaultHostLabel(id)
	}
	return Host{
		Name: id, Label: label, Address: definition.Address, PrivateAddress: definition.PrivateAddress, Region: definition.Region,
		Provider: definition.Provider, Reachability: reachability, Port: definition.SSH.Port, Username: definition.SSH.Username,
		PrivateKeyFile: definition.SSH.PrivateKeyFile,
	}
}

// ComponentHost 返回单副本组件所在的主机；组件不在任何主机上或在多台主机上时返回空。
func (c *Config) ComponentHost(component string) string {
	selected := ""
	for host, components := range c.Deployments {
		if !slices.Contains(components, component) {
			continue
		}
		if selected != "" && selected != host {
			return ""
		}
		selected = host
	}
	return selected
}

func (c *Config) ControlHost() Host  { return c.HostByID(c.ComponentHost("admin")) }
func (c *Config) EventBusHost() Host { return c.HostByID(c.ComponentHost("eventbus")) }
func (c *Config) StorageHost() Host  { return c.HostByID(c.ComponentHost("storage-primary")) }

// HasEgressProxy 报告拓扑里有没有出口代理：配了海外机才有。没有时 Collector 连不上币安、OKX，下架自动检测要关掉。
func (c *Config) HasEgressProxy() bool { return c.ComponentHost("egress-proxy") != "" }

// CloudRegion 是腾讯云接口的默认地域：取控制机的 region，没写时用 ap-guangzhou。
func (c *Config) CloudRegion() string {
	if region := c.ControlHost().Region; region != "" {
		return region
	}
	return DefaultCloudRegion
}

// Hosts 按主机 ID 排序返回全部主机，控制机排在最前。
func (c *Config) Hosts() []Host {
	ids := make([]string, 0, len(c.HostCatalog))
	for id := range c.HostCatalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	control := c.ComponentHost("admin")
	if index := slices.Index(ids, control); index > 0 {
		copy(ids[1:index+1], ids[:index])
		ids[0] = control
	}
	hosts := make([]Host, 0, len(ids))
	for _, id := range ids {
		hosts = append(hosts, c.HostByID(id))
	}
	return hosts
}

// Topology 是共享的部署模型，包括由代码给每台主机补上的主机级组件。
func (c *Config) Topology() (servicecatalog.Topology, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return servicecatalog.Topology{}, err
	}
	topology := servicecatalog.Topology{ControlHostID: c.ComponentHost("admin")}
	for _, host := range c.Hosts() {
		topology.Hosts = append(topology.Hosts, servicecatalog.Host{ID: host.Name, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Status: servicecatalog.Enabled, Reachability: host.Reachability})
		for _, component := range catalog.Components {
			if component.Scope == servicecatalog.ScopeHost {
				topology.Deployments = append(topology.Deployments, servicecatalog.Deployment{HostID: host.Name, ComponentID: component.ID, Status: servicecatalog.Enabled})
			}
		}
		for _, id := range c.Deployments[host.Name] {
			topology.Deployments = append(topology.Deployments, servicecatalog.Deployment{HostID: host.Name, ComponentID: id, Status: servicecatalog.Enabled})
		}
	}
	return topology, nil
}

var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

func validateHostCatalog(catalog map[string]HostDefinition) error {
	if len(catalog) == 0 || len(catalog) > 1025 {
		return fmt.Errorf("config_invalid: hosts requires 1..1025 definitions")
	}
	seen := make(map[string]string)
	ids := make([]string, 0, len(catalog))
	for id := range catalog {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		definition := catalog[id]
		if !servicecatalog.ValidHostID(id) {
			return fmt.Errorf("config_invalid: hosts keys must be canonical host IDs, not addresses; use [hosts.<host-id>] with address and ssh")
		}
		path := "hosts." + id
		if !servicecatalog.ValidHostAddress(definition.Address) {
			return fmt.Errorf("config_invalid: %s.address must be an IP address or DNS hostname", path)
		}
		if definition.PrivateAddress != "" && !servicecatalog.ValidHostAddress(definition.PrivateAddress) {
			return fmt.Errorf("config_invalid: %s.private_address must be an IP address or DNS hostname", path)
		}
		if len(definition.Region) > 128 {
			return fmt.Errorf("config_invalid: %s.region exceeds 128 bytes", path)
		}
		if _, ok := servicecatalog.ParseReachability(definition.Reachability); !ok {
			return fmt.Errorf("config_invalid: %s.reachability must be direct or outbound", path)
		}
		address := strings.ToLower(strings.TrimSuffix(definition.Address, "."))
		if ip := net.ParseIP(address); ip != nil {
			address = ip.String()
		}
		if previous, exists := seen[address]; exists {
			return fmt.Errorf("config_invalid: %s.address duplicates hosts.%s.address", path, previous)
		}
		seen[address] = id
		if definition.SSH.Port < 1 || definition.SSH.Port > 65535 {
			return fmt.Errorf("config_invalid: %s.ssh.port must be between 1 and 65535", path)
		}
		if definition.SSH.Username == "" {
			return fmt.Errorf("config_invalid: %s.ssh.username is required", path)
		}
		if strings.ContainsAny(definition.SSH.PrivateKeyFile, "\x00\r\n") {
			return fmt.Errorf("config_invalid: %s.ssh.private_key_file 含有控制字符", path)
		}
		if utf8.RuneCountInString(definition.Label) > 32 || strings.ContainsAny(definition.Label, "\r\n\t") {
			return fmt.Errorf("config_invalid: %s.label must be a single line of at most 32 characters", path)
		}
		if definition.Provider != "" && !providerPattern.MatchString(definition.Provider) {
			return fmt.Errorf("config_invalid: %s.provider must use lowercase letters, digits, dash, or underscore", path)
		}
	}
	return nil
}

func (c *Config) validateDeployments() error {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	for host := range c.Deployments {
		if _, ok := c.HostCatalog[host]; !ok {
			return fmt.Errorf("config_invalid: deployments host ID %q is unknown", host)
		}
		for _, id := range c.Deployments[host] {
			component, ok := catalog.Component(id)
			if !ok {
				return fmt.Errorf("config_invalid: [deployments] 主机 %s 的组件 %q 不在服务目录里", host, id)
			}
			if component.Scope == servicecatalog.ScopeHost {
				return fmt.Errorf("config_invalid: [deployments] 主机 %s 的组件 %q 由系统自动部署到每台主机，不要写在 [deployments] 里", host, id)
			}
		}
	}
	topology, err := c.Topology()
	if err != nil {
		return err
	}
	if err := catalog.ValidateTopology(topology); err != nil {
		return fmt.Errorf("config_invalid: deployments: %w", err)
	}
	return nil
}

// hostRoleLabels 是主机 ID 里的角色名对应的中文名。
var hostRoleLabels = map[string]string{
	"control":  "控制机",
	"storage":  "存储机",
	"overseas": "海外机",
	"factor":   "因子机",
}

var hostRolePattern = regexp.MustCompile(`^([a-z]+)(?:-([0-9]+))?$`)

// DefaultHostLabel 按主机 ID 生成中文名：角色名直接对应（overseas → 海外机），带数字后缀时保留序号
// （overseas-2 → 海外机 2）；不是已知角色时返回空，界面回退显示主机 ID。
func DefaultHostLabel(hostID string) string {
	match := hostRolePattern.FindStringSubmatch(strings.ToLower(hostID))
	if match == nil {
		return ""
	}
	label, ok := hostRoleLabels[match[1]]
	if !ok {
		return ""
	}
	if match[2] != "" {
		return label + " " + match[2]
	}
	return label
}
