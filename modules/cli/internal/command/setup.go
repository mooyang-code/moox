package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitdeploy"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/mooyang-code/moox/modules/cli/internal/tencenthost"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

const defaultSetupFile = "./moox.toml"

type setupDeps struct {
	bootstrapCore         setupCoreBootstrap
	bootstrapControl      setupControlBootstrap
	deployHost            setupHostDeploy
	deployUnit            setupUnitDeploy
	buildRelease          setupReleaseBuild
	load                  func(string) (*setupconfig.Snapshot, error)
	loadInitBundle        func(string) (setupInitBundle, error)
	validate              func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error)
	trustHost             func(context.Context, *setupconfig.Snapshot, string, string) error
	apply                 func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error)
	status                func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error)
	applySpaces           func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.ApplyResult, error)
	statusSpaces          func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.StatusResult, error)
	registerCloudAccounts func(context.Context, *setupconfig.Snapshot) (*setupCloudAccountSummary, error)
	provisionCLS          func(context.Context, *setupconfig.Snapshot) (*setupCLSSummary, error)
	ensureSCFPlan         func(context.Context, *setupconfig.Snapshot) (*setupSCFPlanSummary, error)
	login                 func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error)
	openInitStorage       func(context.Context, *setupconfig.Snapshot, string) (setupInitStorage, error)
	openInitFactor        func(context.Context, *setupconfig.Snapshot) (setupInitFactor, error)
	initStorage           func(context.Context, *setupconfig.Snapshot, string, string, string, setupInitBundle) (setupInitSummary, error)
	installWatchdog       func(context.Context, *setupconfig.Snapshot, setupconfig.Host, bool) (json.RawMessage, error)
	importMetadata        func(context.Context, *setupconfig.Snapshot, string, string, []string) (metadataImportSummary, error)
	exportSkillConfig     func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error)
	ensureFirewall        func(context.Context, *setupconfig.Snapshot) (setupFirewallSummary, error)
	ensureRelayFirewall   func(context.Context, *setupconfig.Snapshot) (setupFirewallSummary, error)
	// scanDeployment 检测目标机器的部署情况；测试里为空时跳过检测。
	scanDeployment func(context.Context, *setupconfig.Snapshot, deployscan.Options) ([]deployscan.HostReport, error)
	wipeHost       func(context.Context, setupconfig.Host, string, []string) (deployscan.HostResult, error)
	trialPackage   func(context.Context, unitdeploy.CoreOptions) error
}

func init() {
	rootCmd.AddCommand(newSetupCommand(defaultSetupDeps()))
}

func newSetupCommand(deps setupDeps) *cobra.Command {
	deps = completeSetupDeps(deps)
	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "部署与初始化：主机、组件部署、防火墙、发布与初始数据",
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newSetupInitCommand(deps),
		newSetupWipeCommand(deps),
		newSetupFactorsCommand(deps),
		newSetupHostsCommand(deps),
		newSetupSyncDeploymentsCommand(deps),
		newSetupRemoveHostCommand(deps),
		newSetupValidateCommand(deps),
		newSetupTrustHostCommand(deps),
		newSetupTrustBrowserCommand(deps),
		newSetupBuildLinuxCommand(deps),
		newSetupBootstrapCommand(deps),
		newSetupDeployHostCommand(deps),
		newSetupDeployUnitCommand(deps),
		newSetupReleaseCommand(deps),
		newSetupPackageCommand(),
		newSetupRenderRuntimeConfigCommand(deps),
		newSetupApplyCommand(deps),
		newSetupStatusCommand(deps),
		newSetupRebootHostCommand(deps),
		newSetupHostDiagnosticsCommand(deps),
		newSetupInspectSCFCommand(deps),
		newSetupAttachSCFVPCCommand(deps),
		newSetupInvokeSCFCommand(deps),
		newSetupWatchdogCommand(deps),
		newSetupMetadataImportCommand(deps),
		newSetupExportSkillConfigCommand(deps),
		newSetupFirewallCommand(deps),
		newSetupStorageAuthCommand(deps),
		newSetupHostStatusCommand(deps, defaultStatusEnv),
		newSetupDeploymentStatusCommand(deps, defaultStatusEnv),
	)
	return cmd
}

