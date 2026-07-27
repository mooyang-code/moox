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
	"strconv"
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

const defaultCollectorSCFTimeout = "120"

var defaultCollectorJobTypes = []string{
	"collect.binance.kline",
	"collect.binance.symbol",
}

type collectorPackageOptions struct {
	CollectorRoot            string
	Version                  string
	Out                      string
	ConfigDir                string
	CLSLogsetID              string
	CLSTopicID               string
	StoragePrimaryAuthSecret string
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
	JobTypes               []string
	Env                    []string
	Config                 []string
	EventBusCredentialFile string
	NodeCount              int
	CreateBatchSize        int
	FunctionNamePrefix     string
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
	ZipPath              string   `json:"zip_path"`
	PackageID            string   `json:"package_id"`
	FleetMode            string   `json:"fleet_mode"`
	CreateBatchIDs       []string `json:"create_batch_ids,omitempty"`
	DeployBatchIDs       []string `json:"deploy_batch_ids,omitempty"`
	CreateProcessedCount int      `json:"create_processed_count,omitempty"`
	DeployProcessedCount int      `json:"deploy_processed_count,omitempty"`
}

type collectorDeploySummary struct {
	ZipPath              string `json:"zip_path"`
	PackageID            string `json:"package_id"`
	DeployBatchID        string `json:"deploy_batch_id"`
	DeployProcessedCount int    `json:"deploy_processed_count"`
}

type collectorFleetAPI interface {
	ListCloudNodes(context.Context, adminclient.CloudNodeListFilter) ([]adminclient.CloudNode, error)
	BatchCreateNodes(context.Context, []adminclient.NodeCreateItem) (*adminclient.BatchChangeResponse, error)
	BatchDeployNodes(context.Context, []adminclient.NodeDeployItem) (*adminclient.BatchChangeResponse, error)
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
	collectorFunctionPublishCmd.Flags().StringSliceVar(&collectorPublishFlags.JobTypes, "job-types", defaultCollectorJobTypes, "JobTypes consumed by this SCF deployment")
	collectorFunctionPublishCmd.Flags().StringArrayVar(&collectorPublishFlags.Env, "env", nil, "SCF environment variable as KEY=VALUE")
	collectorFunctionPublishCmd.Flags().StringArrayVar(&collectorPublishFlags.Config, "function-config", nil, "cloudnode node runtime config as KEY=VALUE; not written into SCF package config.yaml")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.EventBusCredentialFile, "eventbus-credential-file", "~/.config/moox/eventbus/cloudnode-worker.yaml", "0600 cloudnode-worker EventBus credential YAML")
	collectorFunctionPublishCmd.Flags().IntVar(&collectorPublishFlags.NodeCount, "node-count", 50, "number of SCF nodes in the collector fleet")
	collectorFunctionPublishCmd.Flags().IntVar(&collectorPublishFlags.CreateBatchSize, "create-batch-size", 5, "nodes submitted in each serial create batch")
	collectorFunctionPublishCmd.Flags().StringVar(&collectorPublishFlags.FunctionNamePrefix, "function-name-prefix", "moox-collector", "stable function name prefix used to identify the fleet")
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
		BinaryPath:               binaryPath,
		ConfigDir:                configDir,
		OutPath:                  outPath,
		CLSTopicID:               clsTopicID,
		StoragePrimaryAuthSecret: firstNonEmpty(opts.StoragePrimaryAuthSecret, os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")),
	})
}

var newCollectorCLSAPI = func(secretID, secretKey string) (tencent.CLSAPI, error) {
	return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{
		SecretID:  secretID,
		SecretKey: secretKey,
		Region:    clsprepare.Region,
	})
}

