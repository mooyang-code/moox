package command

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/tencenthost"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

// setupFirewallSummary is intentionally small and secret-free so setup output
// can be persisted by CI without exposing Tencent credentials.
type setupFirewallSummary struct {
	Status      string `json:"status"`
	Targets     int    `json:"targets"`
	Rules       int    `json:"rules"`
	Skipped     int    `json:"skipped"`
	AlreadyOpen int    `json:"already_open"`
	Created     int    `json:"created"`
}

type setupFirewallTarget struct {
	Host  setupconfig.Host
	Rules []cloudtencent.CreateFirewallRulesOptions
}

func newSetupFirewallCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "firewall",
		Short: "初始化所有目标主机的腾讯云防火墙端口",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			result, err := deps.ensureFirewall(cmd.Context(), snapshot)
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
	return cmd
}

// defaultSetupEnsureFirewall opens only ports used by the deployed runtime.
// It is idempotent and is safe to run before or after any service deployment.
func defaultSetupEnsureFirewall(ctx context.Context, snapshot *setupconfig.Snapshot) (setupFirewallSummary, error) {
	if snapshot == nil {
		return setupFirewallSummary{}, fmt.Errorf("firewall: setup configuration is missing")
	}
	return ensureFirewallTargets(ctx, snapshot.Manifest, setupFirewallTargets(snapshot.Manifest))
}

// setupEnsureRelayFirewall 在部署只能出站的主机之前，经腾讯云接口确认 control 的跨主机端口（反向连接）
// 与 EventBus 主机上的 EventBus 端口（出站主机的 HostAgent 上报）对公网开放；出站主机的出口 IP 会变，只能按 0.0.0.0/0 放行，
// 认证由 TLS 与签名完成。任何一条规则无法确认都返回错误，不让部署在网络不通的情况下继续。
func setupEnsureRelayFirewall(ctx context.Context, snapshot *setupconfig.Snapshot) (setupFirewallSummary, error) {
	if snapshot == nil {
		return setupFirewallSummary{}, fmt.Errorf("firewall: setup configuration is missing")
	}
	summary, err := ensureFirewallTargets(ctx, snapshot.Manifest, setupRelayFirewallTargets(snapshot.Manifest))
	if err != nil {
		return summary, err
	}
	if summary.Status != "ready" {
		return summary, fmt.Errorf("firewall: could not confirm control port %d and EventBus port %d are open for outbound hosts; check the Tencent Cloud credentials and the control and EventBus instances", servicecatalog.HostGatewayRemotePort, snapshot.Manifest.EventBus.Port)
	}
	return summary, nil
}