// newSetupInspectSCFCommand reads a single Tencent SCF function without
// exposing credentials or the function's full environment. It is used by
// deployment validation to prove the accepted VPC/subnet and Storage target.
func newSetupInspectSCFCommand(deps setupDeps) *cobra.Command {
	var file, region, namespace, functionName string
	cmd := &cobra.Command{
		Use:   "inspect-scf",
		Short: "检查腾讯云函数网络配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = strings.TrimSpace(region)
			if region == "" {
				region = snapshot.Manifest.TencentCloud.Region
			}
			namespace = firstNonEmpty(namespace, "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("--function is required")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Manifest.TencentCloud.SecretID,
				SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
				Region:    region,
			})
			if err != nil {
				return err
			}
			fn, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, strings.TrimSpace(functionName))
			if err != nil {
				return err
			}
			accessAddress := ""
			if fn.Environment != nil {
				accessAddress = fn.Environment["MOOX_ACCESS_ADDRESS"]
			}
			return writeSetupJSON(cmd, map[string]any{
				"region": region, "namespace": namespace, "function": functionName,
				"status": fn.Status, "vpc_id": fn.VpcID, "subnet_id": fn.SubnetID,
				"public_net_status": fn.PublicNetStatus,
				"access_address":    accessAddress,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "SCF 地域")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "SCF 命名空间")
	cmd.Flags().StringVar(&functionName, "function", "", "SCF 函数名")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

// newSetupAttachSCFVPCCommand updates one canary function in place. This is
// kept as a setup diagnostic rather than part of the normal fleet publisher so
// an operator can validate the VPC route without waiting for the control-plane
// rollout to finish.
func newSetupAttachSCFVPCCommand(deps setupDeps) *cobra.Command {
	var file, region, namespace, functionName, vpcID, subnetID string
	var yes bool
	cmd := &cobra.Command{
		Use:   "attach-scf-vpc",
		Short: "为腾讯云函数绑定 VPC 子网（默认只预览，--yes 才绑定）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.TencentCloud.Region)
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" || strings.TrimSpace(vpcID) == "" || strings.TrimSpace(subnetID) == "" {
				return fmt.Errorf("--function, --vpc-id and --subnet-id are required")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: region,
			})
			if err != nil {
				return err
			}
			fn, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, functionName)
			if err != nil {
				return err
			}
			status := firstNonEmpty(fn.PublicNetStatus, "ENABLE")
			if !yes {
				return writePreview(cmd, map[string]any{
					"action": "attach_scf_vpc", "region": region, "namespace": namespace, "function": functionName,
					"current_vpc_id": fn.VpcID, "current_subnet_id": fn.SubnetID, "vpc_id": vpcID, "subnet_id": subnetID, "public_net_status": status,
				})
			}
			if err := network.ForRegion(region).UpdateSCFNetwork(cmd.Context(), namespace, functionName, vpcID, subnetID, status, fn.Environment); err != nil {
				return err
			}
			updated, err := network.ForRegion(region).GetSCFFunction(cmd.Context(), namespace, functionName)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"region": region, "namespace": namespace, "function": functionName, "status": updated.Status, "vpc_id": updated.VpcID, "subnet_id": updated.SubnetID, "public_net_status": updated.PublicNetStatus})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "SCF 地域")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "SCF 命名空间")
	cmd.Flags().StringVar(&functionName, "function", "", "SCF 函数名")
	cmd.Flags().StringVar(&vpcID, "vpc-id", "", "VPC ID")
	cmd.Flags().StringVar(&subnetID, "subnet-id", "", "子网 ID")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认绑定；不加只预览")
	_ = cmd.MarkFlagRequired("function")
	_ = cmd.MarkFlagRequired("vpc-id")
	_ = cmd.MarkFlagRequired("subnet-id")
	return cmd
}

// newSetupInvokeSCFCommand invokes a single function directly through SCF's
// control API. It is useful for connectivity proof when the optional
// FunctionInstance control service is temporarily unavailable.
func newSetupInvokeSCFCommand(deps setupDeps) *cobra.Command {
	var file, region, namespace, functionName, eventJSON string
	cmd := &cobra.Command{
		Use:   "invoke-scf",
		Short: "直接调用腾讯云函数",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.TencentCloud.Region)
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("--function is required")
			}
			var event any = map[string]any{"action": "market_fetch", "space_id": "crypto", "subject_id": "BTC-USDT"}
			if strings.TrimSpace(eventJSON) != "" {
				if err := json.Unmarshal([]byte(eventJSON), &event); err != nil {
					return fmt.Errorf("event json invalid: %w", err)
				}
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey, Region: region})
			if err != nil {
				return err
			}
			result, err := network.ForRegion(region).InvokeSCF(cmd.Context(), namespace, functionName, event)
			if err != nil {
				return err
			}
			return writeSetupJSON(cmd, map[string]any{"region": region, "namespace": namespace, "function": functionName, "request_id": result.RequestID, "result": result.Result, "log": result.Log})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&region, "region", "", "SCF 地域")
	cmd.Flags().StringVar(&namespace, "namespace", "default", "SCF 命名空间")
	cmd.Flags().StringVar(&functionName, "function", "", "SCF 函数名")
	cmd.Flags().StringVar(&eventJSON, "event", "", "JSON 事件；缺省为 crypto BTC-USDT market_fetch")
	_ = cmd.MarkFlagRequired("function")
	return cmd
}

