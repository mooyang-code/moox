package command

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupdeploy "github.com/mooyang-code/moox/modules/cli/internal/setup/deploy"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	setupvalidate "github.com/mooyang-code/moox/modules/cli/internal/setup/validate"
	cloudtencent "github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/spf13/cobra"
)

const defaultSetupFile = "./custom.toml"

type setupDeps struct {
	load               func(string) (*setupconfig.Snapshot, error)
	loadInitBundle     func(string) (setupInitBundle, error)
	validate           func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error)
	validateDeployment func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error)
	trustHost          func(context.Context, *setupconfig.Snapshot, string, string) error
	deployControl      func(context.Context, *setupconfig.Snapshot, bool) error
	deployService      func(context.Context, *setupconfig.Snapshot, string, string, string, string) (setupdeploy.ServiceResult, error)
	apply              func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error)
	status             func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error)
	applySpaces        func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.ApplyResult, error)
	statusSpaces       func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.StatusResult, error)
	login              func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error)
	openInitStorage    func(context.Context, *setupconfig.Snapshot, string) (setupInitStorage, error)
	deployStorage      func(context.Context, *setupconfig.Snapshot, string, bool) error
	importMetadata     func(context.Context, *setupconfig.Snapshot, string, string, []string) (metadataImportSummary, error)
	verifyStorage      func(context.Context, *setupconfig.Snapshot, string) (storageVerifyResult, error)
	e2eStorage         func(context.Context, *setupconfig.Snapshot, string, string) (storageE2EResult, error)
	browserE2EStorage  func(context.Context, *setupconfig.Snapshot, string, string) (storageBrowserResult, error)
	e2eEventBus        func(context.Context, *setupconfig.Snapshot) (eventBusE2EResult, error)
}

func init() {
	rootCmd.AddCommand(newSetupCommand(defaultSetupDeps()))
}

func newSetupCommand(deps setupDeps) *cobra.Command {
	deps = completeSetupDeps(deps)
	cmd := &cobra.Command{
		Use:          "setup",
		Short:        "初始化 MooX 控制面",
		SilenceUsage: true,
	}
	cmd.AddCommand(
		newSetupInitCommand(deps),
		newSetupHostsCommand(deps),
		newSetupValidateCommand(deps),
		newSetupTrustHostCommand(deps),
		newSetupDeployCommand(deps),
		newSetupDeployServiceCommand(deps),
		newSetupApplyCommand(deps),
		newSetupStatusCommand(deps),
		newSetupDeployStorageCommand(deps),
		newSetupMetadataImportCommand(deps),
		newSetupVerifyStorageCommand(deps),
		newSetupE2EStorageCommand(deps),
		newSetupBrowserE2EStorageCommand(deps),
		newSetupE2EEventBusCommand(deps),
	)
	return cmd
}

