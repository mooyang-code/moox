package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"reflect"
	"runtime"
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
	load           func(string) (*setupSnapshot, error)
	loadInitBundle func(string) (setupInitBundle, error)
	validate       func(context.Context, *setupSnapshot) (setupvalidate.Result, error)
	trustHost      func(context.Context, *setupSnapshot, string, string) error
	// applySecrets 把秘密文件里的默认腾讯云密钥写进管理台（只建不改）。
	applySecrets          func(context.Context, *setupSnapshot) (setupclient.ApplyResult, error)
	registerCloudAccounts func(context.Context, *setupSnapshot) (*setupCloudAccountSummary, error)
	provisionCLS          func(context.Context, *setupSnapshot) (*setupCLSSummary, error)
	// ensureSCFPlan 的最后一个参数是源码或产物包目录：云函数代码包从它下面的 modules/collector 构建。
	ensureSCFPlan   func(context.Context, *setupSnapshot, string) (*setupSCFPlanSummary, error)
	login           func(context.Context, *setupSnapshot) (setupclient.LoginResult, error)
	openInitStorage func(context.Context, *setupSnapshot, string) (setupInitStorage, error)
	openInitFactor  func(context.Context, *setupSnapshot) (setupInitFactor, error)
	// checkNotification 只读检查 Monitor 是否已配置告警渠道（为空时提醒用户到管理台添加）；测试里替换。
	checkNotification func(context.Context, *setupSnapshot) (*setupNotificationSummary, error)
	// initStorage 的参数依次是存储机、源码或产物包目录、种子。
	initStorage  func(context.Context, *setupSnapshot, string, string, setupInitBundle) (setupInitSummary, error)
	initMetadata func(context.Context, *setupSnapshot, string, setupInitBundle) (setupInitStorageSummary, error)
	// ensureOperatorKey 确保这台电脑的身份目录里有 moox-cli 的签名私钥（已有就复用），返回公钥与是否新生成。
	ensureOperatorKey func(string) (string, bool, error)
	// registerIdentity 经 SSH 在控制机上登记（替换）一个不属于主机的身份的公钥。
	registerIdentity    func(context.Context, *setupSnapshot, string, string) error
	ensureFirewall      func(context.Context, *setupSnapshot) (setupFirewallSummary, error)
	ensureRelayFirewall func(context.Context, *setupSnapshot) (setupFirewallSummary, error)
	// scanDeployment 只读检测目标机器的部署情况；wipeHost 清空一台主机。
	scanDeployment func(context.Context, *setupSnapshot, deployscan.Options) ([]deployscan.HostReport, error)
	wipeHost       func(context.Context, topology.Host, string) (deployscan.HostResult, error)
	// deployHost 部署一台主机；waitReady 等一台主机上的一组组件就绪，返回到时限还没就绪的组件与原因。
	deployHost func(context.Context, deploy.Options) (deploy.Result, error)
	// preflightRender 在不动任何主机前对全部主机与组件做一次纯试渲染（含 [overrides] 应用）。
	preflightRender func(*setupSnapshot, string) error
	waitReady       func(context.Context, topology.Host, []string, time.Duration) (map[string]string, error)
	// operate 对一个组件的用户服务执行 restart、stop、start；readLogs 读组件日志。
	operate   func(context.Context, topology.Host, string, string) (string, error)
	readLogs  func(context.Context, topology.Host, string, []string, string, int, bool) ([]deploy.LogSection, error)
	openStart func(context.Context, string, *setupSnapshot) (setupStartClient, func(), error)
	// runDoctor 是 setup install 的倒数第二步（按空间验收之前），对全部启用主机跑安装后自检，测试里替换。
	runDoctor func(context.Context, string, *setupSnapshot) error
	// verifySpace 是 setup install 的最后一步，测试里替换。
	verifySpace func(context.Context, string, *setupSnapshot, string) error
	// markInstallComplete 在安装验收全部通过后，在控制机部署根下单独写完成标记文件。
	markInstallComplete func(context.Context, *setupSnapshot, string) error
	// previewBackup 只读统计清空前将备份的密钥与告警渠道条数（不落盘、不解密）。
	previewBackup func(context.Context, string, *setupSnapshot) (int, int, error)
	// backupBeforeWipe 在清空前自动导出密钥与告警渠道到 moox.backup（写出时不带待导入标记，overwritePending 允许覆盖已过期的待导入备份）。
	backupBeforeWipe func(context.Context, string, *setupSnapshot, string, bool) (setupBackupOutput, error)
	// restoreBackupSecrets 在控制机 Admin 就绪后、部署其余主机前按覆盖自动导入待恢复密钥（不清除待导入标记）。
	restoreBackupSecrets func(context.Context, string, *setupSnapshot) (*setupRestoreBackupSummary, error)
	// restoreBackupChannels 在全部组件（含 Monitor）就绪之后、完整初始化之前按地址幂等覆盖导入告警渠道，完成后清除待导入标记。
	restoreBackupChannels func(context.Context, string, *setupSnapshot) (*setupRestoreBackupSummary, error)
	regionCloud           func(*setupSnapshot, string) (setupFunctionCloud, error)
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
		newSetupBackupCommand(deps),
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
		newSetupTrustHostCommand(deps),
		newSetupTrustBrowserCommand(deps),
		newSetupPackageComponentsCommand(),
		newSetupFirewallCommand(deps),
	)
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