// newSetupRebootHostCommand provides a narrowly-scoped recovery operation for
// an unresponsive Tencent CVM. It resolves the host's actual region before
// issuing RebootInstances, because the setup manifest's home region may differ
// from the Storage region.
func newSetupRebootHostCommand(deps setupDeps) *cobra.Command {
	var file, hostName string
	var yes bool
	cmd := &cobra.Command{
		Use:   "reboot-host",
		Short: "重启无响应的腾讯云主机（默认只预览，--yes 才重启）",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostName)
			if err != nil {
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(host.Provider), "tencent") {
				return fmt.Errorf("reboot_host_provider_unsupported")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Manifest.TencentCloud.SecretID,
				SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.TencentCloud.Region,
			})
			if err != nil {
				return err
			}
			lighthouse, lighthouseErr := cloudtencent.NewClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Manifest.TencentCloud.SecretID,
				SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.TencentCloud.Region,
			})
			if lighthouseErr != nil {
				return lighthouseErr
			}
			cloud := tencenthost.TencentCloud{Network: network, Lighthouse: lighthouse}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			instance, err := cloud.LookupHost(ctx, host.Address, cloudtencent.DefaultProbeRegions)
			if err != nil {
				return fmt.Errorf("reboot host lookup: %w", err)
			}
			if !yes {
				return writePreview(cmd, map[string]any{
					"action": "reboot_host", "host": host.Name, "instance_id": instance.InstanceID,
					"region": instance.Region, "zone": instance.Zone, "kind": instance.Kind,
				})
			}
			var requestID string
			switch instance.Kind {
			case cloudtencent.KindLighthouse:
				requestID, err = lighthouse.ForRegion(instance.Region).RebootInstance(ctx, instance.InstanceID)
			case cloudtencent.KindCVM:
				cvm, cvmErr := cloudtencent.NewCVMClient(cloudtencent.ClientOptions{
					SecretID:  snapshot.Manifest.TencentCloud.SecretID,
					SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
					Region:    instance.Region,
				})
				if cvmErr != nil {
					return cvmErr
				}
				requestID, err = cvm.RebootInstance(ctx, instance.InstanceID)
			default:
				return fmt.Errorf("reboot_host_kind_unsupported")
			}
			if err != nil {
				return fmt.Errorf("reboot %s: %w", host.Name, err)
			}
			return writeSetupJSON(cmd, map[string]any{
				"status": "reboot_requested", "host": host.Name,
				"instance_id": instance.InstanceID, "region": instance.Region,
				"zone": instance.Zone, "request_id": requestID,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostName, "host", "", "目标主机名称")
	cmd.Flags().BoolVar(&yes, "yes", false, "确认重启；不加只预览目标实例")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newSetupHostDiagnosticsCommand(deps setupDeps) *cobra.Command {
	var file, hostName string
	cmd := &cobra.Command{
		Use:   "host-diagnostics",
		Short: "检查远端主机磁盘和服务进程",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostName)
			if err != nil {
				return err
			}
			transport, err := dialSetupHost(cmd.Context(), host)
			if err != nil {
				return err
			}
			defer transport.Close()
			paths := snapshot.Manifest.Paths.Resolved()
			root := paths.DeployRoot
			if strings.EqualFold(host.Name, snapshot.Manifest.ControlHost().Name) {
				root = paths.ControlRoot
			} else if snapshot.Manifest.HasStorageHost() && strings.EqualFold(host.Name, snapshot.Manifest.StorageHost().Name) {
				root = paths.StorageRoot
			}
			result, err := transport.Run(cmd.Context(), []string{"sh", "-lc", `set -eu
root="$1"
printf 'df:\n'
df -h "$root" /
printf 'processes:\n'
ps -eo pid,stat,cmd | grep -E 'moox-|caddy' | grep -v grep || true
printf 'sizes:\n'
du -xh --max-depth=2 "$root/data" "$root/logs" 2>/dev/null | sort -h | tail -40 || true
printf 'staged archives:\n'
ls -lh /tmp/moox-control-*.tar.gz /tmp/moox-storage-*.tar.gz 2>/dev/null || true
printf 'recent service errors:\n'
for name in cloudfunction eventbus collector factor gateway storage storage-primary storage-view storage-node; do
  for log in "$root/logs/$name/stdout.log" "$root/logs/$name/stderr.log" "$root/logs/$name.log"; do
    if [ -f "$log" ]; then echo "--- $log"; tail -30 "$log"; fi
  done
done
`, "moox-host-diagnostics", root}, nil)
			if err != nil {
				return fmt.Errorf("host_diagnostics_failed: %v", err)
			}
			return writeSetupJSON(cmd, map[string]any{"host": host.Name, "root": root, "diagnostics": result.Stdout})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostName, "host", "control", "主机名称")
	return cmd
}

