package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/privatenet"
	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
)

const defaultSetupFile = "./moox.toml"

// setupDeps 是 setup 命令的外部依赖，测试时替换。
type setupDeps struct {
	load                  func(string) (*setupconfig.Snapshot, error)
	loadInitBundle        func(string) (setupInitBundle, error)
	validate              func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error)
	trustHost             func(context.Context, *setupconfig.Snapshot, string, string) error
	openDeployer          func(*setupconfig.Snapshot, string, io.Writer, bool) (setupDeployer, func(), error)
	apply                 func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error)
	status                func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error)
	applySpaces           func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.ApplyResult, error)
	statusSpaces          func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.StatusResult, error)
	registerCloudAccounts func(context.Context, *setupconfig.Snapshot) (*setupCloudAccountSummary, error)
	login                 func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error)
	openInitStorage       func(context.Context, *setupconfig.Snapshot, string) (setupInitStorage, error)
	openInitFactor        func(context.Context, *setupconfig.Snapshot) (setupInitFactor, error)
	initStorage           func(context.Context, *setupconfig.Snapshot, string, string, string, setupInitBundle) (setupInitSummary, error)
	exportSkillConfig     func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error)
	ensureFirewall        func(context.Context, *setupconfig.Snapshot) (setupFirewallSummary, error)
	ensurePrivateNetwork  func(context.Context, *setupconfig.Snapshot, privatenet.Options, io.Writer) (privatenet.Result, error)
	resolveSCFRoutes      func(context.Context, *setupconfig.Snapshot, string) (privatenet.SCFRoutePlan, error)
}

func init() {
	rootCmd.AddCommand(newSetupCommand(defaultSetupDeps()))
}

func newSetupCommand(deps setupDeps) *cobra.Command {
	deps = completeSetupDeps(deps)
	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "部署与初始化 MooX",
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newSetupHostsCommand(deps),
		newSetupValidateCommand(deps),
		newSetupTrustHostCommand(deps),
		newSetupTrustBrowserCommand(deps),
		newSetupBootstrapCommand(deps),
		newSetupDeployHostCommand(deps),
		newSetupDeployServiceCommand(deps),
		newSetupRollbackCommand(deps),
		newSetupPauseCommand(deps),
		newSetupResumeCommand(deps),
		newSetupServiceCommand(deps),
		newSetupRenderCommand(deps),
		newSetupExportStateCommand(deps),
		newSetupBuildLinuxCommand(deps),
		newSetupApplyCommand(deps),
		newSetupStatusCommand(deps),
		newSetupInitCommand(deps),
		newSetupFactorsCommand(deps),
		newSetupRebootHostCommand(deps),
		newSetupHostDiagnosticsCommand(deps),
		newSetupInspectSCFCommand(deps),
		newSetupAttachSCFVPCCommand(deps),
		newSetupInvokeSCFCommand(deps),
		newSetupExportSkillConfigCommand(deps),
		newSetupFirewallCommand(deps),
		newSetupPrivateNetworkCommand(deps),
		newSetupSCFNetworkPlanCommand(deps),
	)
	return cmd
}

