package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/spf13/cobra"
)

type collectorPackageOptions struct {
	CollectorRoot string
	Version       string
	Out           string
	ConfigDir     string
}

type collectorPublishOptions struct {
	collectorPackageOptions
	ControlURL  string
	AccessToken string
	SpaceID     string
	// 后台服务签名鉴权（推荐，取代登录态 AccessToken）
	ServiceAccessKey string
	ServiceSecretKey string
	CloudAccountID   string
	Runtime          string
	Handler          string
	Region           string
	ZipPath          string
	PackageName      string
	PackageType      string
	BizType          string
	NodeType         string
	Env              []string
	Config           []string
}

type collectorDeployOptions struct {
	collectorPackageOptions
	ControlURL       string
	ServiceAccessKey string
	ServiceSecretKey string
	SpaceID          string
	CloudAccountID   string
	NodeID           string
	ZipPath          string
	PackageName      string
	PackageType      string
	BizType          string
	Runtime          string
}

var collectorDeployFlags collectorDeployOptions

var collectorFunctionDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "上传并部署数据采集器云函数到已有节点",
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := deployCollectorFunction(cmd.Context(), collectorDeployFlags)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	},
}

var collectorPackageFlags collectorPackageOptions
var collectorPublishFlags collectorPublishOptions

var collectorCmd = &cobra.Command{
	Use:   "collector",
	Short: "数据采集器工具",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var collectorFunctionCmd = &cobra.Command{
	Use:   "function",
	Short: "数据采集器云函数包与发布工具",
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var collectorFunctionPackageCmd = &cobra.Command{
	Use:   "package",
	Short: "构建数据采集器 SCF zip 包",
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := packageCollectorFunction(cmd.Context(), collectorPackageFlags)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "package=%s\n", result.Path)
		return nil
	},
}

var collectorFunctionPublishCmd = &cobra.Command{
	Use:   "publish",
	Short: "上传并发布数据采集器云函数节点",
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := publishCollectorFunction(cmd.Context(), collectorPublishFlags)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(summary)
	},
}

type collectorPublishSummary struct {
	ZipPath              string `json:"zip_path"`
	PackageID            string `json:"package_id,omitempty"`
	CreateBatchID        string `json:"create_batch_id,omitempty"`
	DeployBatchID        string `json:"deploy_batch_id,omitempty"`
	CreateProcessedCount int    `json:"create_processed_count,omitempty"`
	DeployProcessedCount int    `json:"deploy_processed_count,omitempty"`
}

func init() {
	rootCmd.AddCommand(collectorCmd)
	collectorCmd.AddCommand(collectorFunctionCmd)
	collectorFunctionCmd.AddCommand(collectorFunctionPackageCmd, collectorFunctionPublishCmd, collectorFunctionDeployCmd)

	addCollectorPackageFlags(collectorFunctionPackageCmd, &collectorPackageFlags)
	addCollectorPackageFlags(collectorFunctionPublishCmd, &collectorPublishFlags.collectorPackageOptions)
	addCollectorPackageFlags(collectorFunctionDeployCmd, &collectorDeployFlags.collectorPackageOptions)

	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ControlURL, "control-url", "", "Control service base URL")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 access_key")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.CloudAccountID, "cloud-account-id", "", "cloud account id")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.NodeID, "node-id", "", "existing cloud node id / function name")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ZipPath, "zip", "", "existing SCF zip path")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.PackageName, "package-name", "moox-collector", "function package name")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.PackageType, "package-type", "data_collector", "function package type")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.BizType, "biz-type", "data_collector", "business type")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.Runtime, "runtime", "Go1", "SCF runtime")

	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.ControlURL, "control-url", "", "Control service base URL")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.AccessToken, "access-token", "", "Control access token; defaults to MOOX_ACCESS_TOKEN (登录态, 不推荐)")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 access_key; 默认取 MOOX_SERVICE_AUTH_ACCESS_KEY (推荐, 走 /api/service 后台接口)")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key; 默认取 MOOX_SERVICE_AUTH_SECRET_KEY (推荐, 走 /api/service 后台接口)")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.CloudAccountID, "cloud-account-id", "", "cloud account id")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.Runtime, "runtime", "Go1", "SCF runtime")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.Handler, "handler", "main", "SCF handler")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.Region, "region", "", "cloud region")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.ZipPath, "zip", "", "existing SCF zip path")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.PackageName, "package-name", "moox-collector", "function package name")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.PackageType, "package-type", "data_collector", "function package type")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.BizType, "biz-type", "data_collector", "business type")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.NodeType, "node-type", "scf-event", "cloud node type")
	collectorFunctionPublishCmd.Flags().StringArrayVar(&collectorPublishFlags.Env, "env", nil, "SCF environment variable as KEY=VALUE")
	collectorFunctionPublishCmd.Flags().StringArrayVar(&collectorPublishFlags.Config, "function-config", nil, "cloudnode node runtime config as KEY=VALUE; not written into SCF package config.yaml")
}