// setupRelayFirewallTargets 是出站主机接入需要的端口：control 的网关端口，和 EventBus 所在主机的 EventBus 端口。
// 两者是同一台主机时合并成一个目标。
func setupRelayFirewallTargets(manifest setupconfig.Manifest) []setupFirewallTarget {
	gateway := cloudtencent.CreateFirewallRulesOptions{Protocol: "TCP", Ports: fmt.Sprint(servicecatalog.HostGatewayRemotePort), CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX service gateway native"}
	control, bus := manifest.ControlHost(), manifest.EventBusHost()
	targets := []setupFirewallTarget{{Host: control, Rules: []cloudtencent.CreateFirewallRulesOptions{gateway}}}
	busRule := setupEventBusFirewallRule(manifest.EventBus.Port)
	if bus.Name == control.Name {
		targets[0].Rules = append(targets[0].Rules, busRule)
		return targets
	}
	return append(targets, setupFirewallTarget{Host: bus, Rules: []cloudtencent.CreateFirewallRulesOptions{busRule}})
}

func ensureFirewallTargets(ctx context.Context, manifest setupconfig.Manifest, targets []setupFirewallTarget) (setupFirewallSummary, error) {
	tencent, err := cloudtencent.NewClient(cloudtencent.ClientOptions{
		SecretID:  manifest.TencentCloud.SecretID,
		SecretKey: manifest.TencentCloud.SecretKey,
		Region:    manifest.TencentCloud.Region,
	})
	if err != nil {
		return setupFirewallSummary{}, fmt.Errorf("firewall: %w", err)
	}
	summary := setupFirewallSummary{Status: "ready", Targets: len(targets)}
	for _, target := range targets {
		address, err := setupFirewallAddress(ctx, target.Host.Address)
		if err != nil {
			return summary, fmt.Errorf("firewall %s: resolve address: %w", target.Host.Name, err)
		}
		if !isPublicFirewallIP(address) {
			summary.Skipped++
			continue
		}
		for _, rule := range target.Rules {
			result, err := tencent.EnsureFirewallRule(ctx, address, rule)
			if err != nil {
				// The manifest home region need not be the region of every
				// configured host. Resolve the actual Tencent region before
				// falling back to a CVM security group or retrying Lighthouse.
				created, fallbackErr := ensureFirewallRuleAtDiscoveredRegion(ctx, manifest, address, rule)
				if fallbackErr == nil {
					summary.Rules++
					if created {
						summary.Created++
					} else {
						summary.AlreadyOpen++
					}
					continue
				}
				fmt.Fprintf(os.Stderr, "firewall %s port %s skipped: lighthouse=%v; discovered=%v\n", target.Host.Name, rule.Ports, err, fallbackErr)
				summary.Skipped++
				continue
			}
			summary.Rules++
			if result.Created {
				summary.Created++
			} else {
				summary.AlreadyOpen++
			}
		}
	}
	if summary.Skipped > 0 {
		// A skipped target is not equivalent to an already-open rule. Keep the
		// command idempotent, but make partial cloud-account/region resolution
		// explicit to callers and initialization reports.
		summary.Status = "partial"
	}
	return summary, nil
}

// ensureFirewallRuleAtDiscoveredRegion handles hosts placed outside the
// manifest's home region. Lighthouse rules and CVM security-group rules both
// require the actual region in their signed request, so retrying with the
// home-region client can never succeed for those hosts.
func ensureFirewallRuleAtDiscoveredRegion(ctx context.Context, manifest setupconfig.Manifest, address string, rule cloudtencent.CreateFirewallRulesOptions) (bool, error) {
	network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
		SecretID: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey,
		Region: manifest.TencentCloud.Region,
	})
	if err != nil {
		return false, err
	}
	lighthouse, err := cloudtencent.NewClient(cloudtencent.ClientOptions{
		SecretID: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey,
		Region: manifest.TencentCloud.Region,
	})
	if err != nil {
		return false, err
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	instance, err := (tencenthost.TencentCloud{Network: network, Lighthouse: lighthouse}).LookupHost(
		lookupCtx, address, cloudtencent.ProbeRegions(manifest.TencentCloud.Region),
	)
	if err != nil {
		return false, err
	}
	switch instance.Kind {
	case cloudtencent.KindLighthouse:
		result, err := lighthouse.ForRegion(instance.Region).EnsureFirewallRule(ctx, address, rule)
		return result.Created, err
	case cloudtencent.KindCVM:
		cvm, err := cloudtencent.NewCVMClient(cloudtencent.ClientOptions{
			SecretID: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey,
			Region: instance.Region,
		})
		if err != nil {
			return false, err
		}
		return false, cvm.EnsureSecurityGroupRule(ctx, address, rule)
	default:
		return false, fmt.Errorf("unsupported Tencent instance kind %q", instance.Kind)
	}
}

func setupFirewallAddress(ctx context.Context, address string) (string, error) {
	address = strings.TrimSpace(address)
	if ip := net.ParseIP(address); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
		return "", fmt.Errorf("IPv4 address is required")
	}
	return eventBusFirewallIP(ctx, address, net.DefaultResolver.LookupIP)
}