func newSetupRenderRuntimeConfigCommand(deps setupDeps) *cobra.Command {
	var file, egressOutput, collectorOutput string
	cmd := &cobra.Command{
		Use:   "render-runtime-config",
		Short: "从 moox.toml 渲染 Egress/Collector 运行配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			egressOutput = strings.TrimSpace(egressOutput)
			collectorOutput = strings.TrimSpace(collectorOutput)
			if egressOutput == "" && collectorOutput == "" {
				return fmt.Errorf("runtime_config: at least one output path is required")
			}
			if egressOutput != "" && collectorOutput != "" {
				egressCanonical, err := canonicalRuntimeOutputPath(egressOutput)
				if err != nil {
					return err
				}
				collectorCanonical, err := canonicalRuntimeOutputPath(collectorOutput)
				if err != nil {
					return err
				}
				if egressCanonical == collectorCanonical {
					return fmt.Errorf("runtime_config: egress and collector outputs must be different")
				}
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)

			// Render both files before mutating either one. The deployment script
			// can therefore fail before a service restart if moox.toml or YAML
			// validation is invalid.
			var egressRaw, collectorRaw []byte
			if egressOutput != "" {
				egressRaw, err = readRuntimeConfigFile(egressOutput)
				if err != nil {
					return err
				}
				egressRaw, err = setupconfig.RenderEgressConfig(snapshot, egressRaw)
				if err != nil {
					return err
				}
			}
			if collectorOutput != "" {
				collectorRaw, err = readRuntimeConfigFile(collectorOutput)
				if err != nil {
					return err
				}
				collectorRaw, err = setupconfig.RenderCollectorRuntimeConfig(snapshot, collectorRaw)
				if err != nil {
					return err
				}
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			if egressOutput != "" {
				if err := setupconfig.WriteRenderedRuntimeConfig(egressOutput, egressRaw); err != nil {
					return err
				}
			}
			if collectorOutput != "" {
				if err := setupconfig.WriteRenderedRuntimeConfig(collectorOutput, collectorRaw); err != nil {
					return err
				}
			}
			tradeConsoleHost := ""
			tradeConsolePort := 0
			if tradeNode := snapshot.Manifest.ComponentHost("trade"); tradeNode != "" {
				tradeHost, tradeErr := findSetupHost(snapshot.Manifest, tradeNode)
				if tradeErr != nil {
					return tradeErr
				}
				tradeConsoleHost = tradeHost.Address
				tradeConsolePort = 11200
			}
			return writeSetupJSON(cmd, map[string]any{
				"status":             "rendered",
				"egress_output":      egressOutput,
				"collector_output":   collectorOutput,
				"trade_console_host": tradeConsoleHost,
				"trade_console_port": tradeConsolePort,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&egressOutput, "egress-output", "", "Egress app.yaml 输出路径")
	cmd.Flags().StringVar(&collectorOutput, "collector-output", "", "Collector app.yaml 输出路径")
	return cmd
}

func canonicalRuntimeOutputPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("runtime_config: resolve output %q: %w", path, err)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func readRuntimeConfigFile(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("runtime_config: read %s: %w", path, err)
	}
	return raw, nil
}

type setupHostChoice struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Provider string `json:"provider,omitempty"`
	Role     string `json:"role"`
}

func newSetupHostsCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "hosts", Short: "列出可选的部署主机（不输出密码）", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		hosts := make([]setupHostChoice, 0, len(snapshot.Manifest.Hosts())+1)
		for _, host := range snapshot.Manifest.Hosts() {
			role := "other"
			switch {
			case host.Name == snapshot.Manifest.ControlHost().Name:
				role = "control"
			case snapshot.Manifest.HasStorageHost() && strings.EqualFold(host.Name, snapshot.Manifest.StorageHost().Name):
				role = "storage"
			case snapshot.Manifest.HasViewHost() && strings.EqualFold(host.Name, snapshot.Manifest.ViewHost().Name):
				role = "view"
			}
			hosts = append(hosts, setupHostChoice{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username, Provider: host.Provider, Role: role})
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]any{"hosts": hosts})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func newSetupValidateCommand(deps setupDeps) *cobra.Command {
	var file string
	var options unitdeploy.CoreOptions
	cmd := &cobra.Command{Use: "validate", Short: "校验初始化配置和连接，并报告目标机器的部署情况与发布路线（route）", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.validate(cmd.Context(), snapshot)
		output := setupValidateOutput{Result: result}
		var deployment error
		if err == nil && deps.scanDeployment != nil {
			output.Deployment, deployment = setupDeploymentStatus(cmd.Context(), deps, snapshot, &options)
		}
		if encodeErr := writeSetupJSON(cmd, output); encodeErr != nil {
			return encodeErr
		}
		if err != nil {
			return err
		}
		return deployment
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&options.StateDirectory, "state-dir", "", "持久私有部署状态目录（默认和 bootstrap 相同）")
	cmd.Flags().StringVar(&options.OperatorDirectory, "operator-dir", "", "本机操作员身份目录（默认 ~/.config/moox）")
	return cmd
}