func addCollectorPackageFlags(cmd *cobra.Command, opts *collectorPackageOptions) {
	cmd.Flags().StringVar(&opts.CollectorRoot, "collector-root", "", "collector module root")
	cmd.Flags().StringVar(&opts.Version, "version", "dev", "collector package version")
	cmd.Flags().StringVar(&opts.Out, "out", "", "output zip path")
	cmd.Flags().StringVar(&opts.ConfigDir, "config", "", "collector config directory")
}

func packageCollectorFunction(ctx context.Context, opts collectorPackageOptions) (*collectorpackager.BuildSCFPackageResult, error) {
	collectorRoot, err := resolveCollectorRoot(opts.CollectorRoot)
	if err != nil {
		return nil, err
	}
	version := opts.Version
	if version == "" {
		version = "dev"
	}
	outPath := opts.Out
	if outPath == "" {
		outPath = filepath.Join(collectorRoot, fmt.Sprintf("collector-scf-%s.zip", version))
	}
	configDir := opts.ConfigDir
	if configDir == "" {
		configDir = filepath.Join(collectorRoot, "configs")
	}
	binaryPath := filepath.Join(os.TempDir(), fmt.Sprintf("moox-collector-scf-%d", time.Now().UnixNano()), "main")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(binaryPath))

	if err := buildCollectorLinuxBinary(ctx, collectorRoot, binaryPath, version); err != nil {
		return nil, err
	}
	return collectorpackager.BuildSCFPackage(collectorpackager.BuildSCFPackageOptions{
		BinaryPath: binaryPath,
		ConfigDir:  configDir,
		OutPath:    outPath,
	})
}

func publishCollectorFunction(ctx context.Context, opts collectorPublishOptions) (collectorPublishSummary, error) {
	if opts.ControlURL == "" {
		return collectorPublishSummary{}, fmt.Errorf("--control-url is required")
	}
	if opts.CloudAccountID == "" {
		return collectorPublishSummary{}, fmt.Errorf("--cloud-account-id is required")
	}
	if opts.Region == "" {
		return collectorPublishSummary{}, fmt.Errorf("--region is required")
	}
	zipPath := opts.ZipPath
	if zipPath == "" {
		result, err := packageCollectorFunction(ctx, opts.collectorPackageOptions)
		if err != nil {
			return collectorPublishSummary{}, err
		}
		zipPath = result.Path
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return collectorPublishSummary{}, err
	}

	client := newControlClient(opts.ControlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, opts.SpaceID)
	uploadResp, err := client.UploadPackage(ctx, adminclient.UploadPackageRequest{
		PackageName:      defaultFlag(opts.PackageName, "moox-collector"),
		Version:          defaultFlag(opts.Version, "dev"),
		Runtime:          defaultFlag(opts.Runtime, "Go1"),
		PackageType:      adminclient.ResolvePackageType(defaultFlag(opts.PackageType, "data_collector")),
		BizType:          defaultFlag(opts.BizType, "data_collector"),
		CloudAccountID:   opts.CloudAccountID,
		OriginalFilename: filepath.Base(zipPath),
	}, data)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	summary := collectorPublishSummary{
		ZipPath:   zipPath,
		PackageID: uploadResp.PackageID,
	}

	createResp, err := client.BatchCreateNodes(ctx, []adminclient.NodeCreateItem{
		buildCollectorCreateNodeItem(opts, uploadResp.PackageID),
	})
	if err != nil {
		return summary, err
	}
	summary.CreateBatchID = createResp.BatchID
	summary.CreateProcessedCount = createResp.ProcessedCount
	return summary, nil
}

func buildCollectorCreateNodeItem(opts collectorPublishOptions, packageID string) adminclient.NodeCreateItem {
	packageName := defaultFlag(opts.PackageName, "moox-collector")
	bizType := defaultFlag(opts.BizType, "data_collector")
	return adminclient.NodeCreateItem{
		CloudAccountID: opts.CloudAccountID,
		NodeType:       defaultFlag(opts.NodeType, "scf-event"),
		Runtime:        defaultFlag(opts.Runtime, "Go1"),
		Handler:        defaultFlag(opts.Handler, "main"),
		Config:         parseCollectorOverrides(opts.Config),
		Environment:    collectorFunctionEnvironment(opts),
		Region:         opts.Region,
		PackageID:      packageID,
		Metadata: map[string]any{
			"function_name_prefix": packageName,
			"biz_type":             bizType,
			"supported_workloads":  []string{"collect.kline", "collect.symbol"},
		},
	}
}

