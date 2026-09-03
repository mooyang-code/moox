package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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

const defaultSetupFile = "./moox.toml"

type setupDeps struct {
	load                   func(string) (*setupconfig.Snapshot, error)
	loadInitBundle         func(string) (setupInitBundle, error)
	validate               func(context.Context, *setupconfig.Snapshot) (setupvalidate.Result, error)
	validateDeployment     func(context.Context, *setupconfig.Snapshot, []setupconfig.Host) (setupvalidate.Result, error)
	trustHost              func(context.Context, *setupconfig.Snapshot, string, string) error
	deployControl          func(context.Context, *setupconfig.Snapshot, bool) error
	deployService          func(context.Context, *setupconfig.Snapshot, string, string, string, string) (setupdeploy.ServiceResult, error)
	apply                  func(context.Context, *setupconfig.Snapshot) (setupclient.ApplyResult, error)
	status                 func(context.Context, *setupconfig.Snapshot) (setupclient.StatusResult, error)
	applySpaces            func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.ApplyResult, error)
	statusSpaces           func(context.Context, *setupconfig.Snapshot, []setupclient.Space) (setupclient.StatusResult, error)
	login                  func(context.Context, *setupconfig.Snapshot) (setupclient.LoginResult, error)
	openInitStorage        func(context.Context, *setupconfig.Snapshot, string) (setupInitStorage, error)
	openInitFactor         func(context.Context, *setupconfig.Snapshot) (setupInitFactor, error)
	deployStorage          func(context.Context, *setupconfig.Snapshot, string, bool, bool) error
	installStorageWatchdog func(context.Context, *setupconfig.Snapshot, string) error
	importMetadata         func(context.Context, *setupconfig.Snapshot, string, string, []string) (metadataImportSummary, error)
	verifyStorage          func(context.Context, *setupconfig.Snapshot, string) (storageVerifyResult, error)
	e2eStorage             func(context.Context, *setupconfig.Snapshot, string, string) (storageE2EResult, error)
	browserE2EStorage      func(context.Context, *setupconfig.Snapshot, string, string) (storageBrowserResult, error)
	e2eEventBus            func(context.Context, *setupconfig.Snapshot) (eventBusE2EResult, error)
	exportSkillConfig      func(context.Context, *setupconfig.Snapshot, string) (dataAccessConfig, error)
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
		newSetupFactorsCommand(deps),
		newSetupHostsCommand(deps),
		newSetupValidateCommand(deps),
		newSetupTrustHostCommand(deps),
		newSetupTrustBrowserCommand(deps),
		newSetupDeployCommand(deps),
		newSetupDeployServiceCommand(deps),
		newSetupRenderRuntimeConfigCommand(deps),
		newSetupApplyCommand(deps),
		newSetupStatusCommand(deps),
		newSetupDeployStorageCommand(deps),
		newSetupInstallStorageWatchdogCommand(deps),
		newSetupMetadataImportCommand(deps),
		newSetupVerifyStorageCommand(deps),
		newSetupE2EStorageCommand(deps),
		newSetupBrowserE2EStorageCommand(deps),
		newSetupE2EEventBusCommand(deps),
		newSetupExportSkillConfigCommand(deps),
	)
	return cmd
}

