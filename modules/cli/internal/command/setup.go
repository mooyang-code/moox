package command

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
	load           func(string) (*setupconfig.Snapshot, error)
	validate       func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error)
	trustHost      func(context.Context, *setupconfig.Snapshot, string, string) error
	deployControl  func(context.Context, *setupconfig.Snapshot) error
	deployWebHost  func(context.Context, *setupconfig.Snapshot, string, string, string) (setupdeploy.WebHostResult, error)
	apply          func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error)
	status         func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error)
	login          func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error)
	deployStorage  func(context.Context, *setupconfig.Snapshot, string) error
	importMetadata func(context.Context, *setupconfig.Snapshot, string, string, []string) (metadataImportSummary, error)
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
		newSetupHostsCommand(deps),
		newSetupValidateCommand(deps),
		newSetupTrustHostCommand(deps),
		newSetupDeployCommand(deps),
		newSetupDeployWebHostCommand(deps),
		newSetupApplyCommand(deps),
		newSetupStatusCommand(deps),
		newSetupDeployStorageCommand(deps),
		newSetupMetadataImportCommand(deps),
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
		hosts := make([]setupHostChoice, 0, len(snapshot.Manifest.Hosts()))
		for index, host := range snapshot.Manifest.Hosts() {
			role := "other"
			if index == 0 {
				role = "control"
			}
			hosts = append(hosts, setupHostChoice{Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username, Role: role})
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
	cmd := &cobra.Command{Use: "deploy-control", Short: "部署 Admin、Gateway 和 Web", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if _, err := deps.validate(cmd.Context(), snapshot); err != nil {
			return err
		}
		if err := deps.deployControl(cmd.Context(), snapshot); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]string{"host": snapshot.Manifest.ControlHost.Name, "status": "ready"})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func newSetupDeployWebHostCommand(deps setupDeps) *cobra.Command {
	var file, host, binary, deployDir string
	cmd := &cobra.Command{Use: "deploy-web-host", Short: "通过 SSH 发布并校验单个 Web Host 二进制", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.deployWebHost(cmd.Context(), snapshot, host, binary, deployDir)
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
	cmd.Flags().StringVar(&binary, "binary", "", "本地 moox-web-host 二进制路径")
	cmd.Flags().StringVar(&deployDir, "deploy-dir", "~/moox/prod", "远端部署目录")
	_ = cmd.MarkFlagRequired("binary")
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
	cmd := &cobra.Command{Use: "deploy-storage", Short: "将 Storage 组件部署到用户选择的主机", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if _, err := deps.validate(cmd.Context(), snapshot); err != nil {
			return err
		}
		status, err := deps.status(cmd.Context(), snapshot)
		if err != nil || status.State != "completed" {
			return fmt.Errorf("setup_incomplete")
		}
		if err := deps.deployStorage(cmd.Context(), snapshot, host); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]string{"host": host, "status": "ready"})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "Storage 目标主机名称")
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
	cmd.Flags().StringVar(&seed, "seed", "examples/metadata-quant-initial.seed.yaml", "metadata seed YAML")
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
	if deps.validate == nil {
		deps.validate = defaults.validate
	}
	if deps.trustHost == nil {
		deps.trustHost = defaults.trustHost
	}
	if deps.deployControl == nil {
		deps.deployControl = defaults.deployControl
	}
	if deps.deployWebHost == nil {
		deps.deployWebHost = defaults.deployWebHost
	}
	if deps.apply == nil {
		deps.apply = defaults.apply
	}
	if deps.status == nil {
		deps.status = defaults.status
	}
	if deps.login == nil {
		deps.login = defaults.login
	}
	if deps.deployStorage == nil {
		deps.deployStorage = defaults.deployStorage
	}
	if deps.importMetadata == nil {
		deps.importMetadata = defaults.importMetadata
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
		validate:       defaultSetupValidate,
		trustHost:      defaultSetupTrustHost,
		deployControl:  defaultSetupDeploy,
		deployWebHost:  defaultSetupDeployWebHost,
		apply:          defaultSetupApply,
		status:         defaultSetupStatus,
		deployStorage:  defaultSetupDeployStorage,
		importMetadata: defaultSetupImportMetadata,
		login: func(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			baseURL := fmt.Sprintf("https://%s:9527", snapshot.Manifest.ControlHost.Address)
			return setupclient.VerifyPublicLoginWithCAFile(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password, setupdeploy.CAPath(snapshot.Manifest.ControlHost.Address))
		},
	}
}

func defaultSetupDeployStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string) error {
	host, err := findSetupHost(snapshot.Manifest, name)
	if err != nil {
		return err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return err
	}
	defer transport.Close()
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("storage_deploy_invalid")
	}
	if err := setupdeploy.Storage(ctx, transport, setupdeploy.Options{RepositoryRoot: root, PublicHost: host.Address}, setupdeploy.Dependencies{}); err != nil {
		return err
	}
	control, err := dialSetupHost(ctx, snapshot.Manifest.ControlHost)
	if err != nil {
		return err
	}
	defer control.Close()
	_, err = setupclient.New(control).ApplyStoragePlacement(ctx, host.Address)
	return err
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
	host, err := findSetupHost(snapshot.Manifest, name)
	if err != nil {
		return err
	}
	return setupssh.TrustHost(ctx, sshTarget(host), fingerprint, setupssh.Options{Timeout: 15 * time.Second})
}

func defaultSetupDeploy(ctx context.Context, snapshot *setupconfig.Snapshot) error {
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
	return setupdeploy.Control(ctx, transport, setupdeploy.Options{RepositoryRoot: root, PublicHost: host.Address, BrowserPort: 9527}, setupdeploy.Dependencies{})
}

func defaultSetupDeployWebHost(ctx context.Context, snapshot *setupconfig.Snapshot, hostName, binary, deployDir string) (setupdeploy.WebHostResult, error) {
	host, err := findSetupHost(snapshot.Manifest, hostName)
	if err != nil {
		return setupdeploy.WebHostResult{}, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return setupdeploy.WebHostResult{}, err
	}
	defer transport.Close()
	return setupdeploy.WebHost(ctx, transport, setupdeploy.WebHostOptions{BinaryPath: binary, DeployDir: deployDir})
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

func clearSetupSecrets(snapshot *setupconfig.Snapshot) {
	if snapshot == nil {
		return
	}
	snapshot.Manifest.Admin.Password = ""
	snapshot.Manifest.TencentCloud.SecretID = ""
	snapshot.Manifest.TencentCloud.SecretKey = ""
	snapshot.Manifest.ControlHost.Password = ""
	for index := range snapshot.Manifest.OtherHosts {
		snapshot.Manifest.OtherHosts[index].Password = ""
	}
}

func writeSetupJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