func newSetupTrustHostCommand(deps setupDeps) *cobra.Command {
	var file, host, fingerprint string
	cmd := &cobra.Command{Use: "trust-host", Short: "确认并记录 SSH 主机指纹", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if err := deps.trustHost(cmd.Context(), snapshot, host, fingerprint); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]string{"host": host, "status": "trusted"})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "control", "主机名称")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "已核验的 SHA256 指纹")
	_ = cmd.MarkFlagRequired("fingerprint")
	return cmd
}

func newSetupTrustBrowserCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "trust-browser",
		Short: "检查并安装管理台浏览器证书信任",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host := snapshot.Manifest.ControlHost()
			result := map[string]any{
				"host":   host.Name,
				"status": "trusted",
			}
			if !tlstrust.RequiresLocalCATrust(tlstrust.TLSMode(host.TLSMode), host.Address) {
				result["status"] = "not_required"
			} else {
				if err := ensureSetupBrowserCATrust(cmd.Context(), snapshot); err != nil {
					return err
				}
				trust := describeSetupBrowserTrust(snapshot)
				result["ca_path"] = trust.CAPath
				result["fingerprint"] = trust.Fingerprint
				result["other_machines"] = trust.OtherMachines
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

func newSetupApplyCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "apply", Short: "写入初始用户与云凭据", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if _, err := deps.validate(cmd.Context(), snapshot); err != nil {
			return err
		}
		applied, err := deps.apply(cmd.Context(), snapshot)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		login, err := deps.login(cmd.Context(), snapshot)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, struct {
			setupclient.ApplyResult
			LoginAPI string `json:"login_api"`
		}{ApplyResult: applied, LoginAPI: login.LoginAPI})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func newSetupStatusCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "status", Short: "检查初始化记录状态", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.status(cmd.Context(), snapshot)
		if err != nil {
			return err
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func newSetupMetadataImportCommand(deps setupDeps) *cobra.Command {
	var file, seed, storageHost string
	var spaces []string
	cmd := &cobra.Command{Use: "metadata-import", Short: "通过 Storage SSH 隧道导入选定业务空间", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.importMetadata(cmd.Context(), snapshot, storageHost, seed, spaces)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&seed, "seed", "config/setup/metadata.yaml", "metadata seed YAML")
	cmd.Flags().StringVar(&storageHost, "storage-host", "", "已部署 Storage 的主机名称")
	cmd.Flags().StringSliceVar(&spaces, "spaces", nil, "要导入的 Space ID 或中文名")
	_ = cmd.MarkFlagRequired("storage-host")
	_ = cmd.MarkFlagRequired("spaces")
	return cmd
}

func completeSetupDeps(deps setupDeps) setupDeps {
	defaults := defaultSetupDeps()
	if deps.load == nil {
		deps.load = defaults.load
	}
	if deps.loadInitBundle == nil {
		deps.loadInitBundle = defaults.loadInitBundle
	}
	if deps.validate == nil {
		deps.validate = defaults.validate
	}
	if deps.trustHost == nil {
		deps.trustHost = defaults.trustHost
	}
	if deps.apply == nil {
		deps.apply = defaults.apply
	}
	if deps.status == nil {
		deps.status = defaults.status
	}
	if deps.applySpaces == nil {
		deps.applySpaces = defaults.applySpaces
	}
	if deps.statusSpaces == nil {
		deps.statusSpaces = defaults.statusSpaces
	}
	if deps.registerCloudAccounts == nil {
		deps.registerCloudAccounts = defaults.registerCloudAccounts
	}
	if deps.provisionCLS == nil {
		deps.provisionCLS = defaults.provisionCLS
	}
	if deps.ensureSCFPlan == nil {
		deps.ensureSCFPlan = defaults.ensureSCFPlan
	}
	if deps.login == nil {
		deps.login = defaults.login
	}
	if deps.openInitStorage == nil {
		deps.openInitStorage = defaults.openInitStorage
	}
	if deps.openInitFactor == nil {
		deps.openInitFactor = defaults.openInitFactor
	}
	if deps.initStorage == nil {
		deps.initStorage = func(ctx context.Context, snapshot *setupconfig.Snapshot, file, configDir, storageHost string, bundle setupInitBundle) (setupInitSummary, error) {
			return runSetupInit(ctx, deps, snapshot, file, configDir, storageHost, bundle)
		}
	}
	if deps.installWatchdog == nil {
		deps.installWatchdog = defaults.installWatchdog
	}
	if deps.importMetadata == nil {
		deps.importMetadata = defaults.importMetadata
	}
	if deps.exportSkillConfig == nil {
		deps.exportSkillConfig = defaults.exportSkillConfig
	}
	if deps.ensureRelayFirewall == nil {
		deps.ensureRelayFirewall = defaults.ensureRelayFirewall
	}
	if deps.ensureFirewall == nil {
		deps.ensureFirewall = defaults.ensureFirewall
	}
	return deps
}

func defaultSetupDeps() setupDeps {
	return setupDeps{
		load: func(path string) (*setupconfig.Snapshot, error) {
			root, err := os.Getwd()
			if err != nil {
				return nil, fmt.Errorf("config_invalid")
			}
			return setupconfig.Load(path, root)
		},
		loadInitBundle:        loadSetupInitBundle,
		scanDeployment:        defaultSetupScanDeployment,
		wipeHost:              defaultSetupWipeHost,
		trialPackage:          defaultSetupTrialPackage,
		validate:              defaultSetupValidate,
		trustHost:             defaultSetupTrustHost,
		apply:                 defaultSetupApply,
		status:                defaultSetupStatus,
		applySpaces:           defaultSetupApplyWithSpaces,
		statusSpaces:          defaultSetupStatusWithSpaces,
		registerCloudAccounts: defaultSetupRegisterCloudAccounts,
		provisionCLS:          defaultSetupProvisionCLS,
		ensureSCFPlan:         defaultSetupEnsureSCFPlan,
		openInitStorage:       defaultOpenSetupInitStorage,
		openInitFactor:        defaultOpenSetupFactor,
		initStorage:           nil,
		installWatchdog:       defaultSetupInstallWatchdog,
		importMetadata:        defaultSetupImportMetadata,
		exportSkillConfig:     defaultSetupExportSkillConfig,
		ensureFirewall:        defaultSetupEnsureFirewall,
		ensureRelayFirewall:   setupEnsureRelayFirewall,
		login: func(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			baseURL := fmt.Sprintf("https://%s:9527", snapshot.Manifest.ControlHost().Address)
			tlsMode := tlstrust.TLSMode(snapshot.Manifest.ControlHost().TLSMode)
			if err := ensureSetupBrowserCATrust(ctx, snapshot); err != nil {
				return setupclient.LoginResult{}, err
			}
			if tlstrust.UsesPublicTLSMode(tlsMode, snapshot.Manifest.ControlHost().Address) {
				return setupclient.VerifyPublicLogin(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password)
			}
			return setupclient.VerifyPublicLoginWithCAFile(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password, tlstrust.CAPath(snapshot.Manifest.ControlHost().Address))
		},
	}
}

var storageSecretValuePattern = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

func normalizeStorageInternalAuth(raw string) (string, error) {
	return normalizeSecretEnv(raw, []string{
		"MOOX_STORAGE_PRIMARY_AUTH_SECRET",
		"MOOX_STORAGE_VIEW_AUTH_SECRET",
	})
}

func normalizeSecretEnv(raw string, keys []string) (string, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return "", fmt.Errorf("invalid auth file")
	}
	allowed := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		allowed[key] = struct{}{}
	}
	values := make(map[string]string, len(keys))
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if _, accepted := allowed[key]; !ok || !accepted || value == "" || !storageSecretValuePattern.MatchString(value) {
			return "", fmt.Errorf("invalid auth entry")
		}
		if _, exists := values[key]; exists {
			return "", fmt.Errorf("duplicate auth entry")
		}
		values[key] = value
	}
	var output strings.Builder
	for _, key := range keys {
		if values[key] == "" {
			return "", fmt.Errorf("missing auth entry")
		}
		output.WriteString(key)
		output.WriteByte('=')
		output.WriteString(values[key])
		output.WriteByte('\n')
	}
	return output.String(), nil
}

