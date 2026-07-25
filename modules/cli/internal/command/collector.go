package command

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/spf13/cobra"
)

type collectorPackageOptions struct {
	CollectorRoot string
	Version       string
	Out           string
	ConfigDir     string
	CLSLogsetID   string
	CLSTopicID    string
}

type collectorPublishOptions struct {
	collectorPackageOptions
	ControlURL  string
	AccessToken string
	SpaceID     string
	// 后台服务签名鉴权（推荐，取代登录态 AccessToken）
	ServiceAccessKey       string
	ServiceSecretKey       string
	CloudAccountID         string
	Runtime                string
	Handler                string
	Region                 string
	ZipPath                string
	PackageName            string
	PackageType            string
	BizType                string
	NodeType               string
	Env                    []string
	Config                 []string
	EventBusCredentialFile string
	CLSSecretID            string
	CLSSecretKey           string
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
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 key_id; 默认取 MOOX_GATEWAY_SERVICE_KEY_ID")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key; 默认取 MOOX_GATEWAY_SERVICE_SECRET_KEY")
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
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.EventBusCredentialFile, "eventbus-credential-file", "~/.config/moox/eventbus/cloudnode-worker.yaml", "0600 cloudnode-worker EventBus credential YAML")
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
	clsTopicID := strings.TrimSpace(opts.CLSTopicID)
	if clsTopicID == "" {
		resources, err := resolveCollectorCLSResources(ctx)
		if err != nil {
			return nil, err
		}
		clsTopicID = resources.TopicID
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
		CLSTopicID: clsTopicID,
	})
}

var newCollectorCLSAPI = func() (tencent.CLSAPI, error) {
	return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{
		SecretID:  firstNonEmpty(os.Getenv("TENCENTCLOUD_SECRET_ID"), os.Getenv("MOOX_CLS_SECRET_ID")),
		SecretKey: firstNonEmpty(os.Getenv("TENCENTCLOUD_SECRET_KEY"), os.Getenv("MOOX_CLS_SECRET_KEY")),
		Region:    clsprepare.Region,
	})
}

func resolveCollectorCLSResources(ctx context.Context) (tencent.CLSBootstrapResult, error) {
	api, err := newCollectorCLSAPI()
	if err != nil {
		return tencent.CLSBootstrapResult{}, fmt.Errorf("create CLS client for collector package: %w", err)
	}
	resources, err := tencent.ResolveExistingCLS(ctx, api, clsprepare.LogsetName, clsprepare.TopicName)
	if err != nil {
		return tencent.CLSBootstrapResult{}, fmt.Errorf("resolve CLS topic for collector package: %w", err)
	}
	return resources, nil
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
	client := newControlClient(opts.ControlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, opts.SpaceID)
	accounts, err := client.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return collectorPublishSummary{}, err
	}
	var credentialSecretID string
	for _, account := range accounts {
		if account.AccountID == opts.CloudAccountID {
			credentialSecretID = account.CredentialSecretID
			break
		}
	}
	if credentialSecretID == "" {
		return collectorPublishSummary{}, fmt.Errorf("Tencent cloud account %q not found or has no credential reference", opts.CloudAccountID)
	}
	secret, err := client.RevealSecret(ctx, credentialSecretID)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	opts.CLSSecretID, opts.CLSSecretKey = secret.KeyID, secret.SecretValue
	clsAPI, err := tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{SecretID: secret.KeyID, SecretKey: secret.SecretValue, Region: clsprepare.Region})
	if err != nil {
		return collectorPublishSummary{}, err
	}
	clsResources, err := tencent.ResolveExistingCLS(ctx, clsAPI, clsprepare.LogsetName, clsprepare.TopicName)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	opts.CLSLogsetID = clsResources.LogsetID
	opts.CLSTopicID = clsResources.TopicID
	zipPath := opts.ZipPath
	if zipPath == "" {
		result, err := packageCollectorFunction(ctx, opts.collectorPackageOptions)
		if err != nil {
			return collectorPublishSummary{}, err
		}
		zipPath = result.Path
	}
	if err := collectorpackager.ValidateSCFPackageCLSTopic(zipPath, opts.CLSTopicID); err != nil {
		return collectorPublishSummary{}, err
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return collectorPublishSummary{}, err
	}

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

	createItem, err := buildCollectorCreateNodeItem(opts, uploadResp.PackageID)
	if err != nil {
		return summary, err
	}
	createResp, err := client.BatchCreateNodes(ctx, []adminclient.NodeCreateItem{createItem})
	if err != nil {
		return summary, err
	}
	summary.CreateBatchID = createResp.BatchID
	summary.CreateProcessedCount = createResp.ProcessedCount
	return summary, nil
}