func newSetupRenderRuntimeConfigCommand(deps setupDeps) *cobra.Command {
	var file, tradeOutput, collectorOutput, nodeID string
	cmd := &cobra.Command{
		Use:   "render-runtime-config",
		Short: "从 moox.toml 渲染 Trade/Collector 运行配置",
		RunE: func(cmd *cobra.Command, _ []string) error {
			tradeOutput = strings.TrimSpace(tradeOutput)
			collectorOutput = strings.TrimSpace(collectorOutput)
			if tradeOutput == "" && collectorOutput == "" {
				return fmt.Errorf("runtime_config: at least one output path is required")
			}
			if tradeOutput != "" && collectorOutput != "" {
				tradeCanonical, err := canonicalRuntimeOutputPath(tradeOutput)
				if err != nil {
					return err
				}
				collectorCanonical, err := canonicalRuntimeOutputPath(collectorOutput)
				if err != nil {
					return err
				}
				if tradeCanonical == collectorCanonical {
					return fmt.Errorf("runtime_config: trade and collector outputs must be different")
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
			var tradeRaw, collectorRaw []byte
			if tradeOutput != "" {
				tradeRaw, err = readRuntimeConfigFile(tradeOutput)
				if err != nil {
					return err
				}
				tradeRaw, err = setupconfig.RenderTradeDNSResolverConfigForNode(snapshot, nodeID, tradeRaw)
				if err != nil {
					return err
				}
			}
			if collectorOutput != "" {
				collectorRaw, err = readRuntimeConfigFile(collectorOutput)
				if err != nil {
					return err
				}
				collectorRaw, err = setupconfig.RenderCollectorDNSResolverConfig(snapshot, collectorRaw)
				if err != nil {
					return err
				}
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			if tradeOutput != "" {
				if err := setupconfig.WriteRenderedRuntimeConfig(tradeOutput, tradeRaw); err != nil {
					return err
				}
			}
			if collectorOutput != "" {
				if err := setupconfig.WriteRenderedRuntimeConfig(collectorOutput, collectorRaw); err != nil {
					return err
				}
			}
			resolverNodeID, resolverTarget, resolverErr := setupconfig.DNSResolverRuntimeTarget(snapshot)
			if resolverErr != nil {
				return resolverErr
			}
			tradeConsoleHost := ""
			tradeConsolePort := 0
			if snapshot.Manifest.DNSResolver.Enabled {
				tradeHost, tradeErr := findSetupHost(snapshot.Manifest, snapshot.Manifest.DNSResolver.TradeNode)
				if tradeErr != nil {
					return tradeErr
				}
				tradeConsoleHost = tradeHost.Address
				tradeConsolePort = 11200
			}
			return writeSetupJSON(cmd, map[string]any{
				"status":               "rendered",
				"trade_output":         tradeOutput,
				"collector_output":     collectorOutput,
				"dns_resolver_enabled": snapshot.Manifest.DNSResolver.Enabled,
				"dns_resolver_node_id": resolverNodeID,
				"dns_resolver_target":  resolverTarget,
				"trade_console_host":   tradeConsoleHost,
				"trade_console_port":   tradeConsolePort,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&tradeOutput, "trade-output", "", "Trade app.yaml 输出路径")
	cmd.Flags().StringVar(&collectorOutput, "collector-output", "", "Collector app.yaml 输出路径")
	cmd.Flags().StringVar(&nodeID, "node-id", "", "当前部署节点 ID；非 Resolver 节点上的 Trade 会禁用 Resolver")
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
			switch {
			case index == 0:
				role = "control"
			case snapshot.Manifest.HasStorageHost() && strings.EqualFold(host.Name, snapshot.Manifest.StorageHost.Name):
				role = "storage"
			case snapshot.Manifest.HasViewHost() && strings.EqualFold(host.Name, snapshot.Manifest.ViewHost.Name):
				role = "view"
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
			host := snapshot.Manifest.ControlHost
			result := map[string]any{
				"host":   host.Name,
				"status": "trusted",
			}
			if !setupdeploy.RequiresLocalCATrust(setupdeploy.TLSMode(host.TLSMode), host.Address) {
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

func newSetupDeployCommand(deps setupDeps) *cobra.Command {
	var file string
	var resetData bool
	cmd := &cobra.Command{Use: "deploy-control", Short: "部署 Admin、Gateway 和 Web", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		deploymentHosts := []setupconfig.Host{snapshot.Manifest.ControlHost}
		if snapshot.Manifest.DNSResolver.Enabled {
			resolverHost, resolveErr := findSetupHost(snapshot.Manifest, snapshot.Manifest.DNSResolver.TradeNode)
			if resolveErr != nil {
				return resolveErr
			}
			deploymentHosts = append(deploymentHosts, resolverHost)
		}
		result, validationErr := deps.validateDeployment(cmd.Context(), snapshot, deploymentHosts)
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
		return writeSetupJSON(cmd, map[string]any{
			"host":        snapshot.Manifest.ControlHost.Name,
			"status":      "ready",
			"reset_data":  resetData,
			"certificate": setupCertificateSummaryWithMode(snapshot.Manifest.ControlHost.Address, setupdeploy.TLSMode(snapshot.Manifest.ControlHost.TLSMode)),
		})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().BoolVar(&resetData, "reset-data", false, "删除控制面现有数据后重新部署")
	return cmd
}

// setupCertificateSummary makes the certificate work performed by
// deploy-control explicit without exposing any key material. The deployment
// itself remains the source of truth for Caddy configuration and renewal.
func setupCertificateSummary(publicHost string) map[string]any {
	return setupCertificateSummaryWithMode(publicHost, "")
}

func setupCertificateSummaryWithMode(publicHost string, mode setupdeploy.TLSMode) map[string]any {
	if setupdeploy.UsesPublicTLSMode(mode, publicHost) {
		return map[string]any{
			"mode":              "public",
			"issuer":            "letsencrypt",
			"automatic_renewal": true,
			"renewal":           "caddy_acme_ari",
		}
	}
	return map[string]any{
		"mode":              "internal",
		"issuer":            "caddy_internal_ca",
		"automatic_renewal": true,
		"renewal":           "caddy_internal",
	}
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
	cmd.Flags().StringVar(&deployDir, "deploy-dir", setupconfig.DefaultControlRoot, "远端部署目录")
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
	var resetStorageData, resetViewData bool
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
		if resetStorageData && resetViewData {
			return fmt.Errorf("--reset-storage-data and --reset-view-data are mutually exclusive")
		}
		if err := deps.deployStorage(cmd.Context(), snapshot, host, resetStorageData, resetViewData); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]any{"host": host, "status": "ready", "reset_storage_data": resetStorageData, "reset_view_data": resetViewData})
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "Storage 目标主机名称")
	cmd.Flags().BoolVar(&resetStorageData, "reset-storage-data", false, "仅用于已确认的破坏性 Schema 切换，清空旧 Storage data")
	cmd.Flags().BoolVar(&resetViewData, "reset-view-data", false, "清空 View A/B 索引和消费状态，但保留 Primary 数据")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newSetupInstallStorageWatchdogCommand(deps setupDeps) *cobra.Command {
	var file, host string
	cmd := &cobra.Command{Use: "install-storage-watchdog", Short: "在 Storage 主机安装并启用自动恢复监控", RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		if _, err := resolveStorageDeploymentHost(snapshot.Manifest, host); err != nil {
			return err
		}
		if err := deps.installStorageWatchdog(cmd.Context(), snapshot, host); err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, map[string]any{"host": host, "status": "ready"})
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
	if deps.openInitFactor == nil {
		deps.openInitFactor = defaults.openInitFactor
	}
	if deps.deployStorage == nil {
		deps.deployStorage = defaults.deployStorage
	}
	if deps.installStorageWatchdog == nil {
		deps.installStorageWatchdog = defaults.installStorageWatchdog
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
	if deps.exportSkillConfig == nil {
		deps.exportSkillConfig = defaults.exportSkillConfig
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
		loadInitBundle:         loadSetupInitBundle,
		validate:               defaultSetupValidate,
		validateDeployment:     defaultSetupValidateDeployment,
		trustHost:              defaultSetupTrustHost,
		deployControl:          defaultSetupDeploy,
		deployService:          defaultSetupDeployService,
		apply:                  defaultSetupApply,
		status:                 defaultSetupStatus,
		applySpaces:            defaultSetupApplyWithSpaces,
		statusSpaces:           defaultSetupStatusWithSpaces,
		openInitStorage:        defaultOpenSetupInitStorage,
		openInitFactor:         defaultOpenSetupFactor,
		deployStorage:          defaultSetupDeployStorage,
		installStorageWatchdog: defaultSetupInstallStorageWatchdog,
		importMetadata:         defaultSetupImportMetadata,
		verifyStorage:          defaultSetupVerifyStorage,
		e2eStorage:             defaultSetupE2EStorage,
		browserE2EStorage:      defaultSetupBrowserE2EStorage,
		e2eEventBus:            defaultSetupE2EEventBus,
		exportSkillConfig:      defaultSetupExportSkillConfig,
		login: func(ctx context.Context, snapshot *setupconfig.Snapshot) (setupclient.LoginResult, error) {
			baseURL := fmt.Sprintf("https://%s:9527", snapshot.Manifest.ControlHost.Address)
			tlsMode := setupdeploy.TLSMode(snapshot.Manifest.ControlHost.TLSMode)
			if err := ensureSetupBrowserCATrust(ctx, snapshot); err != nil {
				return setupclient.LoginResult{}, err
			}
			if setupdeploy.UsesPublicTLSMode(tlsMode, snapshot.Manifest.ControlHost.Address) {
				return setupclient.VerifyPublicLogin(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password)
			}
			return setupclient.VerifyPublicLoginWithCAFile(ctx, baseURL, snapshot.Manifest.Admin.Username, snapshot.Manifest.Admin.Password, setupdeploy.CAPath(snapshot.Manifest.ControlHost.Address))
		},
	}
}

func defaultSetupDeployStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string, resetStorageData, resetViewData bool) error {
	host, err := resolveStorageDeploymentHost(snapshot.Manifest, name)
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
	paths := snapshot.Manifest.Paths.Resolved()
	useControlGateway := sameHostEndpoint(host, snapshot.Manifest.ControlHost)
	primarySecret, viewSecret, err := controlStorageInternalAuth(ctx, control, paths.ControlRoot)
	if err != nil {
		return err
	}
	healthVersion, healthAccessKey, healthSecret, err := controlHealthAuth(ctx, control, paths.ControlRoot)
	if err != nil {
		return err
	}
	controlTLSMode := setupdeploy.TLSMode(snapshot.Manifest.ControlHost.TLSMode)
	controlURL, controlKey, serviceKey, gatewayCA, err := controlGatewayMaterial(ctx, control, snapshot.Manifest.ControlHost.Address, useControlGateway, paths.ControlRoot, controlTLSMode)
	if err != nil {
		return err
	}
	var storageEventBusCredential, storageEventBusCA, storageMetricsEventBusCredential []byte
	if !useControlGateway {
		storageEventBusCredential, err = readRemoteControlFile(ctx, control, ".config/moox/eventbus/storage-eventbus.yaml")
		if err != nil {
			return fmt.Errorf("read control Storage EventBus credential: %w", err)
		}
		storageEventBusCA, err = readRemoteControlFile(ctx, control, ".config/moox/eventbus/ca.pem")
		if err != nil {
			return fmt.Errorf("read control EventBus CA for Storage: %w", err)
		}
		storageMetricsEventBusCredential, err = readRemoteControlFile(ctx, control, ".config/moox/eventbus/metrics-publisher.yaml")
		if err != nil {
			return fmt.Errorf("read control metrics EventBus credential for Storage: %w", err)
		}
	}
	if !useControlGateway {
		if _, err = setupclient.New(control).PrepareStoragePlacement(ctx, host.Name, host.Address); err != nil {
			return err
		}
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("storage_deploy_invalid")
	}
	buildHost := host
	buildHostRole := ""
	if snapshot.Manifest.HasCompileHost() {
		buildHost = snapshot.Manifest.CompileHost
		buildHostRole = "compile"
	}
	if err := setupdeploy.Storage(ctx, transport, setupdeploy.Options{
		RepositoryRoot: root, PublicHost: host.Address, NodeID: host.Name, ResetStorageData: resetStorageData, ResetViewData: resetViewData,
		DeployRoot: paths.DeployRoot, ControlRoot: paths.ControlRoot, StorageRoot: paths.StorageRoot,
		UseControlGateway:                useControlGateway,
		EventBusPublicAddress:            snapshot.Manifest.EventBus.PublicAddress,
		EventBusPort:                     snapshot.Manifest.EventBus.Port,
		EventBusTLSEnabled:               snapshot.Manifest.EventBus.TLSEnabled,
		StoragePrimarySecret:             primarySecret,
		StorageViewSecret:                viewSecret,
		StorageEventBusCredential:        storageEventBusCredential,
		StorageEventBusCA:                storageEventBusCA,
		StorageMetricsEventBusCredential: storageMetricsEventBusCredential,
		StorageBuildPassword:             buildHost.Password,
		StorageBuildHost:                 buildHost.Name,
		StorageBuildHostRole:             buildHostRole,
		StorageViewPolicy:                snapshot.Manifest.StorageView,
		LocalLogs:                        snapshot.Manifest.LocalLogs,
		InstallStorageWatchdog:           true,
		HealthAuthVersion:                healthVersion,
		HealthAuthAccessKey:              healthAccessKey,
		HealthAuthSecretKey:              healthSecret,
		GatewayControlURL:                controlURL,
		GatewayControlKey:                controlKey,
		GatewayServiceKey:                serviceKey,
		GatewayCABundle:                  gatewayCA,
		TLSMode:                          controlTLSMode,
	}, setupdeploy.Dependencies{}); err != nil {
		return err
	}
	if !useControlGateway {
		if _, err = setupclient.New(control).ApplyStoragePlacement(ctx, host.Name, host.Address); err != nil {
			return err
		}
		if err = configureRemoteCollectorStorageTarget(ctx, control, host.Name, host.Address, paths.ControlRoot); err != nil {
			return err
		}
		if err = ensureSetupStorageGatewayFirewall(ctx, snapshot, host.Address); err != nil {
			return err
		}
	} else {
		// Control setup disables Storage routes until the package is installed.
		// Activate them only after Storage readiness succeeds so the native
		// gateway can resolve PrimaryStore and Metadata calls.
		// Control-plane service deployments use the stable node id "control";
		// the manifest host name is only an SSH/config alias.
		if _, err = setupclient.New(control).ActivateStoragePlacement(ctx, "control"); err != nil {
			return err
		}
	}
	// Storage may be deployed in its own root while Admin/Gateway remains in
	// the control root. Persist the placement decision so a later Admin
	// restart does not re-import the control profile with Storage routes
	// disabled. The update is atomic and guarded by a per-config lock.
	if err := persistControlStorageRoutePolicy(ctx, control, paths.ControlRoot, true); err != nil {
		return err
	}
	return restartStorageClients(ctx, control, paths.ControlRoot)
}

func persistControlStorageRoutePolicy(ctx context.Context, control setupssh.Client, controlRoot string, preserve bool) error {
	if control == nil {
		return fmt.Errorf("storage_route_policy_persist_failed")
	}
	value := "0"
	if preserve {
		value = "1"
	}
	_, err := control.Run(ctx, []string{
		"sh", "-lc", `set -eu
config="$1/config/components.env"
test -f "$config"
lock="$1.maintenance.lock"
(
  flock -x 9
  tmp="$config.tmp.$$"
  trap 'rm -f "$tmp"' EXIT
  awk '!/^MOOX_PRESERVE_STORAGE_ROUTES=/' "$config" >"$tmp"
  printf 'MOOX_PRESERVE_STORAGE_ROUTES=%s\n' "$2" >>"$tmp"
  chmod --reference="$config" "$tmp" 2>/dev/null || chmod 0600 "$tmp"
  mv -f "$tmp" "$config"
  trap - EXIT
) 9>"$lock"
	`, "moox-persist-storage-route-policy", controlRoot, value,
	}, nil)
	if err != nil {
		return fmt.Errorf("storage_route_policy_persist_failed")
	}
	return nil
}

func defaultSetupInstallStorageWatchdog(ctx context.Context, snapshot *setupconfig.Snapshot, name string) error {
	host, err := resolveStorageDeploymentHost(snapshot.Manifest, name)
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
		return fmt.Errorf("storage_watchdog_install_invalid")
	}
	eventBusURL := "tls://" + net.JoinHostPort(snapshot.Manifest.EventBus.PublicAddress, strconv.Itoa(snapshot.Manifest.EventBus.Port))
	return setupdeploy.InstallStorageViewWatchdogWithOptions(ctx, transport, root, setupdeploy.WatchdogOptions{
		StorageRoot: snapshot.Manifest.Paths.Resolved().StorageRoot,
		EventBusURL: eventBusURL, GatewayNodeID: host.Name,
	})
}

// configureRemoteCollectorStorageTarget keeps the Collector planner on the
// same native Storage gateway that short-lived SCF invocations use. The
// control host has no local Storage when Storage is deployed remotely.
func configureRemoteCollectorStorageTarget(ctx context.Context, control setupssh.Client, nodeID, host, controlRoot string) error {
	if control == nil || strings.TrimSpace(nodeID) == "" || net.ParseIP(strings.TrimSpace(host)) == nil {
		return fmt.Errorf("collector_storage_target_prepare_failed")
	}
	target := "ip://" + net.JoinHostPort(host, "11003")
	_, err := control.Run(ctx, []string{
		"sh", "-lc", `set -eu
control_root="$1"
target="$2"
config="$control_root/collector/config/app.yaml"
test -f "$config"
python3 - "$config" "$target" "$3" <<'PY'
import pathlib
import re
import sys

path = pathlib.Path(sys.argv[1])
target = sys.argv[2]
node_id = sys.argv[3]
raw = path.read_text()
updated, target_count = re.subn(r'(?m)^  gateway_target:.*$', '  gateway_target: ' + target, raw, count=1)
updated, node_count = re.subn(r'(?m)^  gateway_node_id:.*$', '  gateway_node_id: "' + node_id + '"', updated, count=1)
if target_count != 1 or node_count != 1:
    raise SystemExit(1)
path.write_text(updated)
PY
	`, "sh", controlRoot, target, nodeID}, nil)
	if err != nil {
		return fmt.Errorf("collector_storage_target_prepare_failed")
	}
	return nil
}

func controlGatewayMaterial(ctx context.Context, control setupssh.Client, controlHost string, local bool, controlRoot string, tlsMode setupdeploy.TLSMode) (string, string, string, []byte, error) {
	if local {
		return "http://127.0.0.1:11000", "", "", nil, nil
	}
	result, err := control.Run(ctx, []string{"sh", "-lc", `set -eu
control_root="$1"
	tls_mode="$2"
control_key="$control_root/secrets/gateway-control.key"
service_key="$control_root/secrets/gateway-service.key"
caddy_root="$control_root/data/caddy/caddy/pki/authorities/local/root.crt"
gateway_peers="$control_root/certs/gateway/peers.pem"
for file in "$control_key" "$service_key" "$gateway_peers"; do test -s "$file"; done
base64 -w 0 "$control_key"; printf '\n'
base64 -w 0 "$service_key"; printf '\n'
if [ "$tls_mode" = internal ]; then
  test -s "$caddy_root"
  cat "$caddy_root" "$gateway_peers" | base64 -w 0
else
  cat "$gateway_peers" | base64 -w 0
fi
printf '\n'`, "moox-control-gateway-material", controlRoot, string(tlsMode)}, nil)
	if err != nil {
		return "", "", "", nil, fmt.Errorf("gateway_material_prepare_failed")
	}
	parts := strings.Split(strings.TrimSpace(result.Stdout), "\n")
	if len(parts) != 3 {
		return "", "", "", nil, fmt.Errorf("gateway_material_prepare_failed")
	}
	decoded := make([][]byte, 3)
	for index, value := range parts {
		decoded[index], err = base64.StdEncoding.DecodeString(value)
		if err != nil || index < 2 && len(decoded[index]) == 0 {
			return "", "", "", nil, fmt.Errorf("gateway_material_prepare_failed")
		}
	}
	return "https://" + net.JoinHostPort(controlHost, "9527"), strings.TrimSpace(string(decoded[0])), strings.TrimSpace(string(decoded[1])), decoded[2], nil
}

func controlHealthAuth(ctx context.Context, control setupssh.Client, controlRoot string) (string, string, string, error) {
	if control == nil {
		return "", "", "", fmt.Errorf("health_secret_prepare_failed")
	}
	result, err := control.Run(ctx, []string{
		"sh", "-lc", `set -eu
secret_file="$1/secrets/health-auth.env"
test -s "${secret_file}"
awk '/^MOOX_HEALTH_AUTH_(VERSION|ACCESS_KEY|SECRET_KEY)=/{print}' "${secret_file}"`,
		"moox-control-health-auth", controlRoot,
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

func controlStorageInternalAuth(ctx context.Context, control setupssh.Client, controlRoot string) (string, string, error) {
	if control == nil {
		return "", "", fmt.Errorf("storage_secret_prepare_failed")
	}
	result, err := control.Run(ctx, []string{
		"sh", "-lc", `set -eu
secret_file="$1/secrets/storage-internal-auth.env"
test -s "${secret_file}"
awk '/^MOOX_STORAGE_(PRIMARY|VIEW)_AUTH_SECRET=/{print}' "${secret_file}"`,
		"moox-control-storage-auth", controlRoot,
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

func restartStorageClients(ctx context.Context, control setupssh.Client, controlRoot string) error {
	if control == nil {
		return fmt.Errorf("storage_client_restart_failed")
	}
	if _, err := control.Run(ctx, []string{
		"sh", "-lc",
		restartStorageClientsScript,
		"moox-restart-storage-clients", controlRoot,
	}, nil); err != nil {
		return fmt.Errorf("storage_client_restart_failed")
	}
	return nil
}

const restartStorageClientsScript = `set -eu
for service in monitor cloudnode collector; do
  if "$1/status.sh" "$service" >/dev/null 2>&1; then
    restarted=0
    for attempt in 1 2 3; do
      if "$1/restart.sh" "$service"; then
        restarted=1
        break
      fi
      sleep 2
    done
    test "$restarted" -eq 1
  fi
done`

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
	// The control profile deliberately does not run Trade. When Trade is
	// placed on the dedicated node selected by moox.toml, update the
	// browser-facing control route after Admin has seeded its defaults; doing
	// this here makes a reset/redeploy deterministic instead of restoring the
	// unusable loopback 127.0.0.1:11200 endpoint.
	if snapshot.Manifest.DNSResolver.Enabled {
		tradeHost, resolveErr := findSetupHost(snapshot.Manifest, snapshot.Manifest.DNSResolver.TradeNode)
		if resolveErr != nil {
			return resolveErr
		}
		if err := setupclient.New(transport).ApplyTradeConsolePlacement(ctx, tradeHost.Address); err != nil {
			return err
		}
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
	if !isPublicFirewallIP(publicIP) {
		return nil
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

func isPublicFirewallIP(address string) bool {
	ip := net.ParseIP(strings.TrimSpace(address))
	return ip != nil && ip.To4() != nil && ip.IsGlobalUnicast() &&
		!ip.IsPrivate() && !ip.IsLoopback() && !ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast() && !ip.IsMulticast()
}

func ensureSetupControlFirewall(ctx context.Context, snapshot *setupconfig.Snapshot) error {
	rules := setupControlFirewallRulesForTLS(
		setupdeploy.TLSMode(snapshot.Manifest.ControlHost.TLSMode),
		snapshot.Manifest.ControlHost.Address,
	)
	return ensureSetupFirewallRules(ctx, snapshot, snapshot.Manifest.ControlHost.Address, rules, "control_firewall_failed")
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

func ensureSetupStorageGatewayFirewall(ctx context.Context, snapshot *setupconfig.Snapshot, address string) error {
	if storageGatewayPortReachable(ctx, address, 11003) {
		return nil
	}
	rule := cloudtencent.CreateFirewallRulesOptions{
		Protocol: "TCP", Ports: "11003", CidrBlock: "0.0.0.0/0", Action: "ACCEPT", Description: "MooX remote Storage native gateway",
	}
	if err := ensureSetupFirewallRules(ctx, snapshot, address, []cloudtencent.CreateFirewallRulesOptions{rule}, "storage_gateway_firewall_failed"); err == nil {
		return nil
	}
	// A remote Storage host may be a CVM rather than a Lighthouse instance.
	// Lighthouse's public-IP lookup then reports a false deployment failure;
	// fall back to the CVM/VPC security-group API for that topology.
	publicIP, err := eventBusFirewallIP(ctx, address, net.DefaultResolver.LookupIP)
	if err != nil {
		return fmt.Errorf("storage_gateway_firewall_failed")
	}
	client, err := cloudtencent.NewCVMClient(cloudtencent.ClientOptions{
		SecretID: snapshot.Manifest.TencentCloud.SecretID, SecretKey: snapshot.Manifest.TencentCloud.SecretKey,
		Region: snapshot.Manifest.TencentCloud.Region,
	})
	if err != nil {
		return fmt.Errorf("storage_gateway_firewall_failed")
	}
	if err := client.EnsureSecurityGroupRule(ctx, publicIP, rule); err != nil {
		return fmt.Errorf("storage_gateway_firewall_failed")
	}
	return nil
}

func storageGatewayPortReachable(ctx context.Context, address string, port int) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := 3 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(strings.Trim(address, "[]"), strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
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

func setupControlFirewallRulesForTLS(mode setupdeploy.TLSMode, address string) []cloudtencent.CreateFirewallRulesOptions {
	rules := setupControlFirewallRules()
	if setupdeploy.UsesPublicTLSMode(mode, address) {
		return rules
	}
	return rules[1:]
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
	paths := snapshot.Manifest.Paths.Resolved()
	localStorageTarget := ""
	localStorageNodeID := ""
	if snapshot.Manifest.HasStorageHost() {
		storageHost := snapshot.Manifest.StorageHost
		localStorageTarget = "ip://" + net.JoinHostPort(strings.Trim(storageHost.Address, "[]"), "11003")
		localStorageNodeID = storageHost.Name
	}
	return setupdeploy.Options{
		RepositoryRoot:               repositoryRoot,
		DeployRoot:                   paths.DeployRoot,
		ControlRoot:                  paths.ControlRoot,
		StorageRoot:                  paths.StorageRoot,
		PublicHost:                   snapshot.Manifest.ControlHost.Address,
		BrowserPort:                  9527,
		EventBusPublicAddress:        snapshot.Manifest.EventBus.PublicAddress,
		EventBusPort:                 snapshot.Manifest.EventBus.Port,
		EventBusTLSEnabled:           snapshot.Manifest.EventBus.TLSEnabled,
		LocalStorageRPCGatewayTarget: localStorageTarget,
		LocalStorageGatewayNodeID:    localStorageNodeID,
		NotificationChannelType:      snapshot.Manifest.Notification.ChannelType,
		NotificationWebhookURL:       snapshot.Manifest.Notification.WebhookURL,
		LocalLogs:                    snapshot.Manifest.LocalLogs,
		TLSMode:                      setupdeploy.TLSMode(snapshot.Manifest.ControlHost.TLSMode),
		InstallLocalCA:               true,
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
	if isBrowserService(service) {
		if err := ensureSetupBrowserCATrust(ctx, snapshot); err != nil {
			return setupdeploy.ServiceResult{}, err
		}
	}
	tradeConsoleBindAddress := ""
	if strings.EqualFold(strings.TrimSpace(service), "trade") && !strings.EqualFold(host.Name, snapshot.Manifest.ControlHost.Name) {
		// SSH/public addresses can be NAT front doors and are not necessarily
		// assigned to a target interface. Bind all interfaces on the dedicated
		// Trade host; the cloud firewall must restrict 11200 to control_host.
		tradeConsoleBindAddress = "0.0.0.0"
	}
	result, err := setupdeploy.Service(ctx, transport, setupdeploy.ServiceOptions{
		PackagePath: packagePath, ServiceName: service, DeployDir: deployDir,
		EventBusURL: setupEventBusURL(snapshot.Manifest), TradeConsoleBindAddress: tradeConsoleBindAddress,
	})
	if err != nil {
		return setupdeploy.ServiceResult{}, err
	}

	// Keep the Admin deployment directory in sync with every successful
	// package deployment. Monitor derives system checks from this store, so a
	// service published only through SSH must still become visible in the
	// operations overview. Reuse the target connection for control deployments;
	// remote-node deployments open a short-lived control-plane tunnel.
	control := transport
	closeControl := false
	if !strings.EqualFold(host.Name, snapshot.Manifest.ControlHost.Name) {
		control, err = dialSetupHost(ctx, snapshot.Manifest.ControlHost)
		if err != nil {
			return setupdeploy.ServiceResult{}, fmt.Errorf("service_registry_failed: %w", err)
		}
		closeControl = true
	}
	if closeControl {
		defer control.Close()
	}
	controlClient := setupclient.New(control)
	if err := controlClient.RegisterServiceDeployment(ctx, host.Name, service, host.Address); err != nil {
		// The service has already been activated by setupdeploy.Service. Do not
		// leave a running Trade process without a matching control-plane route.
		if tradeConsoleBindAddress != "" {
			_, _ = transport.Run(ctx, []string{"bash", "-lc", `"$1/stop.sh" trade`, "moox-stop-trade-after-registry-failure", result.DeployDir}, nil)
		}
		return setupdeploy.ServiceResult{}, fmt.Errorf("service_registry_failed: %w", err)
	}
	if tradeConsoleBindAddress != "" {
		if err := probeTradeConsole(ctx, control, host.Address); err != nil {
			_, _ = transport.Run(ctx, []string{"bash", "-lc", `"$1/stop.sh" trade`, "moox-stop-trade-after-probe-failure", result.DeployDir}, nil)
			_ = controlClient.DisableServiceDeployment(ctx, host.Name, service)
			return setupdeploy.ServiceResult{}, fmt.Errorf("service_registry_failed: %w", err)
		}
		if err := controlClient.ApplyTradeConsolePlacement(ctx, host.Address); err != nil {
			// Keep registry and process state aligned when the second control-plane
			// write fails after activation.
			_, _ = transport.Run(ctx, []string{"bash", "-lc", `"$1/stop.sh" trade`, "moox-stop-trade-after-route-failure", result.DeployDir}, nil)
			_ = controlClient.DisableServiceDeployment(ctx, host.Name, service)
			return setupdeploy.ServiceResult{}, fmt.Errorf("service_registry_failed: %w", err)
		}
	}
	result.RegistrySynced = true
	return result, nil
}

// probeTradeConsole checks the actual control-to-Trade network path before
// publishing the browser route. Trade returns HTTP 200 with a business error
// when no space header is supplied, which is sufficient for this liveness
// probe and avoids requiring operator credentials.
func probeTradeConsole(ctx context.Context, control setupssh.Client, host string) error {
	host = strings.TrimSpace(host)
	if control == nil || net.ParseIP(host) == nil {
		return fmt.Errorf("trade_console_probe_invalid")
	}
	_, err := control.Run(ctx, []string{"bash", "-lc", `set -eu
host="$1"
curl --fail --silent --show-error --max-time 5 \
  -X POST -H 'Content-Type: application/json' --data '{}' \
  "http://${host}:11200/trpc.moox.trade.TradeConsoleService/ListTradingAccounts" >/dev/null
`, "moox-probe-trade-console", host}, nil)
	if err != nil {
		return fmt.Errorf("trade_console_probe_failed")
	}
	return nil
}

func setupEventBusURL(manifest setupconfig.Manifest) string {
	address := strings.TrimSpace(manifest.EventBus.PublicAddress)
	if address == "" || manifest.EventBus.Port < 1 || manifest.EventBus.Port > 65535 {
		return ""
	}
	scheme := "nats"
	if manifest.EventBus.TLSEnabled {
		scheme = "tls"
	}
	return scheme + "://" + net.JoinHostPort(address, strconv.Itoa(manifest.EventBus.Port))
}

func isBrowserService(service string) bool {
	switch strings.ToLower(strings.TrimSpace(service)) {
	case "admin", "admin_gateway", "moox-admin", "web-host", "web_host", "moox-web-host":
		return true
	default:
		return false
	}
}

func ensureSetupBrowserCATrust(ctx context.Context, snapshot *setupconfig.Snapshot) error {
	if snapshot == nil {
		return fmt.Errorf("browser_ca_trust_failed: setup configuration is missing")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("browser_ca_trust_failed: resolve repository root: %w", err)
	}
	host := snapshot.Manifest.ControlHost
	return setupdeploy.EnsureLocalCATrustForHost(
		ctx,
		root,
		host.Address,
		setupdeploy.TLSMode(host.TLSMode),
	)
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

func resolveStorageDeploymentHost(manifest setupconfig.Manifest, name string) (setupconfig.Host, error) {
	name = strings.TrimSpace(name)
	if name == "" && manifest.HasStorageHost() {
		name = manifest.StorageHost.Name
	}
	host, err := findSetupHost(manifest, name)
	if err != nil {
		return setupconfig.Host{}, err
	}
	if manifest.HasStorageHost() && !strings.EqualFold(host.Name, manifest.StorageHost.Name) {
		return setupconfig.Host{}, fmt.Errorf("storage_host_required")
	}
	if manifest.HasViewHost() && !sameHostEndpoint(host, manifest.ViewHost) {
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
	snapshot.Manifest.Notification.WebhookURL = ""
	snapshot.Manifest.ControlHost.Password = ""
	snapshot.Manifest.CompileHost.Password = ""
	snapshot.Manifest.StorageHost.Password = ""
	snapshot.Manifest.ViewHost.Password = ""
	for index := range snapshot.Manifest.OtherHosts {
		snapshot.Manifest.OtherHosts[index].Password = ""
	}
}

func writeSetupJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}