func defaultSetupImportMetadata(ctx context.Context, snapshot *setupconfig.Snapshot, hostName, seedPath string, spaces []string) (metadataImportSummary, error) {
	status, err := defaultSetupStatus(ctx, snapshot)
	if err != nil || status.State != "completed" {
		return metadataImportSummary{}, fmt.Errorf("setup_incomplete")
	}
	seed, err := loadMetadataSeed(seedPath)
	if err != nil {
		return metadataImportSummary{}, err
	}
	seed, err = selectMetadataSpaces(seed, spaces)
	if err != nil {
		return metadataImportSummary{}, err
	}
	calls, err := buildMetadataImportCalls(seed)
	if err != nil {
		return metadataImportSummary{}, err
	}
	_, err = findSetupHost(snapshot.Manifest, hostName)
	if err != nil {
		return metadataImportSummary{}, err
	}
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return metadataImportSummary{}, err
	}
	defer gateway.Close()
	return runMetadataImport(ctx, gateway, calls, true)
}

func defaultSetupValidate(ctx context.Context, snapshot *setupconfig.Snapshot) (setupvalidate.Result, error) {
	identity, err := cloudtencent.NewIdentityValidator(cloudtencent.IdentityOptions{Credentials: cloudtencent.Credentials{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
	}})
	if err != nil {
		return setupvalidate.Result{}, fmt.Errorf("tencent_auth_failed")
	}
	return setupvalidate.Run(ctx, snapshot, setupvalidate.Dependencies{Identity: identity, SSH: commandSSHChecker{}})
}