type setupHostChoice struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
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
		for index, host := range snapshot.Manifest.Hosts() {
			role := "other"
			if index == 0 {
				role = "control"
			}
			hosts = append(hosts, setupHostChoice{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username, Role: role})
		}
		if snapshot.Manifest.HasCompileHost() {
			host := snapshot.Manifest.CompileHost
			hosts = append(hosts, setupHostChoice{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username, Role: "compile"})
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
	cmd := &cobra.Command{Use: "validate", Short: "校验初始化配置和连接", RunE: func(cmd *cobra.Command, _ []string) error {
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
	cmd.Flags().StringVar(&host, "host", "control", "主机名称")
	cmd.Flags().StringVar(&fingerprint, "fingerprint", "", "已核验的 SHA256 指纹")
	_ = cmd.MarkFlagRequired("fingerprint")
	return cmd
}

func newSetupDeployCommand(deps setupDeps) *cobra.Command {
	var file string
	var resetData bool
	cmd := &cobra.Command{Use: "deploy-control", Short: "部署 Admin、Gateway 和 Web", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, validationErr := deps.validateDeployment(cmd.Context(), snapshot, []setupconfig.Host{snapshot.Manifest.ControlHost})
		if validationErr != nil {
			if encodeErr := writeSetupJSON(cmd, result); encodeErr != nil {
				return encodeErr
			}
			return validationErr
		}
		if err := deps.deployControl(cmd.Context(), snapshot, resetData); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]any{"host": snapshot.Manifest.ControlHost.Name, "status": "ready", "reset_data": resetData})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().BoolVar(&resetData, "reset-data", false, "删除控制面现有数据后重新部署")
	return cmd
}

func newSetupDeployServiceCommand(deps setupDeps) *cobra.Command {
	var file, host, packagePath, service, deployDir string
	cmd := &cobra.Command{Use: "deploy-service", Short: "通过 SSH 发布并校验完整服务包", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		result, err := deps.deployService(cmd.Context(), snapshot, host, packagePath, service, deployDir)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "control", "目标主机名称")
	cmd.Flags().StringVar(&packagePath, "package", "", "本地服务 ZIP 包路径")
	cmd.Flags().StringVar(&service, "service", "", "远端服务名称")
	cmd.Flags().StringVar(&deployDir, "deploy-dir", "~/moox/prod", "远端部署目录")
	_ = cmd.MarkFlagRequired("package")
	_ = cmd.MarkFlagRequired("service")
	return cmd
}

func newSetupApplyCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{Use: "apply", Short: "写入初始用户、云凭据和主机", RunE: func(cmd *cobra.Command, _ []string) error {
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

func newSetupDeployStorageCommand(deps setupDeps) *cobra.Command {
	var file, host string
	var resetStorageData bool
	cmd := &cobra.Command{Use: "deploy-storage", Short: "将 Storage 组件部署到用户选择的主机", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		storageHost, err := findSetupHost(snapshot.Manifest, host)
		if err != nil {
			return err
		}
		result, validationErr := deps.validateDeployment(cmd.Context(), snapshot, []setupconfig.Host{snapshot.Manifest.ControlHost, storageHost})
		if validationErr != nil {
			if encodeErr := writeSetupJSON(cmd, result); encodeErr != nil {
				return encodeErr
			}
			return validationErr
		}
		status, err := deps.status(cmd.Context(), snapshot)
		if err != nil || status.State != "completed" {
			return fmt.Errorf("setup_incomplete")
		}
		if err := deps.deployStorage(cmd.Context(), snapshot, host, resetStorageData); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]any{"host": host, "status": "ready", "reset_storage_data": resetStorageData})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "Storage 目标主机名称")
	cmd.Flags().BoolVar(&resetStorageData, "reset-storage-data", false, "仅用于已确认的破坏性 Schema 切换，清空旧 Storage data")
	_ = cmd.MarkFlagRequired("host")
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
	cmd.Flags().StringVar(&seed, "seed", "examples/setup/default/metadata.yaml", "metadata seed YAML")
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
	if deps.validateDeployment == nil {
		deps.validateDeployment = defaults.validateDeployment
	}
	if deps.trustHost == nil {
		deps.trustHost = defaults.trustHost
	}
	if deps.deployControl == nil {
		deps.deployControl = defaults.deployControl
	}
	if deps.deployService == nil {
		deps.deployService = defaults.deployService
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
	if deps.login == nil {
		deps.login = defaults.login
	}
	if deps.openInitStorage == nil {
		deps.openInitStorage = defaults.openInitStorage
	}
	if deps.deployStorage == nil {
		deps.deployStorage = defaults.deployStorage
	}
	if deps.importMetadata == nil {
		deps.importMetadata = defaults.importMetadata
	}
	if deps.verifyStorage == nil {
		deps.verifyStorage = defaults.verifyStorage
	}
	if deps.e2eStorage == nil {
		deps.e2eStorage = defaults.e2eStorage
	}
	if deps.browserE2EStorage == nil {
		deps.browserE2EStorage = defaults.browserE2EStorage
	}
	if deps.e2eEventBus == nil {
		deps.e2eEventBus = defaults.e2eEventBus
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
		loadInitBundle:     loadSetupInitBundle,
		validate:           defaultSetupValidate,
		validateDeployment: defaultSetupValidateDeployment,
		trustHost:          defaultSetupTrustHost,
		deployControl:      defaultSetupDeploy,
		deployService:      defaultSetupDeployService,
		apply:              defaultSetupApply,
		status:             defaultSetupStatus,
		applySpaces:        defaultSetupApplyWithSpaces,
		statusSpaces:       defaultSetupStatusWithSpaces,
		openInitStorage:    defaultOpenSetupInitStorage,
		deployStorage:      defaultSetupDeployStorage,
		importMetadata:     defaultSetupImportMetadata,
		verifyStorage:      defaultSetupVerifyStorage,
		e2eStorage:         defaultSetupE2EStorage,
		browserE2EStorage:  defaultSetupBrowserE2EStorage,
		e2eEventBus:        defaultSetupE2EEventBus,
		login: func(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			baseURL := fmt.Sprintf("https://%s:9527", snapshot.Manifest.ControlHost.Address)
			if setupdeploy.UsesPublicTLS(snapshot.Manifest.ControlHost.Address) {
				return setupclient.VerifyPublicLogin(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password)
			}
			return setupclient.VerifyPublicLoginWithCAFile(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password, setupdeploy.CAPath(snapshot.Manifest.ControlHost.Address))
		},
	}
}

func defaultSetupDeployStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string, resetStorageData bool) error {
	host, err := findSetupHost(snapshot.Manifest, name)
	if err != nil {
		return err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return err
	}
	defer transport.Close()
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return err
	}
	defer control.Close()
	primarySecret, viewSecret, err := controlStorageInternalAuth(ctx, control)
	if err != nil {
		return err
	}
	healthVersion, healthAccessKey, healthSecret, err := controlHealthAuth(ctx, control)
	if err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("storage_deploy_invalid")
	}
	useControlGateway := host.Name == snapshot.Manifest.ControlHost.Name
	if err := setupdeploy.Storage(ctx, transport, setupdeploy.Options{
		RepositoryRoot: root, PublicHost: host.Address, NodeID: host.Name, ResetStorageData: resetStorageData,
		UseControlGateway:     useControlGateway,
		EventBusPublicAddress: snapshot.Manifest.EventBus.PublicAddress,
		EventBusPort:          snapshot.Manifest.EventBus.Port,
		EventBusTLSEnabled:    snapshot.Manifest.EventBus.TLSEnabled,
		StoragePrimarySecret:  primarySecret,
		StorageViewSecret:     viewSecret,
		HealthAuthVersion:     healthVersion,
		HealthAuthAccessKey:   healthAccessKey,
		HealthAuthSecretKey:   healthSecret,
	}, setupdeploy.Dependencies{}); err != nil {
		return err
	}
	placementHost := host.Address
	if useControlGateway {
		placementHost = "127.0.0.1"
	}
	if _, err = setupclient.New(control).ApplyStoragePlacement(ctx, placementHost); err != nil {
		return err
	}
	return restartStorageClients(ctx, control)
}

