package command

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
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
	tencent, err := cloudtencent.NewClient(cloudtencent.ClientOptions{
		SecretID:  snapshot.Manifest.TencentCloud.SecretID,
		SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region:    snapshot.Manifest.TencentCloud.Region,
	})
	if err != nil {
		return setupFirewallSummary{}, fmt.Errorf("firewall: %w", err)
	}

	targets := setupFirewallTargets(snapshot.Manifest)
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
				created, fallbackErr := ensureFirewallRuleAtDiscoveredRegion(ctx, snapshot.Manifest, address, rule)
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
	instance, err := (privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}).LookupHost(
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
		if strings.TrimSpace(host.Address) == "" || len(rules) == 0 {
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
		setupdeploy.TLSMode(manifest.ControlHost.TLSMode), manifest.ControlHost.Address,
	)...)
	controlRules = appendUniqueFirewallRules(controlRules, setupRuntimeFirewallRules(manifest.EventBus.Port)...)
	add(manifest.ControlHost, controlRules)

	if setupconfigHostConfigured(manifest.StorageHost) {
		add(manifest.StorageHost, []cloudtencent.CreateFirewallRulesOptions{{Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX remote Storage native gateway"}})
	}
	if setupconfigHostConfigured(manifest.ViewHost) {
		add(manifest.ViewHost, []cloudtencent.CreateFirewallRulesOptions{{Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX remote Storage native gateway"}})
	}
	tradeNode := strings.TrimSpace(manifest.DNSResolver.TradeNode)
	for _, host := range manifest.Hosts() {
		if strings.EqualFold(strings.TrimSpace(host.Name), tradeNode) {
			add(host, []cloudtencent.CreateFirewallRulesOptions{
				{Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX service gateway native"},
				{Protocol: "TCP", Ports: "11200", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX Trade console"},
			})
		}
	}
	return targets
}

func setupconfigHostConfigured(host setupconfig.Host) bool {
	return strings.TrimSpace(host.Name) != "" && strings.TrimSpace(host.Address) != ""
}

func isStorageFirewallTarget(manifest setupconfig.Manifest, host setupconfig.Host) bool {
	return strings.EqualFold(strings.TrimSpace(host.Address), strings.TrimSpace(manifest.StorageHost.Address)) ||
		strings.EqualFold(strings.TrimSpace(host.Address), strings.TrimSpace(manifest.ViewHost.Address))
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