func resolveCollectorCLSResources(ctx context.Context) (tencent.CLSBootstrapResult, error) {
	secretID, secretKey := collectorCLSCredentials()
	api, err := newCollectorCLSAPI(secretID, secretKey)
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
	if opts.NodeCount <= 0 {
		return collectorPublishSummary{}, fmt.Errorf("--node-count must be a positive integer")
	}
	if opts.CreateBatchSize <= 0 {
		return collectorPublishSummary{}, fmt.Errorf("--create-batch-size must be a positive integer")
	}
	if err := validateCollectorPublishAuth(opts); err != nil {
		return collectorPublishSummary{}, err
	}
	opts.FunctionNamePrefix = defaultFlag(opts.FunctionNamePrefix, defaultFlag(opts.PackageName, "moox-collector"))
	if _, err := buildCollectorFleetCreateItems(opts, "preflight-package-id"); err != nil {
		return collectorPublishSummary{}, fmt.Errorf("validate collector fleet before control-plane access: %w", err)
	}
	client := newControlClient(opts.ControlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, opts.SpaceID)
	accounts, err := client.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return collectorPublishSummary{}, err
	}
	accountFound := false
	for _, account := range accounts {
		if account.AccountID == opts.CloudAccountID {
			accountFound = true
			break
		}
	}
	if !accountFound {
		return collectorPublishSummary{}, fmt.Errorf("Tencent cloud account %q not found", opts.CloudAccountID)
	}
	opts.CLSSecretID, opts.CLSSecretKey = collectorCLSCredentials()
	clsAPI, err := newCollectorCLSAPI(opts.CLSSecretID, opts.CLSSecretKey)
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
	if _, err := buildCollectorFleetCreateItems(opts, "preflight-package-id"); err != nil {
		return collectorPublishSummary{}, fmt.Errorf("validate collector fleet before package upload: %w", err)
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

	createItems, err := buildCollectorFleetCreateItems(opts, uploadResp.PackageID)
	if err != nil {
		return summary, err
	}
	fleetSummary, err := applyCollectorFleet(ctx, client, opts, uploadResp.PackageID, createItems)
	if err != nil {
		return summary, err
	}
	summary.FleetMode = fleetSummary.FleetMode
	summary.CreateBatchIDs = fleetSummary.CreateBatchIDs
	summary.DeployBatchIDs = fleetSummary.DeployBatchIDs
	summary.CreateProcessedCount = fleetSummary.CreateProcessedCount
	summary.DeployProcessedCount = fleetSummary.DeployProcessedCount
	return summary, nil
}

func validateCollectorPublishAuth(opts collectorPublishOptions) error {
	accessToken := defaultFlag(opts.AccessToken, os.Getenv("MOOX_ACCESS_TOKEN"))
	accessKey := defaultFlag(opts.ServiceAccessKey, os.Getenv("MOOX_GATEWAY_SERVICE_KEY_ID"))
	secretKey := defaultFlag(opts.ServiceSecretKey, os.Getenv("MOOX_GATEWAY_SERVICE_SECRET_KEY"))
	if strings.TrimSpace(accessToken) != "" {
		return nil
	}
	if strings.TrimSpace(accessKey) == "" || strings.TrimSpace(secretKey) == "" {
		return fmt.Errorf(
			"control authentication is required; set MOOX_ACCESS_TOKEN or both " +
				"MOOX_GATEWAY_SERVICE_KEY_ID and MOOX_GATEWAY_SERVICE_SECRET_KEY",
		)
	}
	return nil
}