// newSetupRebootHostCommand 重启无响应的腾讯云主机。先按公网地址查到实例的实际地域，再调用对应的重启接口
// （主机所在地域可以与 moox.toml 的默认地域不同）。
func newSetupRebootHostCommand(deps setupDeps) *cobra.Command {
	var file, hostID string
	cmd := &cobra.Command{
		Use:   "reboot-host",
		Short: "重启无响应的腾讯云主机",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostID)
			if err != nil {
				return err
			}
			if host.Provider != "tencent" {
				return fmt.Errorf("主机 %s 不是腾讯云主机，不能用这个命令重启", host.ID)
			}
			options := cloudtencent.ClientOptions{
				SecretID:  snapshot.Manifest.TencentCloud.SecretID,
				SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
				Region:    snapshot.Manifest.TencentCloud.Region,
			}
			network, err := cloudtencent.NewNetworkClient(options)
			if err != nil {
				return err
			}
			lighthouse, err := cloudtencent.NewClient(options)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			instance, err := (privatenet.TencentCloud{Network: network, Lighthouse: lighthouse}).LookupHost(ctx, host.Address, cloudtencent.DefaultProbeRegions)
			if err != nil {
				return fmt.Errorf("查找主机 %s 的腾讯云实例: %w", host.ID, err)
			}
			var requestID string
			switch instance.Kind {
			case cloudtencent.KindLighthouse:
				requestID, err = lighthouse.ForRegion(instance.Region).RebootInstance(ctx, instance.InstanceID)
			case cloudtencent.KindCVM:
				options.Region = instance.Region
				cvm, cvmErr := cloudtencent.NewCVMClient(options)
				if cvmErr != nil {
					return cvmErr
				}
				requestID, err = cvm.RebootInstance(ctx, instance.InstanceID)
			default:
				return fmt.Errorf("不支持的实例类型 %q", instance.Kind)
			}
			if err != nil {
				return fmt.Errorf("重启主机 %s: %w", host.ID, err)
			}
			return writeSetupJSON(cmd, map[string]any{
				"status": "reboot_requested", "host": host.ID,
				"instance_id": instance.InstanceID, "region": instance.Region,
				"zone": instance.Zone, "request_id": requestID,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", "", "主机 ID")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

// newSetupHostDiagnosticsCommand 查看主机的磁盘、MooX 进程、数据目录大小和各组件最近的错误日志。
func newSetupHostDiagnosticsCommand(deps setupDeps) *cobra.Command {
	var file, hostID string
	cmd := &cobra.Command{
		Use:   "host-diagnostics",
		Short: "检查远端主机磁盘和服务进程",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			host, err := findSetupHost(snapshot.Manifest, hostID)
			if err != nil {
				return err
			}
			transport, err := dialSetupHost(cmd.Context(), host)
			if err != nil {
				return err
			}
			defer transport.Close()
			result, err := transport.Run(cmd.Context(), []string{"bash", "-c", `set -u
root="$1"
printf 'df:\n'
df -h "$root" / 2>&1
printf 'releases:\n'
readlink "$root/current" 2>&1
ls -1 "$root/releases" 2>/dev/null | tail -5
printf 'paused:\n'
ls -1 "$root/run/paused" 2>/dev/null
printf 'processes:\n'
ps -eo pid,etime,stat,args | grep -E "$root/releases/[^/]+/bin/" | grep -v grep
printf 'sizes:\n'
du -xh --max-depth=1 "$root/data" "$root/logs" 2>/dev/null | sort -h | tail -40
printf 'recent errors:\n'
for log in "$root"/logs/*/*.log; do
  [ -f "$log" ] || continue
  if grep -qiE 'error|panic|fatal' "$log" 2>/dev/null; then
    echo "--- $log"
    grep -iE 'error|panic|fatal' "$log" | tail -10
  fi
done
exit 0`, "moox-host-diagnostics", host.Root}, nil)
			if err != nil {
				return fmt.Errorf("检查主机 %s 失败: %w", host.ID, err)
			}
			return writeSetupJSON(cmd, map[string]any{"host": host.ID, "root": host.Root, "diagnostics": result.Stdout})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&hostID, "host", servicecatalog.ControlHostID, "主机 ID")
	return cmd
}

// setupHostChoice 是 setup hosts 输出的一台主机（不含密码）。编译主机的 role 为 compile，
// 其余主机的 role 为 host，components 是部署表中的组件。
type setupHostChoice struct {
	Name       string   `json:"name"`
	Address    string   `json:"address"`
	Port       int      `json:"port"`
	Username   string   `json:"username"`
	Provider   string   `json:"provider,omitempty"`
	Role       string   `json:"role"`
	Root       string   `json:"root,omitempty"`
	Components []string `json:"components,omitempty"`
}

func newSetupHostsCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "hosts", Short: "列出 moox.toml 中的主机与编译主机（不输出密码）", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		manifest := snapshot.Manifest
		hosts := make([]setupHostChoice, 0, len(manifest.Hosts)+1)
		for _, host := range manifest.HostList() {
			hosts = append(hosts, setupHostChoice{
				Name: host.ID, Address: host.Address, Port: host.SSH.Port, Username: host.SSH.Username,
				Provider: host.Provider, Role: "host", Root: host.Root, Components: manifest.Components(host.ID),
			})
		}
		if manifest.CompileHost.Configured() {
			compile := manifest.CompileHost
			hosts = append(hosts, setupHostChoice{
				Name: "compile", Address: compile.Address, Port: compile.SSH.Port, Username: compile.SSH.Username,
				Provider: compile.Provider, Role: "compile",
			})
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
	cmd := &cobra.Command{Use: "validate", Short: "校验 moox.toml、腾讯云凭据和全部主机的 SSH 连接", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.validate(cmd.Context(), snapshot)
		if encodeErr := writeSetupJSON(cmd, result); encodeErr != nil {
			return encodeErr
		}
		return err
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
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]string{"host": host, "status": "trusted"})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", servicecatalog.ControlHostID, "主机 ID；编译主机为 compile")
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
			host := setupConsoleHost(snapshot.Manifest)
			result := map[string]any{"host": host.ID, "status": "trusted"}
			if !setupdeploy.RequiresLocalCATrust(host) {
				result["status"] = "not_required"
			} else {
				if err := ensureSetupBrowserCATrust(cmd.Context(), snapshot); err != nil {
					return err
				}
				result["ca_path"] = setupdeploy.CAPath(host.Address)
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
	cmd := &cobra.Command{Use: "apply", Short: "写入初始用户、云凭据和 SSH 主机", RunE: func(cmd *cobra.Command, _ []string) error {
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
	if deps.openDeployer == nil {
		deps.openDeployer = defaults.openDeployer
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
	if deps.exportSkillConfig == nil {
		deps.exportSkillConfig = defaults.exportSkillConfig
	}
	if deps.ensureFirewall == nil {
		deps.ensureFirewall = defaults.ensureFirewall
	}
	if deps.ensurePrivateNetwork == nil {
		deps.ensurePrivateNetwork = defaults.ensurePrivateNetwork
	}
	if deps.resolveSCFRoutes == nil {
		deps.resolveSCFRoutes = defaults.resolveSCFRoutes
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
		validate:              defaultSetupValidate,
		trustHost:             defaultSetupTrustHost,
		openDeployer:          defaultOpenSetupDeployer,
		apply:                 defaultSetupApply,
		status:                defaultSetupStatus,
		applySpaces:           defaultSetupApplyWithSpaces,
		statusSpaces:          defaultSetupStatusWithSpaces,
		registerCloudAccounts: defaultSetupRegisterCloudAccounts,
		openInitStorage:       defaultOpenSetupInitStorage,
		openInitFactor:        defaultOpenSetupFactor,
		exportSkillConfig:     defaultSetupExportSkillConfig,
		ensureFirewall:        defaultSetupEnsureFirewall,
		ensurePrivateNetwork:  defaultEnsurePrivateNetwork,
		resolveSCFRoutes:      resolveSCFRoutePlan,
		login:                 defaultSetupLogin,
	}
}

// defaultSetupLogin 用 moox.toml 中的管理员账号登录控制台，确认浏览器入口可用。
func defaultSetupLogin(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.LoginResult, error) {
	host := setupConsoleHost(snapshot.Manifest)
	baseURL := fmt.Sprintf("https://%s:9527", host.Address)
	if err := ensureSetupBrowserCATrust(ctx, snapshot); err != nil {
		return setupclient.LoginResult{}, err
	}
	admin := snapshot.Manifest.Admin
	if !setupdeploy.RequiresLocalCATrust(host) {
		return setupclient.VerifyPublicLogin(ctx, baseURL, admin.Username, admin.Password)
	}
	return setupclient.VerifyPublicLoginWithCAFile(ctx, baseURL, admin.Username, admin.Password, setupdeploy.CAPath(host.Address))
}

// setupConsoleHost 返回部署了控制台代理的主机，部署表没有时为 control。
func setupConsoleHost(manifest setupconfig.Manifest) setupconfig.Host {
	if hosts := manifest.HostsOf("console-proxy"); len(hosts) > 0 {
		host, _ := manifest.Host(hosts[0])
		return host
	}
	return manifest.ControlHost()
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
	target, err := setupTrustTarget(snapshot.Manifest, name)
	if err != nil {
		return err
	}
	return setupssh.TrustHost(ctx, target, fingerprint, setupssh.Options{Timeout: 15 * time.Second})
}

// setupTrustTarget 返回要记录指纹的 SSH 目标：主机 ID，或编译主机 compile。
func setupTrustTarget(manifest setupconfig.Manifest, name string) (setupssh.Target, error) {
	if strings.TrimSpace(name) == "compile" && manifest.CompileHost.Configured() {
		compile := manifest.CompileHost
		return setupssh.Target{Name: "compile", Address: compile.Address, Port: compile.SSH.Port, Username: compile.SSH.Username}, nil
	}
	host, err := findSetupHost(manifest, name)
	if err != nil {
		return setupssh.Target{}, err
	}
	return setupssh.HostTarget(host), nil
}

func ensureSetupBrowserCATrust(ctx context.Context, snapshot *setupconfig.Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("browser_ca_trust_failed: 缺少 moox.toml")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("browser_ca_trust_failed: 读取仓库目录: %w", err)
	}
	return setupdeploy.EnsureLocalCATrust(ctx, root, setupConsoleHost(snapshot.Manifest))
}

func defaultSetupApply(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.ApplyResult, error) {
	controlGateway, err := openControlGateway(snapshot.Manifest)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer controlGateway.Close()
	return setupclient.New(controlGateway).Apply(ctx, snapshot)
}

func defaultSetupStatus(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.StatusResult, error) {
	controlGateway, err := openControlGateway(snapshot.Manifest)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer controlGateway.Close()
	return setupclient.New(controlGateway).Status(ctx, snapshot)
}

func defaultSetupApplyWithSpaces(ctx context.Context, snapshot *setupconfig.Snapshot, spaces []setupclient.Space) (setupclient.ApplyResult, error) {
	controlGateway, err := openControlGateway(snapshot.Manifest)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer controlGateway.Close()
	return setupclient.New(controlGateway).ApplyWithSpaces(ctx, snapshot, spaces)
}

func defaultSetupStatusWithSpaces(ctx context.Context, snapshot *setupconfig.Snapshot, spaces []setupclient.Space) (setupclient.StatusResult, error) {
	controlGateway, err := openControlGateway(snapshot.Manifest)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer controlGateway.Close()
	return setupclient.New(controlGateway).StatusWithSpaces(ctx, snapshot, spaces)
}

// dialSetupHost 用 moox.toml 中的 SSH 口令连接一台主机；测试时替换。
var dialSetupHost = func(ctx context.Context, host setupconfig.Host) (setupssh.Client, error) {
	return setupssh.DialHost(ctx, host, setupssh.Options{Timeout: 15 * time.Second})
}

func findSetupHost(manifest setupconfig.Manifest, id string) (setupconfig.Host, error) {
	if host, ok := manifest.Host(id); ok {
		return host, nil
	}
	return setupconfig.Host{}, fmt.Errorf("moox.toml 中没有主机 %q（主机 ID 见 [hosts.<主机 ID>]）", strings.TrimSpace(id))
}

// clearSetupSecrets 在命令结束时清除内存中的口令与凭据。
func clearSetupSecrets(snapshot *setupconfig.Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.Manifest.Admin.Password = ""
	snapshot.Manifest.TencentCloud.SecretID = ""
	snapshot.Manifest.TencentCloud.SecretKey = ""
	snapshot.Manifest.Notification.WebhookURL = ""
	snapshot.Manifest.CompileHost.SSH.Password = ""
	for id, host := range snapshot.Manifest.Hosts {
		host.SSH.Password = ""
		snapshot.Manifest.Hosts[id] = host
	}
}

func writeSetupJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
