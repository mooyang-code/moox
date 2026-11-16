package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/configinit"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/deployscan"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/tlstrust"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	"github.com/mooyang-code/moox/modules/cli/internal/tencenthost"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

const defaultSetupFile = "./moox.toml"

// setupSnapshot 是 setup 命令读到的两份配置：Manifest 是明文的 moox.toml，Secrets 是解开的 moox.secrets.toml。
type setupSnapshot struct {
	Manifest *topology.Config
	Secrets  *topology.Secrets
}

type setupDeps struct {
	load                  func(string) (*setupSnapshot, error)
	loadInitBundle        func(string) (setupInitBundle, error)
	validate              func(context.Context, *setupSnapshot) (setupvalidate.Result, error)
	trustHost             func(context.Context, *setupSnapshot, string, string) error
	applySpaces           func(context.Context, *setupSnapshot, []setupclient.Space) (setupclient.ApplyResult, error)
	statusSpaces          func(context.Context, *setupSnapshot, []setupclient.Space) (setupclient.StatusResult, error)
	registerCloudAccounts func(context.Context, *setupSnapshot) (*setupCloudAccountSummary, error)
	provisionCLS          func(context.Context, *setupSnapshot) (*setupCLSSummary, error)
	// ensureSCFPlan 的最后一个参数是源码或产物包目录：云函数代码包从它下面的 modules/collector 构建。
	ensureSCFPlan   func(context.Context, *setupSnapshot, string) (*setupSCFPlanSummary, error)
	login           func(context.Context, *setupSnapshot) (setupclient.LoginResult, error)
	openInitStorage func(context.Context, *setupSnapshot, string) (setupInitStorage, error)
	openInitFactor  func(context.Context, *setupSnapshot) (setupInitFactor, error)
	// ensureNotification 在 Monitor 还没有任何告警渠道时写入初始渠道（只建不改）；测试里替换。
	ensureNotification func(context.Context, *setupSnapshot) (*setupNotificationSummary, error)
	// initStorage 的参数依次是存储机、源码或产物包目录、种子。
	initStorage       func(context.Context, *setupSnapshot, string, string, setupInitBundle) (setupInitSummary, error)
	initMetadata      func(context.Context, *setupSnapshot, string, setupInitBundle) (setupInitStorageSummary, error)
	importMetadata    func(context.Context, *setupSnapshot, string, string, []string) (metadataImportSummary, error)
	exportSkillConfig func(context.Context, *setupSnapshot, string) (dataAccessConfig, error)
	// ensureOperatorKey 确保这台电脑的身份目录里有 moox-cli 的签名私钥（已有就复用），返回公钥与是否新生成。
	ensureOperatorKey func(string) (string, bool, error)
	// registerIdentity 经 SSH 在控制机上登记（替换）一个不属于主机的身份的公钥。
	registerIdentity func(context.Context, *setupSnapshot, string, string) error
	// syncAccessIdentities 把最新的外部身份公钥表推到各台 access（临时，外部入口并入主机网关后删除），返回要转告用户的提醒。
	syncAccessIdentities func(context.Context, *setupSnapshot) []string
	ensureFirewall       func(context.Context, *setupSnapshot) (setupFirewallSummary, error)
	ensureRelayFirewall  func(context.Context, *setupSnapshot) (setupFirewallSummary, error)
	// scanDeployment 只读检测目标机器的部署情况；wipeHost 清空一台主机。
	scanDeployment func(context.Context, *setupSnapshot, deployscan.Options) ([]deployscan.HostReport, error)
	wipeHost       func(context.Context, topology.Host, string) (deployscan.HostResult, error)
	// deployHost 部署一台主机；waitReady 等一台主机上的一组组件就绪，返回到时限还没就绪的组件与原因。
	deployHost func(context.Context, deploy.Options) (deploy.Result, error)
	waitReady  func(context.Context, topology.Host, []string, time.Duration) (map[string]string, error)
	// operate 对一个组件的用户服务执行 restart、stop、start；readLogs 读组件日志。
	operate   func(context.Context, topology.Host, string, string) (string, error)
	readLogs  func(context.Context, topology.Host, string, []string, string, int, bool) ([]deploy.LogSection, error)
	openStart func(context.Context, string, *setupSnapshot) (setupStartClient, func(), error)
	// backupConfig、verifySpace 是 setup install 的最后两步，测试里替换。
	backupConfig func(context.Context, string) error
	verifySpace  func(context.Context, string, *setupSnapshot, string) error
	regionCloud  func(*setupSnapshot, string) (setupFunctionCloud, error)
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
		newSetupInstallCommand(deps),
		newSetupOperatorCommand(deps),
		newSetupDeployCommand(deps),
		newSetupInitCommand(deps),
		newSetupStartCommand(deps),
		newSetupVerifyCommand(deps),
		newSetupValidateCommand(deps),
		newSetupStatusCommand(deps),
		newSetupLogsCommand(deps),
		newSetupRestartCommand(deps),
		newSetupStopCommand(deps),
		newSetupStartComponentCommand(deps),
		newSetupWipeCommand(deps),
		newSetupHostsCommand(deps),
		newSetupSyncDeploymentsCommand(deps),
		newSetupRemoveHostCommand(deps),
		newSetupTrustHostCommand(deps),
		newSetupTrustBrowserCommand(deps),
		newSetupBuildLinuxCommand(deps),
		newSetupPackageComponentsCommand(),
		newSetupRebootHostCommand(deps),
		newSetupHostDiagnosticsCommand(deps),
		newSetupInspectSCFCommand(deps),
		newSetupAttachSCFVPCCommand(deps),
		newSetupInvokeSCFCommand(deps),
		newSetupMetadataImportCommand(deps),
		newSetupExportSkillConfigCommand(deps),
		newSetupRegisterIdentityCommand(deps),
		newSetupFirewallCommand(deps),
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
				region = snapshot.Manifest.CloudRegion()
			}
			namespace = firstNonEmpty(namespace, "default")
			if strings.TrimSpace(functionName) == "" {
				return fmt.Errorf("--function is required")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Secrets.TencentCloud.SecretID,
				SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
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
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.CloudRegion())
			namespace = firstNonEmpty(strings.TrimSpace(namespace), "default")
			if strings.TrimSpace(functionName) == "" || strings.TrimSpace(vpcID) == "" || strings.TrimSpace(subnetID) == "" {
				return fmt.Errorf("--function, --vpc-id and --subnet-id are required")
			}
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{
				SecretID: snapshot.Secrets.TencentCloud.SecretID, SecretKey: snapshot.Secrets.TencentCloud.SecretKey, Region: region,
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

// newSetupInvokeSCFCommand 直接经腾讯云 SCF 的控制接口调用一个函数，不经 Collector；Collector 不可用时用来证明连通性。
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
			region = firstNonEmpty(strings.TrimSpace(region), snapshot.Manifest.CloudRegion())
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
			network, err := cloudtencent.NewNetworkClient(cloudtencent.ClientOptions{SecretID: snapshot.Secrets.TencentCloud.SecretID, SecretKey: snapshot.Secrets.TencentCloud.SecretKey, Region: region})
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
				SecretID:  snapshot.Secrets.TencentCloud.SecretID,
				SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.CloudRegion(),
			})
			if err != nil {
				return err
			}
			lighthouse, lighthouseErr := cloudtencent.NewClient(cloudtencent.ClientOptions{
				SecretID:  snapshot.Secrets.TencentCloud.SecretID,
				SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.CloudRegion(),
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
					SecretID:  snapshot.Secrets.TencentCloud.SecretID,
					SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
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
			root := snapshot.Manifest.Paths.DeployRoot
			result, err := transport.Run(cmd.Context(), []string{"sh", "-lc", `set -eu
root="$1"
printf 'df:\n'
df -h "$root" /
printf 'processes:\n'
ps -eo pid,stat,cmd | grep -E 'moox-' | grep -v grep || true
printf 'sizes:\n'
du -xh --max-depth=2 "$root/data" "$root/logs" 2>/dev/null | sort -h | tail -40 || true
printf 'services:\n'
systemctl --user list-units 'moox-*' --no-pager --no-legend 2>/dev/null || true
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
			case snapshot.Manifest.StorageHost().Name != "" && strings.EqualFold(host.Name, snapshot.Manifest.StorageHost().Name):
				role = "storage"
			case snapshot.Manifest.ComponentHost("storage-view") != "" && strings.EqualFold(host.Name, snapshot.Manifest.HostByID(snapshot.Manifest.ComponentHost("storage-view")).Name):
				role = "view"
			}
			hosts = append(hosts, setupHostChoice{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username, Provider: host.Provider, Role: role})
		}
		return writeSetupJSON(cmd, map[string]any{"hosts": hosts})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
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
			if err := ensureSetupBrowserCATrust(cmd.Context(), snapshot); err != nil {
				return err
			}
			trust := describeSetupBrowserTrust(snapshot)
			result := map[string]any{
				"host":           host.Name,
				"status":         "trusted",
				"ca_path":        trust.CAPath,
				"fingerprint":    trust.Fingerprint,
				"other_machines": trust.OtherMachines,
			}
			return writeSetupJSON(cmd, result)
		},
	}
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

// completeSetupDeps 是默认依赖的唯一补全入口：测试只替换自己关心的那几项，其余用默认实现。
// 子命令里不要再判空补默认值：那样只改到子命令自己的副本，聚合命令 setup install 拿到的还是空的。
func completeSetupDeps(deps setupDeps) setupDeps {
	defaults := defaultSetupDeps()
	fillSetupDep(&deps.load, defaults.load)
	fillSetupDep(&deps.loadInitBundle, defaults.loadInitBundle)
	fillSetupDep(&deps.validate, defaults.validate)
	fillSetupDep(&deps.trustHost, defaults.trustHost)
	fillSetupDep(&deps.applySpaces, defaults.applySpaces)
	fillSetupDep(&deps.statusSpaces, defaults.statusSpaces)
	fillSetupDep(&deps.registerCloudAccounts, defaults.registerCloudAccounts)
	fillSetupDep(&deps.provisionCLS, defaults.provisionCLS)
	fillSetupDep(&deps.ensureSCFPlan, defaults.ensureSCFPlan)
	fillSetupDep(&deps.login, defaults.login)
	fillSetupDep(&deps.openInitStorage, defaults.openInitStorage)
	fillSetupDep(&deps.openInitFactor, defaults.openInitFactor)
	fillSetupDep(&deps.ensureNotification, defaults.ensureNotification)
	fillSetupDep(&deps.importMetadata, defaults.importMetadata)
	fillSetupDep(&deps.exportSkillConfig, defaults.exportSkillConfig)
	fillSetupDep(&deps.ensureOperatorKey, defaults.ensureOperatorKey)
	fillSetupDep(&deps.registerIdentity, defaults.registerIdentity)
	fillSetupDep(&deps.syncAccessIdentities, defaults.syncAccessIdentities)
	fillSetupDep(&deps.ensureFirewall, defaults.ensureFirewall)
	fillSetupDep(&deps.ensureRelayFirewall, defaults.ensureRelayFirewall)
	fillSetupDep(&deps.scanDeployment, defaults.scanDeployment)
	fillSetupDep(&deps.wipeHost, defaults.wipeHost)
	fillSetupDep(&deps.deployHost, defaults.deployHost)
	fillSetupDep(&deps.waitReady, defaults.waitReady)
	fillSetupDep(&deps.operate, defaults.operate)
	fillSetupDep(&deps.readLogs, defaults.readLogs)
	fillSetupDep(&deps.openStart, defaults.openStart)
	fillSetupDep(&deps.backupConfig, defaults.backupConfig)
	fillSetupDep(&deps.verifySpace, defaults.verifySpace)
	fillSetupDep(&deps.regionCloud, defaults.regionCloud)
	if deps.initStorage == nil {
		deps.initStorage = func(ctx context.Context, snapshot *setupSnapshot, storageHost, sourceRoot string, bundle setupInitBundle) (setupInitSummary, error) {
			return runSetupInit(ctx, deps, snapshot, storageHost, sourceRoot, bundle)
		}
	}
	if deps.initMetadata == nil {
		deps.initMetadata = func(ctx context.Context, snapshot *setupSnapshot, storageHost string, bundle setupInitBundle) (setupInitStorageSummary, error) {
			return runSetupInitMetadata(ctx, deps, snapshot, storageHost, bundle)
		}
	}
	return deps
}

// fillSetupDep 在一项依赖为空时填上默认实现。
func fillSetupDep[T any](field *T, fallback T) {
	if reflect.ValueOf(*field).IsNil() {
		*field = fallback
	}
}

// loadSetupSnapshot 读 moox.toml 与同目录的 moox.secrets.toml，并核对两份文件之间的关联。
func loadSetupSnapshot(path string) (*setupSnapshot, error) {
	cfg, err := topology.Load(path)
	if err != nil {
		return nil, err
	}
	secrets, err := topology.LoadSecrets(cfg.Path())
	if err != nil {
		return nil, err
	}
	if err := secrets.CheckFor(cfg); err != nil {
		return nil, err
	}
	return &setupSnapshot{Manifest: cfg, Secrets: secrets}, nil
}

func defaultSetupDeps() setupDeps {
	return setupDeps{
		load: loadSetupSnapshot,
		backupConfig: func(ctx context.Context, file string) error {
			_, err := backupConfigFile(ctx, file)
			return err
		},
		verifySpace: func(ctx context.Context, file string, snapshot *setupSnapshot, spaceID string) error {
			_, err := runSetupVerifySpace(ctx, file, snapshot, spaceID, setupVerifyTimeout)
			return err
		},
		regionCloud:           defaultSetupRegionCloud,
		loadInitBundle:        loadSetupInitBundle,
		scanDeployment:        defaultSetupScanDeployment,
		wipeHost:              defaultSetupWipeHost,
		deployHost:            deploy.Deploy,
		waitReady:             defaultSetupWaitReady,
		operate:               defaultSetupOperate,
		readLogs:              defaultSetupReadLogs,
		openStart:             defaultSetupOpenStart,
		validate:              defaultSetupValidate,
		trustHost:             defaultSetupTrustHost,
		applySpaces:           defaultSetupApplyWithSpaces,
		statusSpaces:          defaultSetupStatusWithSpaces,
		registerCloudAccounts: defaultSetupRegisterCloudAccounts,
		provisionCLS:          defaultSetupProvisionCLS,
		ensureSCFPlan:         defaultSetupEnsureSCFPlan,
		openInitStorage:       defaultOpenSetupInitStorage,
		openInitFactor: func(ctx context.Context, snapshot *setupSnapshot) (setupInitFactor, error) {
			return defaultOpenSetupFactor(ctx, snapshot.Manifest)
		},
		ensureNotification:   defaultSetupEnsureNotification,
		importMetadata:       defaultSetupImportMetadata,
		exportSkillConfig:    defaultSetupExportSkillConfig,
		ensureOperatorKey:    defaultSetupEnsureOperatorKey,
		registerIdentity:     defaultSetupRegisterIdentity,
		syncAccessIdentities: defaultSetupSyncAccessIdentities,
		ensureFirewall:       defaultSetupEnsureFirewall,
		ensureRelayFirewall:  setupEnsureRelayFirewall,
		login: func(ctx context.Context, snapshot *setupSnapshot) (setupclient.LoginResult, error) {
			baseURL := fmt.Sprintf("https://%s:9527", snapshot.Manifest.ControlHost().Address)
			// 这里只校验登录，用取回的根证书连接，不碰本机信任库。
			if err := refreshSetupConsoleCA(ctx, snapshot.Manifest); err != nil {
				return setupclient.LoginResult{}, fmt.Errorf("browser_ca_trust_failed: 无法从管理台服务器取回 MooX CA 根证书: %w", err)
			}
			return setupclient.VerifyPublicLoginWithCAFile(ctx, baseURL, snapshot.Secrets.Admin.Username, snapshot.Secrets.Admin.Password, tlstrust.CAPath(snapshot.Manifest.ControlHost().Address))
		},
	}
}

// loginSkippedKeptAdmin 是管理员以管理台为准保留时跳过登录校验的说明。
const loginSkippedKeptAdmin = "管理员以管理台为准（管理台改过密码或用户名），没有用秘密文件里的密码校验登录"

// verifySetupLogin 用秘密文件里的管理员校验管理台能登录；管理员被保留（kept 含 admin）时秘密文件里的密码不是现在的密码，跳过校验并返回说明。
func verifySetupLogin(ctx context.Context, deps setupDeps, snapshot *setupSnapshot, kept []string) (setupclient.LoginResult, string, error) {
	if slices.Contains(kept, "admin") {
		return setupclient.LoginResult{}, loginSkippedKeptAdmin, nil
	}
	login, err := deps.login(ctx, snapshot)
	return login, "", err
}

func defaultSetupImportMetadata(ctx context.Context, snapshot *setupSnapshot, hostName, seedPath string, spaces []string) (metadataImportSummary, error) {
	status, err := defaultSetupStatusWithSpaces(ctx, snapshot, nil)
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
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return metadataImportSummary{}, err
	}
	defer gateway.Close()
	return runMetadataImport(ctx, gateway, calls, true)
}

func defaultSetupValidate(ctx context.Context, snapshot *setupSnapshot) (setupvalidate.Result, error) {
	identity, err := cloudtencent.NewIdentityValidator(cloudtencent.IdentityOptions{Credentials: cloudtencent.Credentials{
		SecretID: snapshot.Secrets.TencentCloud.SecretID, SecretKey: snapshot.Secrets.TencentCloud.SecretKey,
	}})
	if err != nil {
		return setupvalidate.Result{}, fmt.Errorf("tencent_auth_failed")
	}
	return setupvalidate.Run(ctx, snapshot.Manifest, setupvalidate.Dependencies{Identity: identity, SSH: commandSSHChecker{}})
}

type commandSSHChecker struct{}

func (commandSSHChecker) Check(ctx context.Context, host topology.Host) error {
	client, err := dialSetupHost(ctx, host)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Check(ctx)
}

func defaultSetupTrustHost(ctx context.Context, snapshot *setupSnapshot, name, fingerprint string) error {
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

// setupControlFirewallRules 是控制机上浏览器入口的放行规则；没有进程监听 11001，不放行它。
func setupControlFirewallRules() []cloudtencent.CreateFirewallRulesOptions {
	return []cloudtencent.CreateFirewallRulesOptions{
		{
			Protocol: "TCP", Ports: "9527", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX browser HTTPS",
		},
	}
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
// 健康端口只听本机，不放行。
func setupRuntimeFirewallRules() []cloudtencent.CreateFirewallRulesOptions {
	return []cloudtencent.CreateFirewallRulesOptions{
		{
			Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX service gateway native",
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

func ensureSetupBrowserCATrust(ctx context.Context, snapshot *setupSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("browser_ca_trust_failed: setup configuration is missing")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("browser_ca_trust_failed: resolve repository root: %w", err)
	}
	host := snapshot.Manifest.ControlHost()
	// 管理台的 HTTPS 证书由 MooX CA 签发；先经 SSH 从控制机取回 MooX CA 根证书到本机缓存再安装信任，
	// 不依赖本机残留的旧证书副本。
	if err := refreshSetupConsoleCA(ctx, snapshot.Manifest); err != nil {
		return fmt.Errorf("browser_ca_trust_failed: 无法从管理台服务器取回 MooX CA 根证书: %w", err)
	}
	caPath := tlstrust.CAPath(host.Address)
	if !setupHasTerminal() {
		return fmt.Errorf("browser_trust_manual: 这一步要管理员授权，当前没有终端。请在自己的终端运行：%s", manualBrowserTrustCommand(caPath))
	}
	if err := tlstrust.EnsureLocalCATrust(ctx, root, caPath); err != nil {
		return fmt.Errorf("browser_trust_manual: 自动安装失败（%v）。请在自己的终端运行：%s", err, manualBrowserTrustCommand(caPath))
	}
	return nil
}

// setupHasTerminal 在测试里替换。
var setupHasTerminal = func() bool {
	stdin, err := os.Stdin.Stat()
	if err != nil || stdin.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// /dev/null 也是字符设备，Agent 的标准输入常常指向它。
	null, err := os.Stat(os.DevNull)
	return err == nil && !os.SameFile(stdin, null)
}

// manualBrowserTrustCommand 是用户在自己终端运行的信任命令。
func manualBrowserTrustCommand(caPath string) string {
	if runtime.GOOS == "darwin" {
		return "sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain " + caPath
	}
	return "sudo scripts/deploy/install-moox-ca.sh --ca-file " + caPath
}

// refreshSetupConsoleCA 在测试里替换。
var refreshSetupConsoleCA = configinit.RefreshConsoleCA

func defaultSetupApplyWithSpaces(
	ctx context.Context,
	snapshot *setupSnapshot,
	spaces []setupclient.Space,
) (setupclient.ApplyResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).ApplyWithSpaces(ctx, snapshot.Secrets, spaces)
}

func defaultSetupStatusWithSpaces(
	ctx context.Context,
	snapshot *setupSnapshot,
	spaces []setupclient.Space,
) (setupclient.StatusResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).StatusWithSpaces(ctx, snapshot.Secrets, spaces)
}

func dialSetupHost(ctx context.Context, host topology.Host) (setupssh.Client, error) {
	return dialHost(ctx, host)
}

func sshTarget(host topology.Host) setupssh.Target {
	return setupssh.Target{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username}
}

func findSetupHost(manifest *topology.Config, name string) (topology.Host, error) {
	for _, host := range manifest.Hosts() {
		if strings.EqualFold(host.Name, strings.TrimSpace(name)) {
			return host, nil
		}
	}
	return topology.Host{}, fmt.Errorf("setup_host_not_found")
}

func resolveStorageDeploymentHost(manifest *topology.Config, name string) (topology.Host, error) {
	name = strings.TrimSpace(name)
	if name == "" && manifest.StorageHost().Name != "" {
		name = manifest.StorageHost().Name
	}
	host, err := findSetupHost(manifest, name)
	if err != nil {
		return topology.Host{}, err
	}
	if manifest.StorageHost().Name != "" && !strings.EqualFold(host.Name, manifest.StorageHost().Name) {
		return topology.Host{}, fmt.Errorf("storage_host_required")
	}
	if manifest.ComponentHost("storage-view") != "" && !sameHostEndpoint(host, manifest.HostByID(manifest.ComponentHost("storage-view"))) {
		return topology.Host{}, fmt.Errorf("storage_view_hosts_must_share_endpoint")
	}
	return host, nil
}

func sameHostEndpoint(left, right topology.Host) bool {
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

func findSetupTrustHost(manifest *topology.Config, name string) (topology.Host, error) {
	return findSetupHost(manifest, name)
}

func clearSetupSecrets(snapshot *setupSnapshot) {
	if snapshot != nil {
		snapshot.Secrets.Clear()
	}
}

func writeSetupJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