func controlHealthAuth(ctx context.Context, control setupssh.Client) (string, string, string, error) {
	if control == nil {
		return "", "", "", fmt.Errorf("health_secret_prepare_failed")
	}
	result, err := control.Run(ctx, []string{
		"sh", "-lc", `set -eu
secret_file="$HOME/moox/prod/secrets/health-auth.env"
test -s "${secret_file}"
awk '/^MOOX_HEALTH_AUTH_(VERSION|ACCESS_KEY|SECRET_KEY)=/{print}' "${secret_file}"`,
	}, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("health_secret_prepare_failed")
	}
	normalized, err := normalizeHealthAuth(result.Stdout)
	if err != nil {
		return "", "", "", fmt.Errorf("health_secret_prepare_failed")
	}
	values := make(map[string]string, 3)
	for _, line := range strings.Split(normalized, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return values["MOOX_HEALTH_AUTH_VERSION"], values["MOOX_HEALTH_AUTH_ACCESS_KEY"], values["MOOX_HEALTH_AUTH_SECRET_KEY"], nil
}

func controlStorageInternalAuth(ctx context.Context, control setupssh.Client) (string, string, error) {
	if control == nil {
		return "", "", fmt.Errorf("storage_secret_prepare_failed")
	}
	result, err := control.Run(ctx, []string{
		"sh", "-lc", `set -eu
secret_file="$HOME/moox/prod/secrets/storage-internal-auth.env"
test -s "${secret_file}"
awk '/^MOOX_STORAGE_(PRIMARY|VIEW)_AUTH_SECRET=/{print}' "${secret_file}"`,
	}, nil)
	if err != nil {
		return "", "", fmt.Errorf("storage_secret_prepare_failed")
	}
	secretEnv, err := normalizeStorageInternalAuth(result.Stdout)
	if err != nil {
		return "", "", fmt.Errorf("storage_secret_prepare_failed")
	}
	values := make(map[string]string, 2)
	for _, line := range strings.Split(secretEnv, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	return values["MOOX_STORAGE_PRIMARY_AUTH_SECRET"], values["MOOX_STORAGE_VIEW_AUTH_SECRET"], nil
}

func restartStorageClients(ctx context.Context, control setupssh.Client) error {
	if control == nil {
		return fmt.Errorf("storage_client_restart_failed")
	}
	if _, err := control.Run(ctx, []string{
		"sh", "-lc",
		`set -eu
for service in monitor cloudnode; do
  if "$HOME/moox/prod/status.sh" "$service" >/dev/null 2>&1; then
    "$HOME/moox/prod/restart.sh" "$service"
  fi
done`,
	}, nil); err != nil {
		return fmt.Errorf("storage_client_restart_failed")
	}
	return nil
}

var storageSecretValuePattern = regexp.MustCompile(`^[A-Za-z0-9._~+/=-]+$`)

func normalizeHealthAuth(raw string) (string, error) {
	keys := []string{
		"MOOX_HEALTH_AUTH_VERSION",
		"MOOX_HEALTH_AUTH_ACCESS_KEY",
		"MOOX_HEALTH_AUTH_SECRET_KEY",
	}
	return normalizeSecretEnv(raw, keys)
}

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
	host, err := findSetupHost(snapshot.Manifest, hostName)
	if err != nil {
		return metadataImportSummary{}, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return metadataImportSummary{}, err
	}
	defer transport.Close()
	forwardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := transport.ForwardLocal(forwardContext, "127.0.0.1:20200")
	if err != nil {
		return metadataImportSummary{}, fmt.Errorf("storage_not_reachable")
	}
	defer listener.Close()
	return runMetadataImport(ctx, "http://"+listener.Addr().String(), calls, true)
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

func defaultSetupValidateDeployment(ctx context.Context, snapshot *setupconfig.Snapshot, hosts []setupconfig.Host) (setupvalidate.Result, error) {
	return setupvalidate.RunSSHHosts(ctx, snapshot, setupvalidate.Dependencies{SSH: commandSSHChecker{}}, hosts)
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

func defaultSetupDeploy(ctx context.Context, snapshot *setupconfig.Snapshot, resetData bool) error {
	return runSetupControlDeploySteps(
		func() error {
			if !setupdeploy.UsesPublicTLS(snapshot.Manifest.ControlHost.Address) {
				return nil
			}
			return ensureSetupControlFirewall(ctx, snapshot)
		},
		func() error { return deploySetupControl(ctx, snapshot, resetData) },
		func() error { return ensureSetupEventBusFirewall(ctx, snapshot) },
	)
}

func runSetupControlDeploySteps(ensureControl, deploy, ensureEventBus func() error) error {
	if err := ensureControl(); err != nil {
		return err
	}
	if err := deploy(); err != nil {
		return err
	}
	return ensureEventBus()
}

func deploySetupControl(ctx context.Context, snapshot *setupconfig.Snapshot, resetData bool) error {
	host := snapshot.Manifest.ControlHost
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return err
	}
	defer transport.Close()
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("control_deploy_invalid")
	}
	opts := controlDeployOptions(snapshot, root)
	opts.ResetControlData = resetData
	if err := setupdeploy.Control(ctx, transport, opts, setupdeploy.Dependencies{}); err != nil {
		return err
	}
	return nil
}

func ensureSetupFirewallRules(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	address string,
	rules []cloudtencent.CreateFirewallRulesOptions,
	failure string,
) error {
	publicIP, err := eventBusFirewallIP(ctx, address, net.DefaultResolver.LookupIP)
	if err != nil {
		return fmt.Errorf("%s", failure)
	}
	client, err := cloudtencent.NewClient(cloudtencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region: snapshot.Manifest.TencentCloud.Region,
	})
	if err != nil {
		return fmt.Errorf("%s", failure)
	}
	for _, rule := range rules {
		if _, err := client.EnsureFirewallRule(ctx, publicIP, rule); err != nil {
			return fmt.Errorf("%s", failure)
		}
	}
	return nil
}