type commandSSHChecker struct{}

func (commandSSHChecker) Check(ctx context.Context, host setupconfig.Host) error {
	client, err := dialSetupHost(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Check(ctx)
}

func defaultSetupTrustHost(ctx context.Context, snapshot *setupconfig.Snapshot, name, fingerprint string) error {
	host, err := findSetupTrustHost(snapshot.Manifest, name)
	if err != nil {
		return err
	}
	return setupssh.TrustHost(ctx, sshTarget(host), fingerprint, setupssh.Options{Timeout: 15 * time.Second})
}

func isPublicFirewallIP(address string) bool {
	ip := net.ParseIP(strings.TrimSpace(address))
	return ip != nil && ip.To4() != nil && ip.IsGlobalUnicast() &&
		!ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsMulticast()
}

func setupControlFirewallRules() []cloudtencent.CreateFirewallRulesOptions {
	return []cloudtencent.CreateFirewallRulesOptions{
		{
			Protocol: "TCP", Ports: "80", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX ACME HTTP challenge",
		},
		{
			Protocol: "TCP", Ports: "9527", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX browser HTTPS",
		},
		{
			Protocol: "TCP", Ports: "11001", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX service HTTPS",
		},
	}
}

func setupControlFirewallRulesForTLS(mode tlstrust.TLSMode, address string) []cloudtencent.CreateFirewallRulesOptions {
	rules := setupControlFirewallRules()
	if tlstrust.UsesPublicTLSMode(mode, address) {
		return rules
	}
	return rules[1:]
}

// setupEventBusFirewallRule 是 EventBus 端口的放行规则：只放在 EventBus 所在主机上（不一定是控制机），
// 其他主机的客户端和因子机、海外机的出口 IP 都不固定，只能按 0.0.0.0/0 放行，认证由 TLS 与角色凭据完成。
func setupEventBusFirewallRule(eventBusPort int) cloudtencent.CreateFirewallRulesOptions {
	return cloudtencent.CreateFirewallRulesOptions{
		Protocol: "TCP", Ports: fmt.Sprint(eventBusPort), CidrBlock: "0.0.0.0/0",
		Action: "ACCEPT", Description: "MooX EventBus TLS",
	}
}

// setupRuntimeFirewallRules 是控制机上运行时需要的端口（不含 EventBus，见 setupEventBusFirewallRule）。
func setupRuntimeFirewallRules() []cloudtencent.CreateFirewallRulesOptions {
	return []cloudtencent.CreateFirewallRulesOptions{
		{
			Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX service gateway native",
		},
		{
			Protocol: "TCP", Ports: "11012", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX SCF Gateway readiness",
		},
		{
			Protocol: "TCP", Ports: "11409", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX SCF Monitor readiness",
		},
	}
}

func eventBusFirewallIP(
	ctx context.Context,
	address string,
	lookup func(context.Context, string, string) ([]net.IP, error),
) (string, error) {
	if ip := net.ParseIP(address); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			return ipv4.String(), nil
		}
		return "", fmt.Errorf("EventBus firewall requires IPv4")
	}
	ips, err := lookup(ctx, "ip4", address)
	if err != nil {
		return "", err
	}
	unique := make(map[string]struct{}, len(ips))
	for _, ip := range ips {
		if ipv4 := ip.To4(); ipv4 != nil {
			unique[ipv4.String()] = struct{}{}
		}
	}
	if len(unique) != 1 {
		return "", fmt.Errorf("EventBus DNS address must resolve to exactly one IPv4 address")
	}
	for ip := range unique {
		return ip, nil
	}
	return "", fmt.Errorf("EventBus DNS address has no IPv4 address")
}