func applyCollectorFleet(
	ctx context.Context,
	api collectorFleetAPI,
	opts collectorPublishOptions,
	packageID string,
	createItems []adminclient.NodeCreateItem,
) (collectorPublishSummary, error) {
	var summary collectorPublishSummary
	if opts.NodeCount <= 0 {
		return summary, fmt.Errorf("node count must be positive")
	}
	if opts.CreateBatchSize <= 0 {
		return summary, fmt.Errorf("create batch size must be positive")
	}
	if strings.TrimSpace(packageID) == "" {
		return summary, fmt.Errorf("package id is required")
	}
	if len(createItems) != opts.NodeCount {
		return summary, fmt.Errorf(
			"collector fleet create item count=%d; expected %d",
			len(createItems),
			opts.NodeCount,
		)
	}
	catalogNodes, err := api.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		CloudAccountID: opts.CloudAccountID,
		Region:         opts.Region,
		NodeType:       defaultFlag(opts.NodeType, "scf-event"),
		BizType:        defaultFlag(opts.BizType, "data_collector"),
	})
	if err != nil {
		return summary, err
	}
	fleetNodes, err := selectCollectorFleetNodes(catalogNodes, opts.FunctionNamePrefix, opts.NodeCount)
	if err != nil {
		return summary, err
	}
	if len(fleetNodes) == 0 {
		summary.FleetMode = "created"
		for start := 0; start < len(createItems); start += opts.CreateBatchSize {
			end := min(start+opts.CreateBatchSize, len(createItems))
			resp, err := api.BatchCreateNodes(ctx, createItems[start:end])
			if err != nil {
				return summary, err
			}
			if resp.ProcessedCount != end-start {
				return summary, fmt.Errorf("BatchCreateNodes processed %d nodes; expected %d", resp.ProcessedCount, end-start)
			}
			summary.CreateBatchIDs = append(summary.CreateBatchIDs, resp.BatchID)
			summary.CreateProcessedCount += resp.ProcessedCount
		}
		return summary, nil
	}

	summary.FleetMode = "updated"
	const deployBatchSize = 10
	deployments := make([]adminclient.NodeDeployItem, len(fleetNodes))
	for index, node := range fleetNodes {
		deployments[index] = adminclient.NodeDeployItem{
			NodeID:      node.NodeID,
			PackageID:   packageID,
			Config:      cloneCollectorStringMap(createItems[index].Config),
			Environment: cloneCollectorStringMap(createItems[index].Environment),
		}
	}
	for start := 0; start < len(deployments); start += deployBatchSize {
		end := min(start+deployBatchSize, len(deployments))
		resp, err := api.BatchDeployNodes(ctx, deployments[start:end])
		if err != nil {
			return summary, err
		}
		if resp.ProcessedCount != end-start {
			return summary, fmt.Errorf("BatchDeployNodes processed %d nodes; expected %d", resp.ProcessedCount, end-start)
		}
		summary.DeployBatchIDs = append(summary.DeployBatchIDs, resp.BatchID)
		summary.DeployProcessedCount += resp.ProcessedCount
	}
	return summary, nil
}

func collectorCLSCredentials() (string, string) {
	return firstNonEmpty(os.Getenv("MOOX_CLS_SECRET_ID"), os.Getenv("TENCENTCLOUD_SECRET_ID")),
		firstNonEmpty(os.Getenv("MOOX_CLS_SECRET_KEY"), os.Getenv("TENCENTCLOUD_SECRET_KEY"))
}

func buildCollectorCreateNodeItem(opts collectorPublishOptions, packageID string) (adminclient.NodeCreateItem, error) {
	packageName := defaultFlag(opts.PackageName, "moox-collector")
	bizType := defaultFlag(opts.BizType, "data_collector")
	jobTypes, err := resolveCollectorPublishJobTypes(opts.JobTypes)
	if err != nil {
		return adminclient.NodeCreateItem{}, err
	}
	opts.JobTypes = jobTypes
	environment, err := collectorFunctionEnvironment(opts, packageID)
	if err != nil {
		return adminclient.NodeCreateItem{}, err
	}
	config := parseCollectorOverrides(opts.Config)
	if config == nil {
		config = make(map[string]string)
	}
	if strings.TrimSpace(config["timeout"]) == "" {
		config["timeout"] = defaultCollectorSCFTimeout
	}
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
			"supported_workloads":  append([]string(nil), jobTypes...),
		},
	}, nil
}