func buildCollectorCreateNodeItem(opts collectorPublishOptions, packageID string) (adminclient.NodeCreateItem, error) {
	packageName := defaultFlag(opts.PackageName, "moox-collector")
	bizType := defaultFlag(opts.BizType, "data_collector")
	environment, err := collectorFunctionEnvironment(opts, packageID)
	if err != nil {
		return adminclient.NodeCreateItem{}, err
	}
	config := parseCollectorOverrides(opts.Config)
	if config == nil {
		config = make(map[string]string)
	}
	config["timeout"] = "60"
	setDefaultEnv(config, "cls_logset_id", opts.CLSLogsetID)
	setDefaultEnv(config, "cls_topic_id", opts.CLSTopicID)
	return adminclient.NodeCreateItem{
		CloudAccountID: opts.CloudAccountID,
		NodeType:       defaultFlag(opts.NodeType, "scf-event"),
		Runtime:        defaultFlag(opts.Runtime, "Go1"),
		Handler:        defaultFlag(opts.Handler, "main"),
		Config:         config,
		Environment:    environment,
		Region:         opts.Region,
		PackageID:      packageID,
		Metadata: map[string]any{
			"function_name_prefix": packageName,
			"biz_type":             bizType,
			"supported_workloads":  []string{"collect.kline", "collect.symbol"},
		},
	}, nil
}