func ensureSetupBrowserCATrust(ctx context.Context, snapshot *setupconfig.Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("browser_ca_trust_failed: setup configuration is missing")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("browser_ca_trust_failed: resolve repository root: %w", err)
	}
	host := snapshot.Manifest.ControlHost()
	mode := tlstrust.TLSMode(host.TLSMode)
	if !tlstrust.RequiresLocalCATrust(mode, host.Address) {
		return nil
	}
	// 原生 control 单元把内部 CA 根证书发布在 current/console-proxy 下；先经 SSH 取回到本机缓存再安装信任，
	// 不依赖本机残留的旧证书副本。
	if err := refreshSetupConsoleCA(ctx, snapshot.Manifest); err != nil {
		return fmt.Errorf("browser_ca_trust_failed: 无法从管理台服务器取回内部 CA 根证书: %w", err)
	}
	return tlstrust.EnsureLocalCATrustForHost(ctx, root, host.Address, mode)
}

// refreshSetupConsoleCA 在测试里替换。
var refreshSetupConsoleCA = configinit.RefreshConsoleCA

func defaultSetupApply(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.ApplyResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).Apply(ctx, snapshot)
}

func defaultSetupStatus(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.StatusResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).Status(ctx, snapshot)
}

func defaultSetupApplyWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []setupclient.Space,
) (setupclient.ApplyResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).ApplyWithSpaces(ctx, snapshot, spaces)
}

func defaultSetupStatusWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []setupclient.Space,
) (setupclient.StatusResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).StatusWithSpaces(ctx, snapshot, spaces)
}

func dialSetupHost(ctx context.Context, host setupconfig.Host) (setupssh.Client, error) {
	return setupssh.Dial(ctx, sshTarget(host), host.Password, setupssh.Options{Timeout: 15 * time.Second})
}

func sshTarget(host setupconfig.Host) setupssh.Target {
	return setupssh.Target{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username}
}

func findSetupHost(manifest setupconfig.Manifest, name string) (setupconfig.Host, error) {
	for _, host := range manifest.Hosts() {
		if strings.EqualFold(host.Name, strings.TrimSpace(name)) {
			return host, nil
		}
	}
	return setupconfig.Host{}, fmt.Errorf("setup_host_not_found")
}

func resolveStorageDeploymentHost(manifest setupconfig.Manifest, name string) (setupconfig.Host, error) {
	name = strings.TrimSpace(name)
	if name == "" && manifest.HasStorageHost() {
		name = manifest.StorageHost().Name
	}
	host, err := findSetupHost(manifest, name)
	if err != nil {
		return setupconfig.Host{}, err
	}
	if manifest.HasStorageHost() && !strings.EqualFold(host.Name, manifest.StorageHost().Name) {
		return setupconfig.Host{}, fmt.Errorf("storage_host_required")
	}
	if manifest.HasViewHost() && !sameHostEndpoint(host, manifest.ViewHost()) {
		return setupconfig.Host{}, fmt.Errorf("storage_view_hosts_must_share_endpoint")
	}
	return host, nil
}

func sameHostEndpoint(left, right setupconfig.Host) bool {
	leftAddress := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(left.Address)), ".")
	rightAddress := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(right.Address)), ".")
	if parsed := net.ParseIP(leftAddress); parsed != nil {
		leftAddress = parsed.String()
	}
	if parsed := net.ParseIP(rightAddress); parsed != nil {
		rightAddress = parsed.String()
	}
	return leftAddress == rightAddress && left.Port == right.Port
}

func findSetupTrustHost(manifest setupconfig.Manifest, name string) (setupconfig.Host, error) {
	return findSetupHost(manifest, name)
}

func clearSetupSecrets(snapshot *setupconfig.Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.Manifest.Admin.Password = ""
	snapshot.Manifest.TencentCloud.SecretID = ""
	snapshot.Manifest.TencentCloud.SecretKey = ""
	snapshot.Manifest.Notification.WebhookURL = ""
	for id, definition := range snapshot.Manifest.HostCatalog {
		definition.SSH.Password = ""
		snapshot.Manifest.HostCatalog[id] = definition
	}
}

func writeSetupJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