func buildCollectorFleetCreateItems(opts collectorPublishOptions, packageID string) ([]adminclient.NodeCreateItem, error) {
	if opts.NodeCount <= 0 {
		return nil, fmt.Errorf("node count must be positive")
	}
	base, err := buildCollectorCreateNodeItem(opts, packageID)
	if err != nil {
		return nil, err
	}
	if err := validateCollectorFleetRuntimeEnvironment(base.Environment); err != nil {
		return nil, err
	}
	prefix := defaultFlag(opts.FunctionNamePrefix, defaultFlag(opts.PackageName, "moox-collector"))
	items := make([]adminclient.NodeCreateItem, opts.NodeCount)
	for index := range items {
		items[index] = cloneCollectorNodeCreateItem(base)
		items[index].Metadata["function_name_prefix"] = prefix
		items[index].Metadata["index"] = index
	}
	return items, nil
}

func validateCollectorFleetRuntimeEnvironment(environment map[string]string) error {
	required := []string{
		"MOOX_SPACE_ID",
		"MOOX_COLLECTOR_JOB_TYPES",
		"MOOX_EVENTBUS_NATS_URL",
		"MOOX_EVENTBUS_NATS_USERNAME",
		"MOOX_EVENTBUS_NATS_PASSWORD",
		"MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64",
		"MOOX_CODE_PACKAGE_ID",
		"MOOX_GATEWAY_NODE_ID",
		"MOOX_GATEWAY_TARGET_NODE",
		"MOOX_GATEWAY_SERVICE_KEY_ID",
		"MOOX_GATEWAY_SERVICE_SECRET_KEY",
		"MOOX_GATEWAY_CA_PEM_B64",
		"MOOX_CLS_HOST",
		"MOOX_CLS_SECRET_ID",
		"MOOX_CLS_SECRET_KEY",
	}
	for _, key := range required {
		if strings.TrimSpace(environment[key]) == "" {
			return fmt.Errorf("collector fleet runtime environment requires %s", key)
		}
	}
	return nil
}

func cloneCollectorNodeCreateItem(source adminclient.NodeCreateItem) adminclient.NodeCreateItem {
	cloned := source
	cloned.Config = cloneCollectorStringMap(source.Config)
	cloned.Environment = cloneCollectorStringMap(source.Environment)
	cloned.Metadata = make(map[string]any, len(source.Metadata))
	for key, value := range source.Metadata {
		switch typed := value.(type) {
		case []string:
			cloned.Metadata[key] = append([]string(nil), typed...)
		case map[string]string:
			cloned.Metadata[key] = cloneCollectorStringMap(typed)
		default:
			cloned.Metadata[key] = value
		}
	}
	return cloned
}