func collectorFunctionEnvironment(opts collectorPublishOptions) map[string]string {
	env := map[string]string{}
	setDefaultEnv(env, "MOOX_SPACE_ID", defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID")))
	setDefaultEnv(env, "MOOX_SERVICE_AUTH_VERSION", "moox-auth-v1")
	setDefaultEnv(env, "MOOX_SERVICE_AUTH_ACCESS_KEY", defaultFlag(opts.ServiceAccessKey, os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY")))
	setDefaultEnv(env, "MOOX_SERVICE_AUTH_SECRET_KEY", defaultFlag(opts.ServiceSecretKey, os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY")))
	setDefaultEnv(env, "MOOX_SERVICE_AUTH_EXPIRE_SECONDS", "1800")
	for key, value := range parseCollectorOverrides(opts.Env) {
		env[key] = value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

func setDefaultEnv(env map[string]string, key string, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	env[key] = value
}

func deployCollectorFunction(ctx context.Context, opts collectorDeployOptions) (collectorPublishSummary, error) {
	if opts.ControlURL == "" {
		return collectorPublishSummary{}, fmt.Errorf("--control-url is required")
	}
	if opts.CloudAccountID == "" {
		return collectorPublishSummary{}, fmt.Errorf("--cloud-account-id is required")
	}
	if opts.NodeID == "" {
		return collectorPublishSummary{}, fmt.Errorf("--node-id is required")
	}
	zipPath := opts.ZipPath
	if zipPath == "" {
		result, err := packageCollectorFunction(ctx, opts.collectorPackageOptions)
		if err != nil {
			return collectorPublishSummary{}, err
		}
		zipPath = result.Path
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return collectorPublishSummary{}, err
	}

	client := newControlClient(opts.ControlURL, "", opts.ServiceAccessKey, opts.ServiceSecretKey, opts.SpaceID)
	uploadResp, err := client.UploadPackage(ctx, adminclient.UploadPackageRequest{
		PackageName:      defaultFlag(opts.PackageName, "moox-collector"),
		Version:          defaultFlag(opts.Version, "dev"),
		Runtime:          defaultFlag(opts.Runtime, "Go1"),
		PackageType:      adminclient.ResolvePackageType(defaultFlag(opts.PackageType, "data_collector")),
		BizType:          defaultFlag(opts.BizType, "data_collector"),
		CloudAccountID:   opts.CloudAccountID,
		OriginalFilename: filepath.Base(zipPath),
	}, data)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	summary := collectorPublishSummary{
		ZipPath:   zipPath,
		PackageID: uploadResp.PackageID,
	}

	deployResp, err := client.BatchDeployNodes(ctx, []adminclient.NodeDeployItem{{
		NodeID:    opts.NodeID,
		PackageID: uploadResp.PackageID,
	}})
	if err != nil {
		return summary, err
	}
	summary.DeployBatchID = deployResp.BatchID
	summary.DeployProcessedCount = deployResp.ProcessedCount
	return summary, nil
}

func newControlClient(controlURL, accessToken, serviceAccessKey, serviceSecretKey string, spaceID string) *adminclient.Client {
	client := adminclient.New(controlURL)
	client.HTTPClient = &http.Client{Timeout: 10 * time.Minute}
	client.AccessToken = defaultFlag(accessToken, os.Getenv("MOOX_ACCESS_TOKEN"))
	client.SpaceID = defaultFlag(spaceID, os.Getenv("MOOX_SPACE_ID"))
	accessKey := defaultFlag(serviceAccessKey, os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"))
	secretKey := defaultFlag(serviceSecretKey, os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"))
	if accessKey != "" && secretKey != "" {
		client.ServiceAuth = &adminclient.ServiceAuthConfig{
			AccessKey:  accessKey,
			SecretKey:  secretKey,
			ExpireSecs: 1800,
		}
	}
	return client
}

func buildCollectorLinuxBinary(ctx context.Context, collectorRoot, outPath, version string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", fmt.Sprintf("-X main.Version=%s", version), "-o", outPath, "./cmd/scf")
	cmd.Dir = collectorRoot
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build collector linux/amd64 binary: %w\n%s", err, output)
	}
	return nil
}

func resolveCollectorRoot(explicit string) (string, error) {
	var candidates []string
	if explicit != "" {
		candidates = append(candidates, explicit)
	} else if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd, filepath.Join(cwd, "modules", "collector"), filepath.Join(cwd, "..", "collector"))
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "cmd", "scf", "main.go")); err == nil {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("collector root not found; pass --collector-root")
}

func parseCollectorOverrides(raw []string) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	overrides := make(map[string]string, len(raw))
	for _, item := range raw {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		overrides[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return overrides
}