func collectorFunctionEnvironment(opts collectorPublishOptions, packageIDs ...string) (map[string]string, error) {
	packageID := ""
	if len(packageIDs) > 0 {
		packageID = packageIDs[0]
	}
	env := map[string]string{}
	setDefaultEnv(env, "MOOX_SPACE_ID", defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID")))
	setDefaultEnv(env, "MOOX_GATEWAY_NODE_ID", os.Getenv("MOOX_GATEWAY_NODE_ID"))
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_KEY_ID", defaultFlag(opts.ServiceAccessKey, os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID")))
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_SECRET_KEY", defaultFlag(opts.ServiceSecretKey, os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY")))
	clsHost := firstNonEmpty(os.Getenv("MOOX_CLS_HOST"), clsprepare.Host)
	clsSecretID := firstNonEmpty(opts.CLSSecretID, os.Getenv("MOOX_CLS_SECRET_ID"), os.Getenv("TENCENTCLOUD_SECRET_ID"))
	clsSecretKey := firstNonEmpty(opts.CLSSecretKey, os.Getenv("MOOX_CLS_SECRET_KEY"), os.Getenv("TENCENTCLOUD_SECRET_KEY"))
	setDefaultEnv(env, "MOOX_CLS_HOST", clsHost)
	setDefaultEnv(env, "MOOX_CLS_SECRET_ID", clsSecretID)
	setDefaultEnv(env, "MOOX_CLS_SECRET_KEY", clsSecretKey)
	overrides := parseCollectorOverrides(opts.Env)
	managed := map[string]struct{}{
		"MOOX_EVENTBUS_NATS_URL":            {},
		"MOOX_EVENTBUS_NATS_USERNAME":       {},
		"MOOX_EVENTBUS_NATS_PASSWORD":       {},
		"MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64": {},
		"MOOX_CODE_PACKAGE_ID":              {},
		"MOOX_CLS_SECRET_ID":                {},
		"MOOX_CLS_SECRET_KEY":               {},
	}
	for key := range overrides {
		if _, ok := managed[key]; ok {
			return nil, fmt.Errorf("--env must not override managed key %s", key)
		}
	}
	if packageID != "" && opts.EventBusCredentialFile != "" {
		credentialPath := jetstream.ExpandCredentialPath(opts.EventBusCredentialFile)
		credential, err := jetstream.LoadCredentialFile(credentialPath)
		if err != nil {
			return nil, err
		}
		if len(credential.URLs) != 1 {
			return nil, fmt.Errorf("cloudnode-worker credential must contain exactly one EventBus URL")
		}
		eventBusURL, err := url.Parse(credential.URLs[0])
		if err != nil || eventBusURL.Scheme != "tls" || eventBusURL.Hostname() == "" || eventBusURL.Port() == "" {
			return nil, fmt.Errorf("cloudnode-worker credential URL must be tls with host and port")
		}
		if host := eventBusURL.Hostname(); host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
			return nil, fmt.Errorf("SCF EventBus URL must not use a loopback host")
		}
		caPath := credential.CAFile
		if caPath == "" {
			return nil, fmt.Errorf("cloudnode-worker credential requires ca_file")
		}
		if !filepath.IsAbs(caPath) {
			caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
		}
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read EventBus CA file: %w", err)
		}
		env["MOOX_EVENTBUS_NATS_URL"] = credential.URLs[0]
		env["MOOX_EVENTBUS_NATS_USERNAME"] = credential.Username
		env["MOOX_EVENTBUS_NATS_PASSWORD"] = credential.Password
		env["MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64"] = base64.StdEncoding.EncodeToString(caPEM)
		env["MOOX_CODE_PACKAGE_ID"] = packageID
	}
	if strings.TrimSpace(overrides["MOOX_EVENTBUS_NATS_TLS_CA_FILE"]) != "" {
		return nil, fmt.Errorf("serverless environment must not contain MOOX_EVENTBUS_NATS_TLS_CA_FILE")
	}
	if strings.TrimSpace(overrides["MOOX_GATEWAY_CA_FILE"]) != "" {
		return nil, fmt.Errorf("serverless environment must not contain MOOX_GATEWAY_CA_FILE")
	}
	caFile := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_FILE"))
	caMaterial := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_PEM_B64"))
	if overrideMaterial := strings.TrimSpace(overrides["MOOX_GATEWAY_CA_PEM_B64"]); overrideMaterial != "" {
		if caFile != "" || (caMaterial != "" && caMaterial != overrideMaterial) {
			return nil, fmt.Errorf("gateway CA material conflicts with host configuration")
		}
		caMaterial = overrideMaterial
	}
	if caFile != "" && caMaterial != "" {
		return nil, fmt.Errorf("gateway CA file and CA PEM material are mutually exclusive")
	}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("read gateway CA file: %w", err)
		}
		caMaterial = base64.StdEncoding.EncodeToString(pem)
	}
	if caMaterial != "" {
		if _, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{CAPEMBase64: caMaterial}); err != nil {
			return nil, fmt.Errorf("invalid gateway CA material: %w", err)
		}
		setDefaultEnv(env, "MOOX_GATEWAY_CA_PEM_B64", caMaterial)
	}
	for key, value := range overrides {
		if key == "MOOX_GATEWAY_CA_FILE" || key == "MOOX_GATEWAY_CA_PEM_B64" || key == "MOOX_EVENTBUS_NATS_TLS_CA_FILE" {
			continue
		}
		env[key] = value
	}
	if strings.TrimSpace(env["MOOX_CLS_HOST"]) == "" || strings.TrimSpace(env["MOOX_CLS_SECRET_ID"]) == "" || strings.TrimSpace(env["MOOX_CLS_SECRET_KEY"]) == "" {
		return nil, fmt.Errorf("CLS runtime host and credentials are required; set MOOX_CLS_* or TENCENTCLOUD_SECRET_ID/TENCENTCLOUD_SECRET_KEY")
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
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
	accessKey := defaultFlag(serviceAccessKey, os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"))
	secretKey := defaultFlag(serviceSecretKey, os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"))
	if accessKey != "" && secretKey != "" {
		client.ServiceAuth = &adminclient.ServiceAuthConfig{
			AccessKey:  accessKey,
			SecretKey:  secretKey,
			Caller:     defaultFlag(os.Getenv("MOOX_GATEWAY_CALLER"), "moox-cli"),
			TargetNode: defaultFlag(os.Getenv("MOOX_GATEWAY_TARGET_NODE"), os.Getenv("MOOX_GATEWAY_NODE_ID")),
			CAFile:     os.Getenv("MOOX_GATEWAY_CA_FILE"),
			ExpireSecs: 60,
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