func cloneCollectorStringMap(source map[string]string) map[string]string {
	cloned := make(map[string]string, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func selectCollectorFleetNodes(nodes []adminclient.CloudNode, prefix string, expected int) ([]adminclient.CloudNode, error) {
	indexed := make([]adminclient.CloudNode, expected)
	found := make([]bool, expected)
	count := 0
	for _, node := range nodes {
		index, belongs, err := collectorFleetIndex(node, prefix)
		if err != nil {
			return nil, err
		}
		if node.IsDeleted || !belongs {
			continue
		}
		count++
		if index < 0 || index >= expected {
			return nil, fmt.Errorf("fleet prefix %q has invalid index metadata on node %q", prefix, node.NodeID)
		}
		if found[index] {
			return nil, fmt.Errorf("fleet prefix %q has duplicate fleet index %d", prefix, index)
		}
		found[index] = true
		indexed[index] = node
	}
	if count == 0 {
		return nil, nil
	}
	if count != expected {
		return nil, fmt.Errorf("fleet prefix %q has %d nodes; expected either 0 or %d", prefix, count, expected)
	}
	for index, present := range found {
		if !present {
			return nil, fmt.Errorf("fleet prefix %q is missing fleet index %d", prefix, index)
		}
	}
	return indexed, nil
}

func collectorFleetIndex(node adminclient.CloudNode, prefix string) (int, bool, error) {
	metadataPrefix := metadataStringValue(node.Metadata, "function_name_prefix")
	if metadataPrefix != "" {
		if metadataPrefix != prefix {
			return 0, false, nil
		}
		index, ok := metadataIntValue(node.Metadata, "index")
		if !ok {
			return 0, true, fmt.Errorf("fleet prefix %q has invalid index metadata on node %q", prefix, node.NodeID)
		}
		return index, true, nil
	}

	stablePrefix := fmt.Sprintf("%s-%s-", prefix, node.Region)
	if !strings.HasPrefix(node.FunctionName, stablePrefix) {
		return 0, false, nil
	}
	rawIndex := strings.TrimPrefix(node.FunctionName, stablePrefix)
	index, err := strconv.Atoi(rawIndex)
	if err != nil || index < 0 {
		return 0, true, fmt.Errorf("fleet prefix %q has invalid function name %q", prefix, node.FunctionName)
	}
	return index, true, nil
}

func metadataStringValue(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return value
}

func metadataIntValue(metadata map[string]any, key string) (int, bool) {
	switch value := metadata[key].(type) {
	case int:
		return value, true
	case float64:
		integer := int(value)
		return integer, value == float64(integer)
	default:
		return 0, false
	}
}

func collectorFunctionEnvironment(opts collectorPublishOptions, packageIDs ...string) (map[string]string, error) {
	jobTypes, err := resolveCollectorPublishJobTypes(opts.JobTypes)
	if err != nil {
		return nil, err
	}
	packageID := ""
	if len(packageIDs) > 0 {
		packageID = packageIDs[0]
	}
	env := map[string]string{}
	setDefaultEnv(env, "MOOX_COLLECTOR_JOB_TYPES", strings.Join(jobTypes, ","))
	setDefaultEnv(env, "MOOX_SPACE_ID", defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID")))
	gatewayNodeID := firstNonEmpty(os.Getenv("MOOX_GATEWAY_NODE_ID"), os.Getenv("MOOX_GATEWAY_TARGET_NODE"))
	setDefaultEnv(env, "MOOX_GATEWAY_NODE_ID", gatewayNodeID)
	setDefaultEnv(env, "MOOX_GATEWAY_TARGET_NODE", gatewayNodeID)
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_KEY_ID", os.Getenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID"))
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_SECRET_KEY", os.Getenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY"))
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
		"MOOX_GATEWAY_SERVICE_KEY_ID":       {},
		"MOOX_GATEWAY_SERVICE_SECRET_KEY":   {},
		"MOOX_GATEWAY_NODE_ID":              {},
		"MOOX_GATEWAY_TARGET_NODE":          {},
		"MOOX_COLLECTOR_JOB_TYPES":          {},
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

func resolveCollectorPublishJobTypes(values []string) ([]string, error) {
	if len(values) == 0 {
		return append([]string(nil), defaultCollectorJobTypes...), nil
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		jobType := strings.TrimSpace(value)
		if jobType == "" {
			return nil, fmt.Errorf("collector job type must not be empty")
		}
		switch jobType {
		case "collect.binance.kline", "collect.binance.symbol":
		default:
			return nil, fmt.Errorf("unsupported collector job type %q", jobType)
		}
		if _, ok := seen[jobType]; ok {
			continue
		}
		seen[jobType] = struct{}{}
		result = append(result, jobType)
	}
	return result, nil
}

func setDefaultEnv(env map[string]string, key string, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	env[key] = value
}

func deployCollectorFunction(ctx context.Context, opts collectorDeployOptions) (collectorDeploySummary, error) {
	if opts.ControlURL == "" {
		return collectorDeploySummary{}, fmt.Errorf("--control-url is required")
	}
	if opts.CloudAccountID == "" {
		return collectorDeploySummary{}, fmt.Errorf("--cloud-account-id is required")
	}
	if opts.NodeID == "" {
		return collectorDeploySummary{}, fmt.Errorf("--node-id is required")
	}
	zipPath := opts.ZipPath
	if zipPath == "" {
		result, err := packageCollectorFunction(ctx, opts.collectorPackageOptions)
		if err != nil {
			return collectorDeploySummary{}, err
		}
		zipPath = result.Path
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return collectorDeploySummary{}, err
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
		return collectorDeploySummary{}, err
	}
	summary := collectorDeploySummary{
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