// completeSetupDeps 是默认依赖的唯一补全入口：测试只替换自己关心的那几项，其余用默认实现。
// 子命令里不要再判空补默认值：那样只改到子命令自己的副本，聚合命令 setup install 拿到的还是空的。
func completeSetupDeps(deps setupDeps) setupDeps {
	defaults := defaultSetupDeps()
	fillSetupDep(&deps.load, defaults.load)
	fillSetupDep(&deps.loadInitBundle, defaults.loadInitBundle)
	fillSetupDep(&deps.validate, defaults.validate)
	fillSetupDep(&deps.trustHost, defaults.trustHost)
	fillSetupDep(&deps.applySecrets, defaults.applySecrets)
	fillSetupDep(&deps.registerCloudAccounts, defaults.registerCloudAccounts)
	fillSetupDep(&deps.provisionCLS, defaults.provisionCLS)
	fillSetupDep(&deps.ensureSCFPlan, defaults.ensureSCFPlan)
	fillSetupDep(&deps.login, defaults.login)
	fillSetupDep(&deps.openInitStorage, defaults.openInitStorage)
	fillSetupDep(&deps.openInitFactor, defaults.openInitFactor)
	fillSetupDep(&deps.checkNotification, defaults.checkNotification)
	fillSetupDep(&deps.ensureOperatorKey, defaults.ensureOperatorKey)
	fillSetupDep(&deps.registerIdentity, defaults.registerIdentity)
	fillSetupDep(&deps.ensureFirewall, defaults.ensureFirewall)
	fillSetupDep(&deps.ensureRelayFirewall, defaults.ensureRelayFirewall)
	fillSetupDep(&deps.scanDeployment, defaults.scanDeployment)
	fillSetupDep(&deps.wipeHost, defaults.wipeHost)
	fillSetupDep(&deps.deployHost, defaults.deployHost)
	fillSetupDep(&deps.preflightRender, defaults.preflightRender)
	fillSetupDep(&deps.waitReady, defaults.waitReady)
	fillSetupDep(&deps.operate, defaults.operate)
	fillSetupDep(&deps.readLogs, defaults.readLogs)
	fillSetupDep(&deps.openStart, defaults.openStart)
	fillSetupDep(&deps.runDoctor, defaults.runDoctor)
	fillSetupDep(&deps.verifySpace, defaults.verifySpace)
	fillSetupDep(&deps.markInstallComplete, defaults.markInstallComplete)
	fillSetupDep(&deps.previewBackup, defaults.previewBackup)
	fillSetupDep(&deps.backupBeforeWipe, defaults.backupBeforeWipe)
	fillSetupDep(&deps.restoreBackupSecrets, defaults.restoreBackupSecrets)
	fillSetupDep(&deps.restoreBackupChannels, defaults.restoreBackupChannels)
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
		load:      loadSetupSnapshot,
		runDoctor: defaultSetupDoctor,
		verifySpace: func(ctx context.Context, file string, snapshot *setupSnapshot, spaceID string) error {
			_, err := runSetupVerifySpace(ctx, file, snapshot, spaceID, setupVerifyTimeout)
			return err
		},
		markInstallComplete:   defaultSetupMarkInstallComplete,
		previewBackup:         defaultSetupPreviewBackup,
		backupBeforeWipe:      defaultSetupBackupBeforeWipe,
		restoreBackupSecrets:  defaultSetupRestoreBackupSecrets,
		restoreBackupChannels: defaultSetupRestoreBackupChannels,
		regionCloud:           defaultSetupRegionCloud,
		loadInitBundle:        loadSetupInitBundle,
		scanDeployment:        defaultSetupScanDeployment,
		wipeHost:              defaultSetupWipeHost,
		deployHost:            deploy.Deploy,
		preflightRender:       defaultSetupPreflightRender,
		waitReady:             defaultSetupWaitReady,
		operate:               defaultSetupOperate,
		readLogs:              defaultSetupReadLogs,
		openStart:             defaultSetupOpenStart,
		validate:              defaultSetupValidate,
		trustHost:             defaultSetupTrustHost,
		applySecrets:          defaultSetupApplySecrets,
		registerCloudAccounts: defaultSetupRegisterCloudAccounts,
		provisionCLS:          defaultSetupProvisionCLS,
		ensureSCFPlan:         defaultSetupEnsureSCFPlan,
		openInitStorage:       defaultOpenSetupInitStorage,
		openInitFactor: func(ctx context.Context, snapshot *setupSnapshot) (setupInitFactor, error) {
			return defaultOpenSetupFactor(ctx, snapshot.Manifest)
		},
		checkNotification:   defaultSetupCheckNotification,
		ensureOperatorKey:   defaultSetupEnsureOperatorKey,
		registerIdentity:    defaultSetupRegisterIdentity,
		ensureFirewall:      defaultSetupEnsureFirewall,
		ensureRelayFirewall: setupEnsureRelayFirewall,
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

func defaultSetupPreflightRender(snapshot *setupSnapshot, sourceRoot string) error {
	if snapshot == nil || snapshot.Manifest == nil {
		return fmt.Errorf("缺少拓扑配置")
	}
	return deploy.PreflightRender(snapshot.Manifest, snapshot.Secrets, sourceRoot)
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

func defaultSetupApplySecrets(ctx context.Context, snapshot *setupSnapshot) (setupclient.ApplyResult, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot.Manifest)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer gateway.Close()
	return setupclient.New(gateway).Apply(ctx, snapshot.Secrets)
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
