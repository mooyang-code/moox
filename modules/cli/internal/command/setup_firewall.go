package command

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/release"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

// setupFirewallSummary 是防火墙同步的结果，不含任何凭据。
type setupFirewallSummary struct {
	Status      string              `json:"status"`
	Targets     int                 `json:"targets"`
	Rules       int                 `json:"rules"`
	Skipped     int                 `json:"skipped"`
	AlreadyOpen int                 `json:"already_open"`
	Created     int                 `json:"created"`
	Deleted     int                 `json:"deleted"`
	Hosts       []setupFirewallHost `json:"hosts,omitempty"`
	DryRun      bool                `json:"dry_run,omitempty"`
	Prune       bool                `json:"prune,omitempty"`
}

// setupFirewallHost 是一台主机的防火墙变更。
type setupFirewallHost struct {
	Host     string               `json:"host"`
	Address  string               `json:"address"`
	Kind     string               `json:"kind,omitempty"`
	Region   string               `json:"region,omitempty"`
	Created  []setupFirewallEntry `json:"created,omitempty"`
	Deleted  []setupFirewallEntry `json:"deleted,omitempty"`
	Obsolete []setupFirewallEntry `json:"obsolete,omitempty"`
	Skipped  string               `json:"skipped,omitempty"`
}

// setupFirewallEntry 是一条入站规则：TCP 端口与来源。
type setupFirewallEntry struct {
	Port        string `json:"port"`
	Source      string `json:"source"`
	Description string `json:"description,omitempty"`
}

type setupFirewallOptions struct {
	// Prune 时删除受管端口上不在期望规则中的旧规则（例如 11001、12004，以及对公网开放的 11003）。
	Prune  bool
	DryRun bool
}

