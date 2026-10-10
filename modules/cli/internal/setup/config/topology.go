package config

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	egresspb "github.com/mooyang-code/moox/modules/egressproxy/proto/egressgen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// Host 是 [hosts.<主机 ID>] 中的一台 MooX 主机。主机 ID 即主机名，也是 SysDeploy 中的主机 ID。
type Host struct {
	ID string `toml:"-"`
	// Address 是公网地址：SSH、跨主机入口和控制台都用它。
	Address string `toml:"address"`
	// PrivateAddress 是可选的私网地址，同地域的 SCF 经它访问外部接入。
	PrivateAddress string `toml:"private_address"`
	// Region 是主机所在地域，例如 ap-nanjing；部署了外部接入的主机必须填写。
	Region string `toml:"region"`
	// Root 是部署根目录，省略时为 <paths.deploy_root>/<主机 ID>。
	Root string `toml:"root"`
	// TLSMode 是控制台代理的证书方式（auto、public、internal），只能写在部署了控制台代理的主机上。
	TLSMode string `toml:"tls_mode"`
	// Provider 是云厂商，例如 tencent；用于防火墙和 VPC 查询。
	Provider string `toml:"provider"`
	SSH      SSH    `toml:"ssh"`
}

// SSH 是登录一台主机的方式。
type SSH struct {
	Port     int    `toml:"port"`
	Username string `toml:"username"`
	Password string `toml:"password"`
}

// CompileHost 是编译 CGO 组件（Storage、因子管理）的机器，它不是 MooX 主机，不部署任何组件。
type CompileHost struct {
	Address  string `toml:"address"`
	Provider string `toml:"provider"`
	SSH      SSH    `toml:"ssh"`
}

// Configured 判断是否配置了编译主机。
func (c CompileHost) Configured() bool {
	return strings.TrimSpace(c.Address) != "" || strings.TrimSpace(c.SSH.Username) != "" || c.SSH.Port != 0 || c.SSH.Password != ""
}

// EgressProxy 是出口代理的配置：HTTPDomains 是经出口代理访问的域名（支持 "*." 后缀通配），DNS 是出口代理的 DNS 解析。
type EgressProxy struct {
	HTTPDomains []string  `toml:"http_domains"`
	DNS         EgressDNS `toml:"dns"`
}

// EgressDNS 是出口代理的 DNS 解析配置（原来的 [dns_resolver]）。
type EgressDNS struct {
	Domains                []string `toml:"domains"`
	RefreshIntervalSeconds int      `toml:"refresh_interval_seconds"`
	RequestTimeoutMS       int      `toml:"request_timeout_ms"`
	LookupTimeoutMS        int      `toml:"lookup_timeout_ms"`
	ProbeTimeoutMS         int      `toml:"probe_timeout_ms"`
	ProbePort              int      `toml:"probe_port"`
	CacheTTLSeconds        int      `toml:"cache_ttl_seconds"`
	MaxIPsPerDomain        int      `toml:"max_ips_per_domain"`
}

var (
	hostIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	regionPattern = regexp.MustCompile(`^[a-z]{2}-[a-z0-9-]{2,30}$`)
	rootPattern   = regexp.MustCompile(`^/[A-Za-z0-9/._-]+$`)
)

// HostIDs 返回全部主机 ID：control 在前，其余按 ID 排序。
func (m Manifest) HostIDs() []string {
	ids := make([]string, 0, len(m.Hosts))
	for id := range m.Hosts {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ids[i] == servicecatalog.ControlHostID || ids[j] == servicecatalog.ControlHostID {
			return ids[i] == servicecatalog.ControlHostID
		}
		return ids[i] < ids[j]
	})
	return ids
}

// HostList 返回全部主机，顺序同 HostIDs。
func (m Manifest) HostList() []Host {
	out := make([]Host, 0, len(m.Hosts))
	for _, id := range m.HostIDs() {
		out = append(out, m.Hosts[id])
	}
	return out
}

// Host 按 ID 返回主机。
func (m Manifest) Host(id string) (Host, bool) {
	host, ok := m.Hosts[strings.TrimSpace(id)]
	return host, ok
}

// ControlHost 返回 control 主机（校验保证它存在）。
func (m Manifest) ControlHost() Host {
	return m.Hosts[servicecatalog.ControlHostID]
}

// Components 返回部署表中一台主机的业务组件（不含每台主机自动部署的主机组件），顺序同 moox.toml。
func (m Manifest) Components(hostID string) []string {
	return append([]string(nil), m.Placements[strings.TrimSpace(hostID)]...)
}

