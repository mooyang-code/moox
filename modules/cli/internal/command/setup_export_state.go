package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

// setupState 是 export-state 导出的云上状态，供切换失败时回滚。
type setupState struct {
	ExportedAt string               `json:"exported_at"`
	SCF        []setupSCFState      `json:"scf"`
	Firewall   []setupFirewallState `json:"firewall"`
	Errors     []string             `json:"errors,omitempty"`
}

// setupSCFState 是一个采集函数的状态：版本、网络配置和环境变量。
type setupSCFState struct {
	Region          string            `json:"region"`
	Namespace       string            `json:"namespace"`
	Function        string            `json:"function"`
	Status          string            `json:"status"`
	FunctionVersion string            `json:"function_version"`
	ModTime         string            `json:"mod_time"`
	VpcID           string            `json:"vpc_id"`
	SubnetID        string            `json:"subnet_id"`
	PublicNetStatus string            `json:"public_net_status"`
	Environment     map[string]string `json:"environment"`
}

// setupFirewallState 是一台主机的入站规则：轻量应用服务器的防火墙或云服务器的安全组。
type setupFirewallState struct {
	Host           string                              `json:"host"`
	Address        string                              `json:"address"`
	Kind           string                              `json:"kind"`
	Region         string                              `json:"region"`
	InstanceID     string                              `json:"instance_id,omitempty"`
	FirewallRules  []cloudtencent.FirewallRule         `json:"firewall_rules,omitempty"`
	SecurityGroups []cloudtencent.SecurityGroupIngress `json:"security_group_ingress,omitempty"`
}

func newSetupExportStateCommand(deps setupDeps) *cobra.Command {
	var file, out string
	cmd := &cobra.Command{
		Use:   "export-state",
		Short: "导出采集函数（版本、环境变量、VPC）和主机防火墙规则，供回滚使用",
		Long: `导出 moox.toml 中全部采集函数的当前版本、环境变量与 VPC 配置，以及每台腾讯云主机的入站规则。
文件中含函数环境变量里的密钥，以 0600 权限写入 --out，不输出到终端。`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			out = strings.TrimSpace(out)
			if out == "" {
				out = "moox-state-" + time.Now().Format("20060102-150405") + ".json"
			}
			state, err := exportSetupState(cmd.Context(), snapshot.Manifest)
			if err != nil {
				return err
			}
			raw, err := json.MarshalIndent(state, "", "  ")
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(out, append(raw, '\n'), 0o600); err != nil {
				return err
			}
			status := "ok"
			if len(state.Errors) > 0 {
				status = "partial"
			}
			return writeSetupJSON(cmd, map[string]any{
				"status": status, "out": out, "scf_functions": len(state.SCF), "firewall_hosts": len(state.Firewall), "errors": state.Errors,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&out, "out", "", "输出文件，默认 moox-state-<时间>.json")
	return cmd
}

func exportSetupState(ctx context.Context, manifest setupconfig.Manifest) (setupState, error) {
	state := setupState{ExportedAt: time.Now().Format(time.RFC3339)}
	options := cloudtencent.ClientOptions{
		SecretID: manifest.TencentCloud.SecretID, SecretKey: manifest.TencentCloud.SecretKey, Region: manifest.TencentCloud.Region,
	}
	network, err := cloudtencent.NewNetworkClient(options)
	if err != nil {
		return state, err
	}
	for _, target := range privatenet.CollectSCFTargets(manifest) {
		client := network.ForRegion(target.Region)
		functions, err := client.ListSCFFunctions(ctx, target.Namespace, target.Prefixes)
		if err != nil {
			state.Errors = append(state.Errors, fmt.Sprintf("列出 %s/%s 的函数: %v", target.Region, target.Namespace, err))
			continue
		}
		for _, function := range functions {
			detail, err := client.GetSCFFunction(ctx, function.Namespace, function.FunctionName)
			if err != nil {
				state.Errors = append(state.Errors, fmt.Sprintf("读取函数 %s/%s/%s: %v", target.Region, function.Namespace, function.FunctionName, err))
				continue
			}
			state.SCF = append(state.SCF, setupSCFState{
				Region: target.Region, Namespace: detail.Namespace, Function: detail.FunctionName, Status: detail.Status,
				FunctionVersion: detail.FunctionVersion, ModTime: detail.ModTime, VpcID: detail.VpcID, SubnetID: detail.SubnetID,
				PublicNetStatus: detail.PublicNetStatus, Environment: detail.Environment,
			})
		}
	}
	cloud, err := newSetupFirewallCloud(manifest)
	if err != nil {
		return state, err
	}
	addresses, err := setupFirewallAddresses(ctx, manifest)
	if err != nil {
		return state, err
	}
	for _, host := range manifest.HostList() {
		address := addresses[host.ID]
		if host.Provider != "tencent" || !isPublicFirewallIP(address) {
			continue
		}
		item, err := exportFirewallState(ctx, cloud, host.ID, address)
		if err != nil {
			state.Errors = append(state.Errors, fmt.Sprintf("读取主机 %s 的入站规则: %v", host.ID, err))
			continue
		}
		state.Firewall = append(state.Firewall, item)
	}
	return state, nil
}

func exportFirewallState(ctx context.Context, cloud *setupFirewallCloud, hostID, address string) (setupFirewallState, error) {
	instance, err := cloud.locate(ctx, address)
	if err != nil {
		return setupFirewallState{}, err
	}
	item := setupFirewallState{Host: hostID, Address: address, Kind: instance.Kind, Region: instance.Region}
	switch instance.Kind {
	case cloudtencent.KindLighthouse:
		item.InstanceID, item.FirewallRules, err = cloud.lighthouse.ForRegion(instance.Region).ListFirewallRules(ctx, address)
	case cloudtencent.KindCVM:
		var client *cloudtencent.CVMClient
		if client, err = cloud.cvm(instance.Region); err == nil {
			item.InstanceID = instance.InstanceID
			item.SecurityGroups, err = client.ListSecurityGroupIngress(ctx, address)
		}
	default:
		err = fmt.Errorf("不支持的实例类型 %q", instance.Kind)
	}
	return item, err
}