func newSetupFirewallCommand(deps setupDeps) *cobra.Command {
	var file string
	var opts setupFirewallOptions
	cmd := &cobra.Command{
		Use:   "firewall",
		Short: "按部署表同步腾讯云主机的防火墙入站规则",
		Long: `按 moox.toml 的部署表同步每台腾讯云主机的入站规则：
  - 控制台代理所在主机：9527 对公网开放；证书方式为 public 时 80 也对公网开放；
  - 每台主机：跨主机入口 11003 只对其他 MooX 主机开放；
  - 部署了外部接入的主机：11004 对公网开放；
  - 消息总线所在主机：消息总线端口对公网开放（SCF 采集函数直接发布事件）；
  - control 以外的主机：组件健康端口（含主机网关的 11012）只对 control 开放。
默认只补齐缺少的规则；--prune 同时删除受管端口上多余的规则（例如旧的 11001、12004 和对公网开放的 11003）。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			result, err := runSetupFirewall(cmd.Context(), snapshot, opts)
			if err != nil {
				return err
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, result)
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().BoolVar(&opts.Prune, "prune", false, "删除受管端口上多余的旧规则")
	cmd.Flags().BoolVar(&opts.DryRun, "dry-run", false, "只列出要新增和删除的规则，不修改")
	return cmd
}

// defaultSetupEnsureFirewall 补齐部署表需要的入站规则，不删除任何规则；setup init 使用。
func defaultSetupEnsureFirewall(ctx context.Context, snapshot *setupconfig.Snapshot) (setupFirewallSummary, error) {
	return runSetupFirewall(ctx, snapshot, setupFirewallOptions{})
}

func runSetupFirewall(ctx context.Context, snapshot *setupconfig.Snapshot, opts setupFirewallOptions) (setupFirewallSummary, error) {
	if snapshot == nil {
		return setupFirewallSummary{}, fmt.Errorf("firewall: 缺少 moox.toml")
	}
	manifest := snapshot.Manifest
	addresses, err := setupFirewallAddresses(ctx, manifest)
	if err != nil {
		return setupFirewallSummary{}, err
	}
	cloud, err := newSetupFirewallCloud(manifest)
	if err != nil {
		return setupFirewallSummary{}, fmt.Errorf("firewall: %w", err)
	}
	desired := setupFirewallRules(manifest, addresses)
	managed := setupFirewallManagedPorts(manifest)
	summary := setupFirewallSummary{Status: "ready", DryRun: opts.DryRun, Prune: opts.Prune}
	for _, host := range manifest.HostList() {
		item := setupFirewallHost{Host: host.ID, Address: addresses[host.ID]}
		switch {
		case host.Provider != "tencent":
			item.Skipped = "不是腾讯云主机"
		case !isPublicFirewallIP(addresses[host.ID]):
			item.Skipped = "不是公网 IPv4 地址"
		}
		if item.Skipped != "" {
			summary.Skipped++
			summary.Hosts = append(summary.Hosts, item)
			continue
		}
		summary.Targets++
		if err := cloud.sync(ctx, &item, desired[host.ID], managed, opts); err != nil {
			item.Skipped = err.Error()
			summary.Skipped++
		}
		summary.Rules += len(desired[host.ID])
		summary.Created += len(item.Created)
		summary.Deleted += len(item.Deleted)
		summary.AlreadyOpen += len(desired[host.ID]) - len(item.Created)
		summary.Hosts = append(summary.Hosts, item)
	}
	if summary.Skipped > 0 {
		summary.Status = "partial"
	}
	return summary, nil
}

// setupFirewallAddresses 把每台主机的地址解析为 IPv4。
func setupFirewallAddresses(ctx context.Context, manifest setupconfig.Manifest) (map[string]string, error) {
	out := map[string]string{}
	for _, host := range manifest.HostList() {
		address, err := setupFirewallAddress(ctx, host.Address)
		if err != nil {
			return nil, fmt.Errorf("firewall: 解析主机 %s 的地址: %w", host.ID, err)
		}
		out[host.ID] = address
	}
	return out, nil
}

// setupFirewallRules 按部署表计算每台主机期望的入站规则。
func setupFirewallRules(manifest setupconfig.Manifest, addresses map[string]string) map[string][]setupFirewallEntry {
	catalog := servicecatalog.Default()
	controlAddress := addresses[servicecatalog.ControlHostID]
	out := map[string][]setupFirewallEntry{}
	add := func(hostID string, entry setupFirewallEntry) {
		for _, existing := range out[hostID] {
			if existing.Port == entry.Port && existing.Source == entry.Source {
				return
			}
		}
		out[hostID] = append(out[hostID], entry)
	}
	fromOtherHosts := func(hostID, port, description string) {
		for _, other := range manifest.HostIDs() {
			if other != hostID && addresses[other] != "" {
				add(hostID, setupFirewallEntry{Port: port, Source: addresses[other], Description: description + " from " + other})
			}
		}
	}
	for _, host := range manifest.HostList() {
		fromOtherHosts(host.ID, "11003", "MooX host gateway")
		if manifest.HasComponent(host.ID, "console-proxy") {
			add(host.ID, setupFirewallEntry{Port: "9527", Source: "0.0.0.0/0", Description: "MooX console HTTPS"})
			if release.ResolveTLSMode(host.TLSMode, host.Address) == release.TLSModePublic {
				add(host.ID, setupFirewallEntry{Port: "80", Source: "0.0.0.0/0", Description: "MooX ACME HTTP challenge"})
			}
		}
		if manifest.HasComponent(host.ID, "access") {
			add(host.ID, setupFirewallEntry{Port: "11004", Source: "0.0.0.0/0", Description: "MooX access"})
		}
		if manifest.HasComponent(host.ID, "eventbus") {
			// SCF 采集函数直接向消息总线发布事件，消息总线端口保持对公网开放（TLS + 账号口令）。
			add(host.ID, setupFirewallEntry{Port: strconv.Itoa(manifest.EventBus.Port), Source: "0.0.0.0/0", Description: "MooX EventBus TLS"})
		}
		if host.ID == servicecatalog.ControlHostID || controlAddress == "" {
			continue
		}
		components := append(catalog.HostComponents(), manifest.Components(host.ID)...)
		for _, id := range components {
			component, ok := catalog.Component(id)
			if !ok || component.Health.Port <= 0 {
				continue
			}
			add(host.ID, setupFirewallEntry{
				Port: strconv.Itoa(component.Health.Port), Source: controlAddress, Description: "MooX health " + id + " from control",
			})
		}
	}
	for hostID := range out {
		sort.Slice(out[hostID], func(i, j int) bool {
			left, right := out[hostID][i], out[hostID][j]
			if left.Port != right.Port {
				return left.Port < right.Port
			}
			return left.Source < right.Source
		})
	}
	return out
}

// setupFirewallManagedPorts 是 MooX 管理的端口：--prune 只删除这些端口上的多余规则，其余端口（例如 SSH）不动。
// 其中 11001、11200、12004 是旧版本使用的端口，现在不再开放。
func setupFirewallManagedPorts(manifest setupconfig.Manifest) map[string]bool {
	ports := map[string]bool{
		"80": true, "9527": true, "11001": true, "11003": true, "11004": true, "11012": true, "11200": true, "12004": true,
		strconv.Itoa(manifest.EventBus.Port): true,
	}
	for _, component := range servicecatalog.Default().Components {
		if component.Health.Port > 0 {
			ports[strconv.Itoa(component.Health.Port)] = true
		}
	}
	return ports
}

func setupFirewallAddress(ctx context.Context, address string) (string, error) {
	address = strings.TrimSpace(address)
	if ip := net.ParseIP(address); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
		return "", fmt.Errorf("需要 IPv4 地址")
	}
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", address)
	if err != nil {
		return "", err
	}
	unique := map[string]bool{}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			unique[v4.String()] = true
		}
	}
	if len(unique) != 1 {
		return "", fmt.Errorf("域名 %s 必须恰好解析到一个 IPv4 地址", address)
	}
	for ip := range unique {
		return ip, nil
	}
	return "", fmt.Errorf("域名 %s 没有 IPv4 地址", address)
}

func isPublicFirewallIP(address string) bool {
	ip := net.ParseIP(strings.TrimSpace(address))
	return ip != nil && ip.To4() != nil && ip.IsGlobalUnicast() &&
		!ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsMulticast()
}

// normalizeFirewallSource 统一来源的写法：单个 IP 的 /32 写成 IP 本身。
func normalizeFirewallSource(source string) string {
	return strings.TrimSuffix(strings.TrimSpace(source), "/32")
}

// setupFirewallCloud 读写腾讯云主机的入站规则：轻量应用服务器用防火墙，云服务器用安全组。
type setupFirewallCloud struct {
	manifest   setupconfig.Manifest
	lighthouse *cloudtencent.Client
	locator    privatenet.TencentCloud
}

func newSetupFirewallCloud(manifest setupconfig.Manifest) (*setupFirewallCloud, error) {
	options := cloudtencent.ClientOptions{
		SecretID: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey, Region: manifest.TencentCloud.Region,
	}
	network, err := cloudtencent.NewNetworkClient(options)
	if err != nil {
		return nil, err
	}
	lighthouse, err := cloudtencent.NewClient(options)
	if err != nil {
		return nil, err
	}
	return &setupFirewallCloud{manifest: manifest, lighthouse: lighthouse, locator: privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}}, nil
}

// locate 查找主机对应的腾讯云实例（类型与地域）。
func (c *setupFirewallCloud) locate(ctx context.Context, address string) (cloudtencent.CloudInstance, error) {
	lookupCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return c.locator.LookupHost(lookupCtx, address, cloudtencent.ProbeRegions(c.manifest.TencentCloud.Region))
}

func (c *setupFirewallCloud) cvm(region string) (*cloudtencent.CVMClient, error) {
	return cloudtencent.NewCVMClient(cloudtencent.ClientOptions{
		SecretID: c.manifest.TencentCloud.SecretID, SecretKey: c.manifest.TencentCloud.SecretKey, Region: region,
	})
}

func (c *setupFirewallCloud) sync(ctx context.Context, item *setupFirewallHost, desired []setupFirewallEntry, managed map[string]bool, opts setupFirewallOptions) error {
	instance, err := c.locate(ctx, item.Address)
	if err != nil {
		return fmt.Errorf("查找腾讯云实例: %w", err)
	}
	item.Kind, item.Region = instance.Kind, instance.Region
	switch instance.Kind {
	case cloudtencent.KindLighthouse:
		return c.syncLighthouse(ctx, item, instance, desired, managed, opts)
	case cloudtencent.KindCVM:
		return c.syncCVM(ctx, item, instance, desired, managed, opts)
	default:
		return fmt.Errorf("不支持的实例类型 %q", instance.Kind)
	}
}

func (c *setupFirewallCloud) syncLighthouse(ctx context.Context, item *setupFirewallHost, instance cloudtencent.CloudInstance, desired []setupFirewallEntry, managed map[string]bool, opts setupFirewallOptions) error {
	client := c.lighthouse.ForRegion(instance.Region)
	instanceID, rules, err := client.ListFirewallRules(ctx, item.Address)
	if err != nil {
		return fmt.Errorf("读取防火墙规则: %w", err)
	}
	existing := make([]setupFirewallEntry, 0, len(rules))
	for _, rule := range rules {
		if strings.EqualFold(rule.Protocol, "TCP") && strings.EqualFold(rule.Action, "ACCEPT") {
			existing = append(existing, setupFirewallEntry{Port: rule.Port, Source: normalizeFirewallSource(rule.CidrBlock), Description: rule.FirewallRuleDescription})
		}
	}
	missing, obsolete := diffFirewallEntries(desired, existing, managed)
	item.Obsolete = obsolete
	if opts.DryRun {
		item.Created = missing
		if opts.Prune {
			item.Deleted = obsolete
		}
		return nil
	}
	if len(missing) > 0 {
		request := cloudtencent.CreateFirewallRulesRequest{InstanceID: instanceID}
		for _, entry := range missing {
			one, err := cloudtencent.NewCreateFirewallRulesRequest(firewallRuleOptions(instanceID, entry))
			if err != nil {
				return err
			}
			request.FirewallRules = append(request.FirewallRules, one.FirewallRules...)
		}
		if _, err := client.CreateFirewallRules(ctx, request); err != nil {
			return fmt.Errorf("新增防火墙规则: %w", err)
		}
		item.Created = missing
	}
	if opts.Prune && len(obsolete) > 0 {
		var remove []cloudtencent.FirewallRule
		for _, rule := range rules {
			entry := setupFirewallEntry{Port: rule.Port, Source: normalizeFirewallSource(rule.CidrBlock)}
			if strings.EqualFold(rule.Protocol, "TCP") && strings.EqualFold(rule.Action, "ACCEPT") && containsFirewallEntry(obsolete, entry) {
				remove = append(remove, rule)
			}
		}
		if err := client.DeleteFirewallRules(ctx, instanceID, remove); err != nil {
			return fmt.Errorf("删除防火墙规则: %w", err)
		}
		item.Deleted = obsolete
	}
	return nil
}

func (c *setupFirewallCloud) syncCVM(ctx context.Context, item *setupFirewallHost, instance cloudtencent.CloudInstance, desired []setupFirewallEntry, managed map[string]bool, opts setupFirewallOptions) error {
	client, err := c.cvm(instance.Region)
	if err != nil {
		return err
	}
	policies, err := client.ListSecurityGroupIngress(ctx, item.Address)
	if err != nil {
		return fmt.Errorf("读取安全组规则: %w", err)
	}
	existing := make([]setupFirewallEntry, 0, len(policies))
	for _, policy := range policies {
		if strings.EqualFold(policy.Protocol, "TCP") && strings.EqualFold(policy.Action, "ACCEPT") {
			existing = append(existing, setupFirewallEntry{Port: policy.Port, Source: normalizeFirewallSource(policy.CidrBlock), Description: policy.Description})
		}
	}
	missing, obsolete := diffFirewallEntries(desired, existing, managed)
	item.Obsolete = obsolete
	if opts.DryRun {
		item.Created = missing
		if opts.Prune {
			item.Deleted = obsolete
		}
		return nil
	}
	for _, entry := range missing {
		if err := client.EnsureSecurityGroupRule(ctx, item.Address, firewallRuleOptions("", entry)); err != nil {
			return fmt.Errorf("新增安全组规则: %w", err)
		}
		item.Created = append(item.Created, entry)
	}
	if opts.Prune && len(obsolete) > 0 {
		// 新增规则之后重新读取：规则序号会随新增、别人的修改而变化，按新增之前读到的序号删除可能删错规则。
		latest, err := client.ListSecurityGroupIngress(ctx, item.Address)
		if err != nil {
			return fmt.Errorf("删除前重新读取安全组规则: %w", err)
		}
		type group struct {
			version string
			indexes []int64
		}
		byGroup := map[string]*group{}
		for _, policy := range latest {
			entry := setupFirewallEntry{Port: policy.Port, Source: normalizeFirewallSource(policy.CidrBlock)}
			if strings.EqualFold(policy.Protocol, "TCP") && strings.EqualFold(policy.Action, "ACCEPT") && containsFirewallEntry(obsolete, entry) {
				if byGroup[policy.SecurityGroupID] == nil {
					byGroup[policy.SecurityGroupID] = &group{version: policy.PolicyVersion}
				}
				byGroup[policy.SecurityGroupID].indexes = append(byGroup[policy.SecurityGroupID].indexes, policy.PolicyIndex)
			}
		}
		for groupID, target := range byGroup {
			if err := client.DeleteSecurityGroupIngress(ctx, groupID, target.version, target.indexes); err != nil {
				return fmt.Errorf("删除安全组规则: %w", err)
			}
		}
		item.Deleted = obsolete
	}
	return nil
}

// diffFirewallEntries 返回缺少的期望规则，以及受管端口上多余的规则。
func diffFirewallEntries(desired, existing []setupFirewallEntry, managed map[string]bool) ([]setupFirewallEntry, []setupFirewallEntry) {
	var missing, obsolete []setupFirewallEntry
	for _, entry := range desired {
		if !containsFirewallEntry(existing, entry) {
			missing = append(missing, entry)
		}
	}
	for _, entry := range existing {
		if managed[entry.Port] && !containsFirewallEntry(desired, entry) && !containsFirewallEntry(obsolete, entry) {
			obsolete = append(obsolete, entry)
		}
	}
	return missing, obsolete
}

func containsFirewallEntry(entries []setupFirewallEntry, want setupFirewallEntry) bool {
	for _, entry := range entries {
		if entry.Port == want.Port && normalizeFirewallSource(entry.Source) == normalizeFirewallSource(want.Source) {
			return true
		}
	}
	return false
}

func firewallRuleOptions(instanceID string, entry setupFirewallEntry) cloudtencent.CreateFirewallRulesOptions {
	description := entry.Description
	if len([]rune(description)) > 64 {
		description = string([]rune(description)[:64])
	}
	return cloudtencent.CreateFirewallRulesOptions{
		InstanceID: instanceID, Protocol: "TCP", Ports: entry.Port, CidrBlock: entry.Source,
		Action: "ACCEPT", Description: description,
	}
}