// HostsOf 返回部署了某个组件的主机 ID，顺序同 HostIDs；主机组件部署在每台主机上。
func (m Manifest) HostsOf(componentID string) []string {
	if component, ok := servicecatalog.Default().Component(componentID); ok && component.Scope == servicecatalog.ScopeHost {
		return m.HostIDs()
	}
	var out []string
	for _, id := range m.HostIDs() {
		for _, placed := range m.Placements[id] {
			if placed == componentID {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// HasComponent 判断一台主机是否部署了某个组件（含主机组件）。
func (m Manifest) HasComponent(hostID, componentID string) bool {
	for _, id := range m.HostsOf(componentID) {
		if id == hostID {
			return true
		}
	}
	return false
}

// EventBusHost 返回部署了消息总线的主机（校验保证恰好一台）。
func (m Manifest) EventBusHost() Host {
	hosts := m.HostsOf("eventbus")
	if len(hosts) == 0 {
		return Host{}
	}
	return m.Hosts[hosts[0]]
}

// EventBusURL 是消息总线的公网地址，例如 tls://106.53.107.122:4222。
func (m Manifest) EventBusURL() string {
	scheme := "nats"
	if m.EventBus.TLSEnabled {
		scheme = "tls"
	}
	return scheme + "://" + net.JoinHostPort(m.EventBusHost().Address, strconv.Itoa(m.EventBus.Port))
}

// Deployment 返回部署表对应的主机与部署（全部启用），用于按组件目录校验，与 SyncHostPlacements 同一套规则。
func (m Manifest) Deployment() servicecatalog.Deployment {
	deployment := servicecatalog.Deployment{}
	for _, host := range m.HostList() {
		deployment.Hosts = append(deployment.Hosts, servicecatalog.Host{
			ID: host.ID, Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Enabled: true,
		})
		for _, component := range m.Placements[host.ID] {
			deployment.Placements = append(deployment.Placements, servicecatalog.Placement{HostID: host.ID, ComponentID: component, Enabled: true})
		}
	}
	return deployment
}

// normalizeTopology 填写主机、编译主机和出口代理的默认值。
func normalizeTopology(m *Manifest, defined func(...string) bool) {
	for id, host := range m.Hosts {
		host.ID = id
		host.Address = strings.TrimSpace(host.Address)
		host.PrivateAddress = strings.TrimSpace(host.PrivateAddress)
		host.Region = strings.TrimSpace(host.Region)
		host.Root = strings.TrimSpace(host.Root)
		if host.Root == "" {
			host.Root = filepath.Join(m.Paths.DeployRoot, id)
		}
		host.Root = filepath.Clean(host.Root)
		host.TLSMode = strings.ToLower(strings.TrimSpace(host.TLSMode))
		host.Provider = strings.ToLower(strings.TrimSpace(host.Provider))
		host.SSH.Username = strings.TrimSpace(host.SSH.Username)
		if host.SSH.Port == 0 {
			host.SSH.Port = 22
		}
		m.Hosts[id] = host
	}
	for id, components := range m.Placements {
		for i := range components {
			components[i] = strings.TrimSpace(components[i])
		}
		m.Placements[id] = components
	}
	m.CompileHost.Address = strings.TrimSpace(m.CompileHost.Address)
	m.CompileHost.Provider = strings.ToLower(strings.TrimSpace(m.CompileHost.Provider))
	m.CompileHost.SSH.Username = strings.TrimSpace(m.CompileHost.SSH.Username)
	if m.CompileHost.Configured() && m.CompileHost.SSH.Port == 0 {
		m.CompileHost.SSH.Port = 22
	}
	dns := &m.EgressProxy.DNS
	defaults := []struct {
		key   string
		value *int
		def   int
	}{
		{"refresh_interval_seconds", &dns.RefreshIntervalSeconds, 300},
		{"request_timeout_ms", &dns.RequestTimeoutMS, 3000},
		{"lookup_timeout_ms", &dns.LookupTimeoutMS, 1500},
		{"probe_timeout_ms", &dns.ProbeTimeoutMS, 500},
		{"probe_port", &dns.ProbePort, 443},
		{"cache_ttl_seconds", &dns.CacheTTLSeconds, 300},
		{"max_ips_per_domain", &dns.MaxIPsPerDomain, 4},
	}
	for _, item := range defaults {
		if !defined("egress_proxy", "dns", item.key) {
			*item.value = item.def
		}
	}
}

// validateTopology 校验主机、部署表、出口代理和编译主机。
func validateTopology(m *Manifest) error {
	if len(m.Hosts) == 0 {
		return fmt.Errorf("config_invalid: 至少需要一台主机：[hosts.control]")
	}
	if _, ok := m.Hosts[servicecatalog.ControlHostID]; !ok {
		return fmt.Errorf("config_invalid: 缺少 control 主机：[hosts.control]")
	}
	catalog := servicecatalog.Default()
	for _, id := range m.HostIDs() {
		if err := validateHostEntry(m.Hosts[id]); err != nil {
			return err
		}
	}
	for hostID, components := range m.Placements {
		if _, ok := m.Hosts[hostID]; !ok {
			return fmt.Errorf("config_invalid: [placements] 中的主机 %s 不在 [hosts] 中", hostID)
		}
		for _, id := range components {
			component, ok := catalog.Component(id)
			if !ok {
				return fmt.Errorf("config_invalid: placements.%s 中的组件 %q 不在组件目录中", hostID, id)
			}
			if component.Scope == servicecatalog.ScopeHost {
				return fmt.Errorf("config_invalid: placements.%s 中的 %s 每台主机自动部署，不要写在部署表里", hostID, id)
			}
		}
	}
	if err := catalog.ValidateDeployment(m.Deployment()); err != nil {
		return fmt.Errorf("config_invalid: 部署表：%w", err)
	}
	if hosts := m.HostsOf("eventbus"); len(hosts) != 1 {
		return fmt.Errorf("config_invalid: 部署表必须恰好有一台主机部署 eventbus")
	}
	for _, id := range m.HostIDs() {
		host := m.Hosts[id]
		if host.TLSMode != "" && !m.HasComponent(id, "console-proxy") {
			return fmt.Errorf("config_invalid: hosts.%s.tls_mode 只能写在部署了 console-proxy 的主机上", id)
		}
		if m.HasComponent(id, "access") && host.Region == "" {
			return fmt.Errorf("config_invalid: hosts.%s 部署了外部接入，必须填写 region（SCF 按地域选择外部接入）", id)
		}
	}
	if err := validateEgressProxy(&m.EgressProxy); err != nil {
		return err
	}
	if m.CompileHost.Configured() {
		if !validEventBusAddress(m.CompileHost.Address) {
			return fmt.Errorf("config_invalid: compile_host.address 必须是 IPv4 地址或域名")
		}
		if err := validateSSH("compile_host", m.CompileHost.SSH, false); err != nil {
			return err
		}
		if m.CompileHost.Provider != "" && !providerPattern.MatchString(m.CompileHost.Provider) {
			return fmt.Errorf("config_invalid: compile_host.provider 只能包含小写字母、数字、连字符和下划线")
		}
	}
	return nil
}

func validateHostEntry(host Host) error {
	path := "hosts." + host.ID
	if !hostIDPattern.MatchString(host.ID) {
		return fmt.Errorf("config_invalid: 主机 ID %q 只能包含小写字母、数字和连字符，且以字母开头", host.ID)
	}
	if !validEventBusAddress(host.Address) {
		return fmt.Errorf("config_invalid: %s.address 必须是 IPv4 地址或域名", path)
	}
	if host.PrivateAddress != "" {
		if ip := net.ParseIP(host.PrivateAddress); ip == nil || ip.To4() == nil {
			return fmt.Errorf("config_invalid: %s.private_address 必须是 IPv4 地址", path)
		}
	}
	if host.Region != "" && !regionPattern.MatchString(host.Region) {
		return fmt.Errorf("config_invalid: %s.region %q 不是有效的地域，例如 ap-nanjing", path, host.Region)
	}
	if !rootPattern.MatchString(host.Root) || host.Root == "/" || strings.Contains(host.Root, "..") {
		return fmt.Errorf("config_invalid: %s.root 必须是不含特殊字符的绝对路径", path)
	}
	switch host.TLSMode {
	case "", "auto", "public", "internal":
	default:
		return fmt.Errorf("config_invalid: %s.tls_mode 只能是 auto、public 或 internal", path)
	}
	if host.Provider != "" && !providerPattern.MatchString(host.Provider) {
		return fmt.Errorf("config_invalid: %s.provider 只能包含小写字母、数字、连字符和下划线", path)
	}
	return validateSSH(path, host.SSH, true)
}

func validateSSH(path string, ssh SSH, requirePassword bool) error {
	if ssh.Port < 1 || ssh.Port > 65535 {
		return fmt.Errorf("config_invalid: %s.ssh.port 必须在 1 到 65535 之间", path)
	}
	if ssh.Username == "" {
		return fmt.Errorf("config_invalid: %s.ssh.username 不能为空", path)
	}
	if requirePassword && ssh.Password == "" {
		return fmt.Errorf("config_invalid: %s.ssh.password 不能为空", path)
	}
	return nil
}

func validateEgressProxy(cfg *EgressProxy) error {
	if _, err := egresspb.ParseDomainList(cfg.HTTPDomains); err != nil {
		return fmt.Errorf("config_invalid: egress_proxy.http_domains：%w", err)
	}
	for i := range cfg.HTTPDomains {
		cfg.HTTPDomains[i] = egresspb.NormalizeHost(cfg.HTTPDomains[i])
	}
	dns := &cfg.DNS
	for name, value := range map[string]int{
		"refresh_interval_seconds": dns.RefreshIntervalSeconds, "request_timeout_ms": dns.RequestTimeoutMS,
		"lookup_timeout_ms": dns.LookupTimeoutMS, "probe_timeout_ms": dns.ProbeTimeoutMS, "cache_ttl_seconds": dns.CacheTTLSeconds,
	} {
		if value <= 0 {
			return fmt.Errorf("config_invalid: egress_proxy.dns.%s 必须大于 0", name)
		}
	}
	if dns.ProbePort < 1 || dns.ProbePort > 65535 {
		return fmt.Errorf("config_invalid: egress_proxy.dns.probe_port 必须在 1 到 65535 之间")
	}
	if dns.MaxIPsPerDomain < 1 || dns.MaxIPsPerDomain > 4 {
		return fmt.Errorf("config_invalid: egress_proxy.dns.max_ips_per_domain 必须在 1 到 4 之间")
	}
	seen := make(map[string]bool, len(dns.Domains))
	for i := range dns.Domains {
		domain := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(dns.Domains[i]), "."))
		if !validDNSResolverDomain(domain) {
			return fmt.Errorf("config_invalid: egress_proxy.dns.domains[%d] 必须是公网域名", i)
		}
		if seen[domain] {
			return fmt.Errorf("config_invalid: egress_proxy.dns.domains 中的 %s 重复", domain)
		}
		seen[domain] = true
		dns.Domains[i] = domain
	}
	if len(dns.Domains) > 16 {
		return fmt.Errorf("config_invalid: egress_proxy.dns.domains 最多 16 个")
	}
	return nil
}

// legacyKeyError 把旧版 moox.toml 的键翻译成迁移提示；不是旧键时返回 nil。
func legacyKeyError(key string) error {
	parts := strings.Split(key, ".")
	top := parts[0]
	switch top {
	case "control_host", "storage_host", "view_host", "strategy_host", "other_hosts":
		return fmt.Errorf("config_invalid: [%s] 已删除：主机改写为 [hosts.<主机 ID>]（address、private_address、region、ssh），"+
			"组件写在 [placements] 中，例如 control = [\"console-proxy\", \"web-host\", \"admin\", ...]", top)
	case "dns_resolver":
		return fmt.Errorf("config_invalid: [dns_resolver] 已改名为 [egress_proxy.dns]，并删除 trade_node；经出口代理访问的域名写在 [egress_proxy] http_domains")
	}
	if len(parts) < 2 {
		return nil
	}
	switch {
	case top == "eventbus" && parts[1] == "host":
		return fmt.Errorf("config_invalid: eventbus.host 已删除：消息总线的地址取自 [placements] 中部署 eventbus 的主机")
	case top == "paths" && (parts[1] == "control_root" || parts[1] == "storage_root"):
		return fmt.Errorf("config_invalid: paths.%s 已删除：改为在 [hosts.<主机 ID>] 中写 root，例如 [hosts.control] root = \"/data/moox/prod\"", parts[1])
	case top == "hosts" && len(parts) >= 3 && (parts[len(parts)-1] == "port" || parts[len(parts)-1] == "username" || parts[len(parts)-1] == "password"):
		return fmt.Errorf("config_invalid: 主机表改为以主机 ID 为键：[hosts.<主机 ID>] address = \"...\"，SSH 写成 ssh = { port = 22, username = \"ubuntu\", password = \"...\" }")
	case top == "compile_host" && (parts[1] == "host" || parts[1] == "name" || parts[1] == "port" || parts[1] == "username" || parts[1] == "password" || parts[1] == "tls_mode"):
		return fmt.Errorf("config_invalid: [compile_host] 改为 address = \"...\" 与 ssh = { port = 22, username = \"...\", password = \"...\" }")
	case top == "scf_fetcher" && len(parts) >= 3:
		switch parts[2] {
		case "collector_rpc_gateway_target", "collector_gateway_target_node", "storage_gateway_node_id", "storage_gateway_host",
			"storage_private_gateway_host", "storage_access_targets", "storage_access_target_nodes":
			return fmt.Errorf("config_invalid: scf_fetcher.spaces.%s 已删除：SCF 按主机的 region 和 private_address 自动选择外部接入", parts[2])
		case "storage_app_id", "storage_app_key", "storage_operator", "storage_request_id":
			return fmt.Errorf("config_invalid: scf_fetcher.spaces.%s 已删除：SCF 用 scf-collector 身份的签名密钥访问外部接入", parts[2])
		}
	}
	return nil
}