func ensureSetupControlFirewall(ctx context.Context, snapshot *setupconfig.Snapshot) error {
	return ensureSetupFirewallRules(ctx, snapshot, snapshot.Manifest.ControlHost.Address, setupControlFirewallRules(), "control_firewall_failed")
}

func ensureSetupEventBusFirewall(ctx context.Context, snapshot *setupconfig.Snapshot) error {
	return ensureSetupFirewallRules(
		ctx,
		snapshot,
		snapshot.Manifest.EventBus.PublicAddress,
		setupRuntimeFirewallRules(snapshot.Manifest.EventBus.Port),
		"eventbus_firewall_failed",
	)
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

func setupRuntimeFirewallRules(eventBusPort int) []cloudtencent.CreateFirewallRulesOptions {
	return []cloudtencent.CreateFirewallRulesOptions{
		{
			Protocol: "TCP", Ports: fmt.Sprint(eventBusPort), CidrBlock: "0.0.0.0/0",
			Action: "ACCEPT", Description: "MooX EventBus TLS",
		},
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

func controlDeployOptions(snapshot *setupconfig.Snapshot, repositoryRoot string) setupdeploy.Options {
	return setupdeploy.Options{
		RepositoryRoot:         repositoryRoot,
		PublicHost:             snapshot.Manifest.ControlHost.Address,
		BrowserPort:            9527,
		EventBusPublicAddress:  snapshot.Manifest.EventBus.PublicAddress,
		EventBusPort:           snapshot.Manifest.EventBus.Port,
		EventBusTLSEnabled:     snapshot.Manifest.EventBus.TLSEnabled,
		MonitoringWeComWebhook: snapshot.Manifest.Monitoring.WeComWebhook,
	}
}

func defaultSetupDeployService(ctx context.Context, snapshot *setupconfig.Snapshot, hostName, packagePath, service, deployDir string) (setupdeploy.ServiceResult, error) {
	host, err := findSetupHost(snapshot.Manifest, hostName)
	if err != nil {
		return setupdeploy.ServiceResult{}, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return setupdeploy.ServiceResult{}, err
	}
	defer transport.Close()
	return setupdeploy.Service(ctx, transport, setupdeploy.ServiceOptions{
		PackagePath: packagePath, ServiceName: service, DeployDir: deployDir,
	})
}

func defaultSetupApply(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.ApplyResult, error) {
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer transport.Close()
	return setupclient.New(transport).Apply(ctx, snapshot)
}

func defaultSetupStatus(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.StatusResult, error) {
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer transport.Close()
	return setupclient.New(transport).Status(ctx, snapshot)
}

func defaultSetupApplyWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []setupclient.Space,
) (setupclient.ApplyResult, error) {
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return setupclient.ApplyResult{}, err
	}
	defer transport.Close()
	return setupclient.New(transport).ApplyWithSpaces(ctx, snapshot, spaces)
}

func defaultSetupStatusWithSpaces(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	spaces []setupclient.Space,
) (setupclient.StatusResult, error) {
	transport, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return setupclient.StatusResult{}, err
	}
	defer transport.Close()
	return setupclient.New(transport).StatusWithSpaces(ctx, snapshot, spaces)
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

func findSetupTrustHost(manifest setupconfig.Manifest, name string) (setupconfig.Host, error) {
	if manifest.HasCompileHost() && strings.EqualFold(manifest.CompileHost.Name, strings.TrimSpace(name)) {
		return manifest.CompileHost, nil
	}
	return findSetupHost(manifest, name)
}

func clearSetupSecrets(snapshot *setupconfig.Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.Manifest.Admin.Password = ""
	snapshot.Manifest.TencentCloud.SecretID = ""
	snapshot.Manifest.TencentCloud.SecretKey = ""
	snapshot.Manifest.Monitoring.WeComWebhook = ""
	snapshot.Manifest.ControlHost.Password = ""
	snapshot.Manifest.CompileHost.Password = ""
	for index := range snapshot.Manifest.OtherHosts {
		snapshot.Manifest.OtherHosts[index].Password = ""
	}
}

func writeSetupJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