func setupFirewallTargets(manifest setupconfig.Manifest) []setupFirewallTarget {
	byAddress := make(map[string]int)
	targets := make([]setupFirewallTarget, 0, len(manifest.Hosts()))
	add := func(host setupconfig.Host, rules []cloudtencent.CreateFirewallRulesOptions) {
		// 只能出站的主机不接受入站连接，它的地址也不是可放行的公网地址。
		if strings.TrimSpace(host.Address) == "" || len(rules) == 0 || host.Reachability == servicecatalog.ReachabilityOutbound {
			return
		}
		key := strings.ToLower(strings.TrimSpace(host.Address))
		if index, ok := byAddress[key]; ok {
			targets[index].Rules = appendUniqueFirewallRules(targets[index].Rules, rules...)
			return
		}
		byAddress[key] = len(targets)
		targets = append(targets, setupFirewallTarget{Host: host, Rules: append([]cloudtencent.CreateFirewallRulesOptions(nil), rules...)})
	}

	controlRules := append([]cloudtencent.CreateFirewallRulesOptions(nil), setupControlFirewallRulesForTLS(
		tlstrust.TLSMode(manifest.ControlHost().TLSMode), manifest.ControlHost().Address,
	)...)
	controlRules = appendUniqueFirewallRules(controlRules, setupRuntimeFirewallRules()...)
	add(manifest.ControlHost(), controlRules)
	// EventBus 端口只在 EventBus 所在的主机上放行（与控制机同机时由 add 按地址合并）。
	if setupconfigHostConfigured(manifest.EventBusHost()) {
		add(manifest.EventBusHost(), []cloudtencent.CreateFirewallRulesOptions{setupEventBusFirewallRule(manifest.EventBus.Port)})
	}

	if setupconfigHostConfigured(manifest.StorageHost()) {
		add(manifest.StorageHost(), []cloudtencent.CreateFirewallRulesOptions{{Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX remote Storage native gateway"}})
	}
	if setupconfigHostConfigured(manifest.ViewHost()) {
		add(manifest.ViewHost(), []cloudtencent.CreateFirewallRulesOptions{{Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX remote Storage native gateway"}})
	}
	tradeNode := strings.TrimSpace(manifest.ComponentHost("trade"))
	for _, host := range manifest.Hosts() {
		if strings.EqualFold(strings.TrimSpace(host.Name), tradeNode) {
			add(host, []cloudtencent.CreateFirewallRulesOptions{
				{Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX service gateway native"},
				{Protocol: "TCP", Ports: "11200", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX Trade console"},
			})
		}
	}
	for _, host := range manifest.Hosts() {
		add(host, healthProbeFirewallRules(manifest, host))
	}
	return targets
}

// healthProbeFirewallRules 是 Monitor（在控制机上）探测其他直连主机上组件健康端口需要的规则：只放行控制机地址。
// 健康端口本身还有签名鉴权；控制机自己的组件由本机探测，不需要放行。
func healthProbeFirewallRules(manifest setupconfig.Manifest, host setupconfig.Host) []cloudtencent.CreateFirewallRulesOptions {
	control := manifest.ControlHost()
	if strings.EqualFold(strings.TrimSpace(host.Name), strings.TrimSpace(control.Name)) || !isPublicFirewallIP(control.Address) {
		return nil
	}
	// 与控制机同一地域且有私网地址的主机，Monitor 走私网探测，不经过公网防火墙（与 Monitor 部署同步的规则一致）。
	if host.PrivateAddress != "" && host.Region != "" && host.Region == control.Region {
		return nil
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil
	}
	var rules []cloudtencent.CreateFirewallRulesOptions
	for _, id := range manifest.Deployments[host.Name] {
		component, ok := catalog.Component(id)
		if !ok || component.Health.Kind != "readyz" || component.Health.Port <= 0 || component.Health.Loopback {
			continue
		}
		rules = append(rules, cloudtencent.CreateFirewallRulesOptions{
			Protocol: "TCP", Ports: fmt.Sprint(component.Health.Port), CidrBlock: control.Address + "/32",
			Action: "ACCEPT", Description: "MooX Monitor health probe " + id,
		})
	}
	return rules
}

func setupconfigHostConfigured(host setupconfig.Host) bool {
	return strings.TrimSpace(host.Name) != "" && strings.TrimSpace(host.Address) != ""
}

func appendUniqueFirewallRules(dst []cloudtencent.CreateFirewallRulesOptions, rules ...cloudtencent.CreateFirewallRulesOptions) []cloudtencent.CreateFirewallRulesOptions {
	for _, rule := range rules {
		duplicate := false
		for _, existing := range dst {
			if strings.EqualFold(existing.Protocol, rule.Protocol) && existing.Ports == rule.Ports && existing.CidrBlock == rule.CidrBlock && strings.EqualFold(existing.Action, rule.Action) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			dst = append(dst, rule)
		}
	}
	return dst
}
