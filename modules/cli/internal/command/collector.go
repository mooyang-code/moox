package command

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/clsprepare"
	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	defaultCollectorSCFTimeout   = "15"
	maxCollectorPublishNodeCount = 100
)

type collectorPackageOptions struct {
	CollectorRoot            string
	SpaceID                  string
	PackageConfigDir         string
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
	Namespace              string
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
	FunctionNamePrefix     string
	File                   string
	FetcherConfig          *setupconfig.SCFFetcherSpace
	CLSSecretID            string
	CLSSecretKey           string
	CLSHost                string
}

type collectorPublishStatusOptions struct {
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	SpaceID          string
	JobID            string
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

type collectorDeleteOptions struct {
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	SpaceID          string
	Region           string
	Confirm          bool
	DryRun           bool
	Wait             bool
}

type collectorProbeOptions struct {
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	SpaceID          string
	Region           string
}

var collectorDeployFlags collectorDeployOptions
var collectorDeleteFlags collectorDeleteOptions
var collectorProbeFlags collectorProbeOptions

var collectorFunctionDeployCmd = &cobra.Command{
	Use:   "deploy",
	Short: "上传数据采集器云函数并提交单节点异步部署",
	Long: "上传数据采集器云函数并提交单节点异步部署。\n" +
		"使用 moox-cli collector function publish status --job-id <id> 查询结果。",
	Args: cobra.NoArgs,
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

var collectorFunctionDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "异步删除当前 space 中全部 SCF 云函数",
	Long: "列出当前 space 的 scf-event 节点，并通过 CloudNode 提交可恢复查询的异步删除任务；可按地域筛选。\n" +
		"必须显式指定 --confirm；使用 --dry-run 只列出目标而不提交删除。",
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := deleteCollectorFunctions(cmd.Context(), collectorDeleteFlags)
		if summary != nil {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if encodeErr := enc.Encode(summary); encodeErr != nil && err == nil {
				return encodeErr
			}
		}
		return err
	},
}

var collectorFunctionProbeCmd = &cobra.Command{
	Use:   "probe-egress",
	Short: "逐个调用短时 SCF 验证公网出口和 Binance 连通性",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		report, err := probeCollectorEgress(cmd.Context(), collectorProbeFlags)
		encErr := json.NewEncoder(cmd.OutOrStdout()).Encode(report)
		if err != nil {
			return err
		}
		return encErr
	},
}

var collectorPackageFlags collectorPackageOptions
var collectorPublishFlags collectorPublishOptions
var collectorPublishStatusFlags collectorPublishStatusOptions

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
	Short: "提交并查询数据采集器云函数发布任务",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		_ = cmd.Help()
	},
}

var collectorFunctionPublishSubmitCmd = &cobra.Command{
	Use:   "submit",
	Short: "上传并提交数据采集器云函数 fleet 发布任务",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := publishCollectorFunction(cmd.Context(), collectorPublishFlags)
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if encodeErr := enc.Encode(summary); encodeErr != nil {
			return encodeErr
		}
		return err
	},
}

var collectorFunctionPublishStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查询数据采集器云函数发布任务",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		status, err := publishCollectorFunctionStatus(cmd.Context(), collectorPublishStatusFlags)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	},
}

type collectorPublishSummary struct {
	ZipPath    string                          `json:"zip_path"`
	PackageID  string                          `json:"package_id"`
	CLSTopicID string                          `json:"cls_topic_id"`
	FleetMode  string                          `json:"fleet_mode"`
	JobID      string                          `json:"job_id"`
	JobIDs     []string                        `json:"job_ids,omitempty"`
	Operation  string                          `json:"operation"`
	TotalCount int                             `json:"total_count"`
	Regions    []collectorPublishRegionSummary `json:"regions,omitempty"`
}

type collectorPublishRegionSummary struct {
	Region         string `json:"region"`
	CloudAccountID string `json:"cloud_account_id"`
	PackageID      string `json:"package_id"`
	CLSLogsetID    string `json:"cls_logset_id"`
	CLSTopicID     string `json:"cls_topic_id"`
	JobID          string `json:"job_id"`
	TotalCount     int    `json:"total_count"`
}

type collectorDeploySummary struct {
	ZipPath    string `json:"zip_path"`
	PackageID  string `json:"package_id"`
	JobID      string `json:"job_id"`
	Operation  string `json:"operation"`
	TotalCount int    `json:"total_count"`
}

type collectorDeleteSummary struct {
	SpaceID       string                            `json:"space_id"`
	Region        string                            `json:"region,omitempty"`
	NodeType      string                            `json:"node_type"`
	DryRun        bool                              `json:"dry_run"`
	TotalCount    int                               `json:"total_count"`
	NodeIDs       []string                          `json:"node_ids,omitempty"`
	JobID         string                            `json:"job_id,omitempty"`
	Operation     string                            `json:"operation,omitempty"`
	Status        string                            `json:"status,omitempty"`
	SuccessCount  int                               `json:"success_count,omitempty"`
	FailedCount   int                               `json:"failed_count,omitempty"`
	FailedResults []adminclient.NodeBatchItemResult `json:"failed_results,omitempty"`
}

type collectorFleetAPI interface {
	ListCloudNodes(context.Context, adminclient.CloudNodeListFilter) ([]adminclient.CloudNode, error)
	SubmitCreateNodes(context.Context, []adminclient.NodeCreateItem) (*adminclient.SubmitNodeBatchResponse, error)
	SubmitDeployNodes(context.Context, []adminclient.NodeDeployItem) (*adminclient.SubmitNodeBatchResponse, error)
}

func init() {
	rootCmd.AddCommand(collectorCmd)
	collectorCmd.AddCommand(collectorFunctionCmd)
	collectorFunctionCmd.AddCommand(collectorFunctionPackageCmd, collectorFunctionPublishCmd, collectorFunctionDeployCmd, collectorFunctionDeleteCmd, collectorFunctionProbeCmd)
	collectorFunctionPublishCmd.AddCommand(collectorFunctionPublishSubmitCmd, collectorFunctionPublishStatusCmd)

	addCollectorPackageFlags(collectorFunctionPackageCmd, &collectorPackageFlags)
	addCollectorPackageFlags(collectorFunctionPublishSubmitCmd, &collectorPublishFlags.collectorPackageOptions)
	addCollectorPackageFlags(collectorFunctionDeployCmd, &collectorDeployFlags.collectorPackageOptions)

	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ControlURL, "control-url", "", "Control service base URL")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 access_key")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	collectorFunctionPackageCmd.Flags().StringVar(&collectorPackageFlags.SpaceID, "space-id", "", "space id; selects configs/scf/<space-id>")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.CloudAccountID, "cloud-account-id", "", "cloud account id")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.NodeID, "node-id", "", "existing cloud node id / function name")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.ZipPath, "zip", "", "existing SCF zip path")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.PackageName, "package-name", "moox-collector", "function package name")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.PackageType, "package-type", "data_collector", "function package type")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.BizType, "biz-type", "market_fetcher", "business type")
	collectorFunctionDeployCmd.Flags().StringVar(&collectorDeployFlags.Runtime, "runtime", "Go1", "SCF runtime")

	deleteFlags := collectorFunctionDeleteCmd.Flags()
	deleteFlags.StringVar(&collectorDeleteFlags.ControlURL, "control-url", "", "Control service base URL")
	deleteFlags.StringVar(&collectorDeleteFlags.AccessToken, "access-token", "", "Control access token; defaults to MOOX_ACCESS_TOKEN")
	deleteFlags.StringVar(&collectorDeleteFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 key_id; 默认取 MOOX_GATEWAY_SERVICE_KEY_ID")
	deleteFlags.StringVar(&collectorDeleteFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key; 默认取 MOOX_GATEWAY_SERVICE_SECRET_KEY")
	deleteFlags.StringVar(&collectorDeleteFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	deleteFlags.StringVar(&collectorDeleteFlags.Region, "region", "", "只删除指定地域的 SCF 节点")
	deleteFlags.BoolVar(&collectorDeleteFlags.Confirm, "confirm", false, "确认删除当前筛选范围内的 SCF 节点")
	deleteFlags.BoolVar(&collectorDeleteFlags.DryRun, "dry-run", false, "只列出目标，不提交删除任务")
	deleteFlags.BoolVar(&collectorDeleteFlags.Wait, "wait", true, "提交后等待异步任务完成")
	probeFlags := collectorFunctionProbeCmd.Flags()
	probeFlags.StringVar(&collectorProbeFlags.ControlURL, "control-url", "", "Control service base URL")
	probeFlags.StringVar(&collectorProbeFlags.AccessToken, "access-token", "", "Control access token")
	probeFlags.StringVar(&collectorProbeFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名 access key")
	probeFlags.StringVar(&collectorProbeFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名 secret key")
	probeFlags.StringVar(&collectorProbeFlags.SpaceID, "space-id", "", "space id")
	probeFlags.StringVar(&collectorProbeFlags.Region, "region", "", "只探测指定地域")

	submitFlags := collectorFunctionPublishSubmitCmd.Flags()
	submitFlags.StringVar(&collectorPublishFlags.ControlURL, "control-url", "", "Control service base URL")
	submitFlags.StringVar(&collectorPublishFlags.AccessToken, "access-token", "", "Control access token; defaults to MOOX_ACCESS_TOKEN (登录态, 不推荐)")
	submitFlags.StringVar(&collectorPublishFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 key_id; 默认取 MOOX_GATEWAY_SERVICE_KEY_ID")
	submitFlags.StringVar(&collectorPublishFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key; 默认取 MOOX_GATEWAY_SERVICE_SECRET_KEY")
	submitFlags.StringVar(&collectorPublishFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	submitFlags.StringVar(&collectorPublishFlags.CloudAccountID, "cloud-account-id", "", "cloud account id")
	submitFlags.StringVar(&collectorPublishFlags.Runtime, "runtime", "", "SCF runtime (defaults to Go1)")
	submitFlags.StringVar(&collectorPublishFlags.Namespace, "namespace", "", "SCF namespace (defaults to default)")
	submitFlags.StringVar(&collectorPublishFlags.Handler, "handler", "main", "SCF handler")
	submitFlags.StringVar(&collectorPublishFlags.Region, "region", "", "cloud region")
	submitFlags.StringVar(&collectorPublishFlags.ZipPath, "zip", "", "existing SCF zip path")
	submitFlags.StringVar(&collectorPublishFlags.PackageName, "package-name", "moox-collector", "function package name")
	submitFlags.StringVar(&collectorPublishFlags.PackageType, "package-type", "data_collector", "function package type")
	submitFlags.StringVar(&collectorPublishFlags.BizType, "biz-type", "market_fetcher", "business type")
	submitFlags.StringVar(&collectorPublishFlags.NodeType, "node-type", "scf-event", "cloud node type")
	submitFlags.StringArrayVar(&collectorPublishFlags.Env, "env", nil, "SCF environment variable as KEY=VALUE")
	submitFlags.StringArrayVar(&collectorPublishFlags.Config, "function-config", nil, "cloudnode node runtime config as KEY=VALUE; not written into SCF package config.yaml")
	submitFlags.StringVar(&collectorPublishFlags.EventBusCredentialFile, "eventbus-credential-file", "~/.config/moox/eventbus/market-fetch-publisher.yaml", "0600 market-fetch-publisher EventBus credential YAML")
	submitFlags.IntVar(&collectorPublishFlags.NodeCount, "node-count", 50, "number of SCF nodes in the collector fleet")
	submitFlags.StringVar(&collectorPublishFlags.FunctionNamePrefix, "function-name-prefix", "", "stable function name prefix used to identify the fleet")
	submitFlags.StringVar(&collectorPublishFlags.File, "file", "", "custom.toml; when scf_fetcher is enabled, regions and function counts are read from it")

	statusFlags := collectorFunctionPublishStatusCmd.Flags()
	statusFlags.StringVar(&collectorPublishStatusFlags.ControlURL, "control-url", "", "Control service base URL")
	statusFlags.StringVar(&collectorPublishStatusFlags.AccessToken, "access-token", "", "Control access token; defaults to MOOX_ACCESS_TOKEN")
	statusFlags.StringVar(&collectorPublishStatusFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 key_id")
	statusFlags.StringVar(&collectorPublishStatusFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key")
	statusFlags.StringVar(&collectorPublishStatusFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	statusFlags.StringVar(&collectorPublishStatusFlags.JobID, "job-id", "", "node batch job id")
}

func addCollectorPackageFlags(cmd *cobra.Command, opts *collectorPackageOptions) {
	cmd.Flags().StringVar(&opts.CollectorRoot, "collector-root", "", "collector module root")
	cmd.Flags().StringVar(&opts.Version, "version", "dev", "collector package version")
	cmd.Flags().StringVar(&opts.Out, "out", "", "output zip path")
	cmd.Flags().StringVar(&opts.ConfigDir, "config", "", "collector config directory")
	cmd.Flags().StringVar(&opts.CLSTopicID, "cls-topic-id", "", "central CLS topic id used by SCF warning/error logs")
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
		spaceID := strings.TrimSpace(opts.SpaceID)
		if spaceID == "" {
			return nil, fmt.Errorf("--space-id is required when --out is omitted")
		}
		outPath = filepath.Join(collectorRoot, fmt.Sprintf("collector-scf-%s-%s.zip", spaceID, version))
	}
	configDir := opts.ConfigDir
	if configDir == "" {
		spaceID := strings.TrimSpace(opts.SpaceID)
		if spaceID == "" {
			return nil, fmt.Errorf("--space-id is required when --config is omitted")
		}
		configDir = filepath.Join(collectorRoot, "configs", filepath.FromSlash(defaultFlag(opts.PackageConfigDir, filepath.ToSlash(filepath.Join("scf", spaceID)))))
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
		CLSTopicID:               opts.CLSTopicID,
		StoragePrimaryAuthSecret: firstNonEmpty(opts.StoragePrimaryAuthSecret, os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET")),
	})
}

var newCollectorCLSAPI = func(secretID, secretKey, region string) (tencent.CLSAPI, error) {
	return tencent.NewCLSSDKAPI(tencent.CLSSDKOptions{
		SecretID:  secretID,
		SecretKey: secretKey,
		Region:    region,
	})
}

type collectorCLSSink struct {
	Resources tencent.CLSBootstrapResult
}

func resolveCollectorCLSSink(ctx context.Context, control *adminclient.Client, account adminclient.CloudAccount) (collectorCLSSink, error) {
	if control == nil || strings.TrimSpace(account.CredentialSecretID) == "" {
		return collectorCLSSink{}, fmt.Errorf("Tencent cloud account %q has no CLS credential secret", account.AccountID)
	}
	secret, err := control.GetSecretValue(ctx, account.CredentialSecretID)
	if err != nil {
		return collectorCLSSink{}, fmt.Errorf("reveal Tencent cloud account %q credentials for CLS: %w", account.AccountID, err)
	}
	if secret == nil || secret.Provider != "tencent" || secret.Category != "cloud" || secret.Status != "active" || strings.TrimSpace(secret.KeyID) == "" || strings.TrimSpace(secret.SecretValue) == "" {
		return collectorCLSSink{}, fmt.Errorf("Tencent cloud account %q returned incomplete active cloud credentials for CLS", account.AccountID)
	}
	api, err := newCollectorCLSAPI(secret.KeyID, secret.SecretValue, clsprepare.Region)
	if err != nil {
		return collectorCLSSink{}, fmt.Errorf("create central CLS client for collector package: %w", err)
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resources, err := tencent.ResolveExistingCLS(resolveCtx, api, clsprepare.LogsetName, clsprepare.TopicName)
	if err != nil {
		return collectorCLSSink{}, fmt.Errorf("resolve central CLS topic for collector function: %w", err)
	}
	return collectorCLSSink{Resources: resources}, nil
}

func publishCollectorFunction(ctx context.Context, opts collectorPublishOptions) (collectorPublishSummary, error) {
	if opts.ControlURL == "" {
		return collectorPublishSummary{}, fmt.Errorf("--control-url is required")
	}
	if opts.CloudAccountID == "" && strings.TrimSpace(opts.File) == "" {
		return collectorPublishSummary{}, fmt.Errorf("--cloud-account-id is required")
	}
	fetcherConfig, err := loadCollectorSCFFetcherConfig(opts.File, opts.SpaceID)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	if opts.Region == "" && fetcherConfig == nil {
		return collectorPublishSummary{}, fmt.Errorf("--region is required")
	}
	if fetcherConfig == nil && (opts.NodeCount <= 0 || opts.NodeCount > maxCollectorPublishNodeCount) {
		return collectorPublishSummary{}, fmt.Errorf(
			"--node-count must be between 1 and %d",
			maxCollectorPublishNodeCount,
		)
	}
	if err := validateCollectorPublishAuth(opts); err != nil {
		return collectorPublishSummary{}, err
	}
	if fetcherConfig != nil {
		opts.FetcherConfig = fetcherConfig
		opts.collectorPackageOptions.SpaceID = fetcherConfig.SpaceID
		opts.collectorPackageOptions.PackageConfigDir = fetcherConfig.PackageConfigDir
		opts.Namespace = defaultFlag(fetcherConfig.Namespace, opts.Namespace)
		opts.Runtime = defaultFlag(fetcherConfig.Runtime, opts.Runtime)
		opts.FunctionNamePrefix = defaultFlag(fetcherConfig.FunctionPrefix, opts.FunctionNamePrefix)
		if opts.PackageName == "moox-collector" {
			opts.PackageName = fetcherConfig.PackageName
		}
	}
	prefixDefault := defaultFlag(opts.PackageName, "moox-collector")
	if fetcherConfig != nil {
		prefixDefault = defaultFlag(fetcherConfig.FunctionPrefix, prefixDefault)
	}
	opts.FunctionNamePrefix = defaultFlag(opts.FunctionNamePrefix, prefixDefault)
	if fetcherConfig == nil {
		if _, err := buildCollectorFleetCreateItems(opts, "preflight-package-id"); err != nil {
			return collectorPublishSummary{}, fmt.Errorf("validate collector fleet before control-plane access: %w", err)
		}
	}
	client := newControlClient(opts.ControlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, opts.SpaceID)
	accounts, err := client.ListCloudAccounts(ctx, "tencent")
	if err != nil {
		return collectorPublishSummary{}, err
	}
	accountsByID := make(map[string]adminclient.CloudAccount, len(accounts))
	for _, account := range accounts {
		accountsByID[account.AccountID] = account
	}
	if fetcherConfig != nil {
		if err := ensureCollectorSpaceCloudAccounts(ctx, client, fetcherConfig, accountsByID); err != nil {
			return collectorPublishSummary{}, err
		}
	}
	if fetcherConfig == nil && accountsByID[opts.CloudAccountID].AccountID == "" {
		return collectorPublishSummary{}, fmt.Errorf("Tencent cloud account %q not found", opts.CloudAccountID)
	}
	var preflightFleetNodes []adminclient.CloudNode
	if fetcherConfig == nil {
		preflightFleetNodes, err = inspectCollectorFleet(ctx, client, opts)
		if err != nil {
			return collectorPublishSummary{}, err
		}
	}
	clsAccountID := opts.CloudAccountID
	if fetcherConfig != nil {
		clsAccountID = fetcherConfig.CLSCloudAccountID
	}
	clsAccount, ok := accountsByID[clsAccountID]
	if !ok || clsAccount.IsDeleted {
		return collectorPublishSummary{}, fmt.Errorf("central CLS cloud account %q not found", clsAccountID)
	}
	clsSink, err := resolveCollectorCLSSink(ctx, client, clsAccount)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	opts.CLSLogsetID = clsSink.Resources.LogsetID
	opts.CLSTopicID = clsSink.Resources.TopicID
	opts.CLSSecretID, opts.CLSSecretKey = collectorCLSCredentials()
	if opts.CLSSecretID == "" || opts.CLSSecretKey == "" {
		return collectorPublishSummary{}, fmt.Errorf("MOOX_CLS_SECRET_ID and MOOX_CLS_SECRET_KEY are required for SCF centralized logging")
	}
	opts.CLSHost = clsprepare.Host
	zipPath := opts.ZipPath
	if zipPath == "" {
		result, err := packageCollectorFunction(ctx, opts.collectorPackageOptions)
		if err != nil {
			return collectorPublishSummary{}, err
		}
		zipPath = result.Path
	} else if err := validateCollectorZipLogging(zipPath, opts.CLSTopicID); err != nil {
		return collectorPublishSummary{}, err
	}
	if fetcherConfig == nil {
		if _, err := buildCollectorFleetCreateItems(opts, "preflight-package-id"); err != nil {
			return collectorPublishSummary{}, fmt.Errorf("validate collector fleet before package upload: %w", err)
		}
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		return collectorPublishSummary{}, err
	}

	summary := collectorPublishSummary{ZipPath: zipPath}
	upload := func(regionOpts collectorPublishOptions) (string, error) {
		response, uploadErr := client.UploadPackage(ctx, adminclient.UploadPackageRequest{
			PackageName: defaultFlag(regionOpts.PackageName, "moox-collector"), Version: defaultFlag(regionOpts.Version, "dev"),
			Runtime: defaultFlag(regionOpts.Runtime, "Go1"), PackageType: adminclient.ResolvePackageType(defaultFlag(regionOpts.PackageType, "data_collector")),
			BizType: defaultFlag(regionOpts.BizType, "market_fetcher"), CloudAccountID: regionOpts.CloudAccountID, OriginalFilename: filepath.Base(zipPath),
		}, data)
		if uploadErr != nil {
			return "", uploadErr
		}
		return response.PackageID, nil
	}

	if fetcherConfig != nil {
		opts.FetcherConfig = fetcherConfig
		var jobs []string
		for _, region := range fetcherConfig.Regions {
			if !region.Enabled {
				continue
			}
			regionOpts := opts
			regionOpts.Region = region.Region
			regionOpts.NodeCount = region.FunctionCount
			regionOpts.CloudAccountID = region.CloudAccountID
			account, ok := accountsByID[regionOpts.CloudAccountID]
			if !ok || account.IsDeleted {
				return summary, fmt.Errorf("Tencent cloud account %q for region %s not found", regionOpts.CloudAccountID, regionOpts.Region)
			}
			if account.COSRegion != regionOpts.Region {
				return summary, fmt.Errorf("Tencent cloud account %q COS region %q must match SCF region %q", account.AccountID, account.COSRegion, regionOpts.Region)
			}
			packageID, uploadErr := upload(regionOpts)
			if uploadErr != nil {
				return summary, uploadErr
			}
			fleetNodes, inspectErr := inspectCollectorFleet(ctx, client, regionOpts)
			if inspectErr != nil {
				return summary, inspectErr
			}
			createItems, buildErr := buildCollectorFleetCreateItems(regionOpts, packageID)
			if buildErr != nil {
				return summary, buildErr
			}
			fleetSummary, submitErr := submitCollectorFleet(ctx, client, regionOpts, packageID, createItems, fleetNodes)
			if submitErr != nil {
				summary.JobIDs = append([]string(nil), jobs...)
				summary.JobID = strings.Join(summary.JobIDs, ",")
				return summary, submitErr
			}
			summary.FleetMode = fleetSummary.FleetMode
			summary.Operation = fleetSummary.Operation
			summary.TotalCount += fleetSummary.TotalCount
			summary.PackageID = packageID
			summary.CLSTopicID = regionOpts.CLSTopicID
			summary.Regions = append(summary.Regions, collectorPublishRegionSummary{Region: regionOpts.Region, CloudAccountID: regionOpts.CloudAccountID, PackageID: packageID, CLSLogsetID: regionOpts.CLSLogsetID, CLSTopicID: regionOpts.CLSTopicID, JobID: fleetSummary.JobID, TotalCount: fleetSummary.TotalCount})
			if fleetSummary.JobID != "" {
				jobs = append(jobs, fleetSummary.JobID)
				summary.JobIDs = append([]string(nil), jobs...)
				summary.JobID = strings.Join(summary.JobIDs, ",")
			}
		}
		return summary, nil
	}
	packageID, err := upload(opts)
	if err != nil {
		return summary, err
	}
	summary.PackageID, summary.CLSTopicID = packageID, opts.CLSTopicID
	createItems, err := buildCollectorFleetCreateItems(opts, packageID)
	if err != nil {
		return summary, err
	}
	fleetSummary, err := submitCollectorFleet(ctx, client, opts, packageID, createItems, preflightFleetNodes)
	if err != nil {
		return summary, err
	}
	summary.FleetMode = fleetSummary.FleetMode
	summary.JobID = fleetSummary.JobID
	summary.Operation = fleetSummary.Operation
	summary.TotalCount = fleetSummary.TotalCount
	summary.Regions = append(summary.Regions, collectorPublishRegionSummary{Region: opts.Region, CloudAccountID: opts.CloudAccountID, PackageID: packageID, CLSLogsetID: opts.CLSLogsetID, CLSTopicID: opts.CLSTopicID, JobID: fleetSummary.JobID, TotalCount: fleetSummary.TotalCount})
	return summary, nil
}

func ensureCollectorSpaceCloudAccounts(
	ctx context.Context,
	client *adminclient.Client,
	fetcher *setupconfig.SCFFetcherSpace,
	accounts map[string]adminclient.CloudAccount,
) error {
	if fetcher == nil {
		return nil
	}
	for _, region := range fetcher.Regions {
		if !region.Enabled || accounts[region.CloudAccountID].AccountID != "" {
			continue
		}
		if region.CloudAccountName == "" || region.CredentialSecretID == "" || region.AppID == "" || region.COSBucket == "" {
			return fmt.Errorf("Tencent cloud account %q for space %s region %s is not registered; set cloud_account_name, credential_secret_id, app_id, and cos_bucket together", region.CloudAccountID, fetcher.SpaceID, region.Region)
		}
		account, err := client.CreateCloudAccount(ctx, adminclient.CloudAccountInput{
			AccountID:          region.CloudAccountID,
			AccountName:        region.CloudAccountName,
			Provider:           "tencent",
			CredentialSecretID: region.CredentialSecretID,
			AppID:              region.AppID,
			COSRegion:          region.Region,
			COSBucket:          region.COSBucket,
		})
		if err != nil {
			return fmt.Errorf("register Tencent cloud account %q for space %s region %s: %w", region.CloudAccountID, fetcher.SpaceID, region.Region, err)
		}
		accounts[account.AccountID] = *account
	}
	return nil
}

func validateCollectorZipLogging(zipPath, topicID string) error {
	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open SCF zip: %w", err)
	}
	defer archive.Close()
	var trpcFile *zip.File
	for _, file := range archive.File {
		if file.Name != "trpc_go.yaml" {
			continue
		}
		if trpcFile != nil {
			return fmt.Errorf("SCF zip must contain exactly one trpc_go.yaml")
		}
		trpcFile = file
	}
	if trpcFile == nil {
		return fmt.Errorf("SCF zip must contain trpc_go.yaml with centralized CLS logging")
	}
	reader, err := trpcFile.Open()
	if err != nil {
		return fmt.Errorf("open SCF trpc_go.yaml: %w", err)
	}
	defer reader.Close()
	var document map[string]any
	decoder := yaml.NewDecoder(io.LimitReader(reader, 1<<20))
	if err := decoder.Decode(&document); err != nil {
		return fmt.Errorf("parse SCF trpc_go.yaml: %w", err)
	}
	plugins, _ := document["plugins"].(map[string]any)
	logs, _ := plugins["log"].(map[string]any)
	writers, _ := logs["default"].([]any)
	if len(writers) != 1 {
		return fmt.Errorf("SCF trpc_go.yaml must contain exactly one centralized CLS writer")
	}
	writer, _ := writers[0].(map[string]any)
	remote, _ := writer["remote_config"].(map[string]any)
	if writer["writer"] != "cls" || writer["level"] != "info" || strings.TrimSpace(fmt.Sprint(remote["topic_id"])) != strings.TrimSpace(topicID) {
		return fmt.Errorf("SCF trpc_go.yaml must use the resolved centralized CLS info writer")
	}
	return nil
}

func loadCollectorSCFFetcherConfig(path, spaceID string) (*setupconfig.SCFFetcherSpace, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	root := filepath.Dir(path)
	snapshot, err := setupconfig.Load(path, root)
	if err != nil {
		return nil, fmt.Errorf("load collector SCF config: %w", err)
	}
	if !snapshot.Manifest.SCFFetcher.Enabled {
		return nil, nil
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return nil, fmt.Errorf("--space-id is required to select scf_fetcher.spaces")
	}
	for index := range snapshot.Manifest.SCFFetcher.Spaces {
		space := &snapshot.Manifest.SCFFetcher.Spaces[index]
		if space.SpaceID == spaceID {
			return space, nil
		}
	}
	return nil, fmt.Errorf("scf_fetcher has no configuration for space %q", spaceID)
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

func inspectCollectorFleet(
	ctx context.Context,
	api collectorFleetAPI,
	opts collectorPublishOptions,
) ([]adminclient.CloudNode, error) {
	catalogNodes, err := api.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		CloudAccountID: opts.CloudAccountID,
		Region:         opts.Region,
		NodeType:       defaultFlag(opts.NodeType, "scf-event"),
		BizType:        defaultFlag(opts.BizType, "market_fetcher"),
	})
	if err != nil {
		return nil, err
	}
	fleetNodes, err := selectCollectorFleetNodes(
		catalogNodes,
		opts.FunctionNamePrefix,
		defaultFlag(opts.BizType, "market_fetcher"),
		opts.NodeCount,
	)
	if err != nil {
		return nil, err
	}
	return fleetNodes, nil
}

func submitCollectorFleet(
	ctx context.Context,
	api collectorFleetAPI,
	opts collectorPublishOptions,
	packageID string,
	createItems []adminclient.NodeCreateItem,
	fleetNodes []adminclient.CloudNode,
) (collectorPublishSummary, error) {
	var summary collectorPublishSummary
	if opts.NodeCount <= 0 {
		return summary, fmt.Errorf("node count must be positive")
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
	if len(fleetNodes) == 0 {
		summary.FleetMode = "created"
		resp, err := api.SubmitCreateNodes(ctx, createItems)
		if err != nil {
			return summary, err
		}
		summary.JobID = resp.JobID
		summary.Operation = "create_nodes"
		summary.TotalCount = resp.TotalCount
		return summary, nil
	}
	if len(fleetNodes) != opts.NodeCount {
		return summary, fmt.Errorf("collector fleet node slots=%d; expected %d", len(fleetNodes), opts.NodeCount)
	}

	existing := 0
	missing := make([]adminclient.NodeCreateItem, 0)
	for index, node := range fleetNodes {
		if strings.TrimSpace(node.NodeID) == "" {
			missing = append(missing, createItems[index])
			continue
		}
		existing++
	}
	if existing == 0 {
		return summary, fmt.Errorf("collector fleet has empty node slots")
	}
	if len(missing) > 0 {
		summary.FleetMode = "expanded"
		resp, err := api.SubmitCreateNodes(ctx, missing)
		if err != nil {
			return summary, err
		}
		summary.JobID = resp.JobID
		summary.Operation = "create_nodes"
		summary.TotalCount = resp.TotalCount
		return summary, nil
	}

	summary.FleetMode = "updated"
	deployments := make([]adminclient.NodeDeployItem, 0, len(fleetNodes))
	for index, node := range fleetNodes {
		deployments = append(deployments, adminclient.NodeDeployItem{
			NodeID:      node.NodeID,
			PackageID:   packageID,
			Config:      cloneCollectorStringMap(createItems[index].Config),
			Environment: cloneCollectorStringMap(createItems[index].Environment),
		})
	}
	resp, err := api.SubmitDeployNodes(ctx, deployments)
	if err != nil {
		return summary, err
	}
	summary.JobID = resp.JobID
	summary.Operation = "deploy_nodes"
	summary.TotalCount = resp.TotalCount
	return summary, nil
}

func publishCollectorFunctionStatus(ctx context.Context, opts collectorPublishStatusOptions) (*adminclient.NodeBatchChangeResponse, error) {
	if strings.TrimSpace(opts.ControlURL) == "" {
		return nil, fmt.Errorf("--control-url is required")
	}
	if strings.TrimSpace(opts.JobID) == "" {
		return nil, fmt.Errorf("--job-id is required")
	}
	client := newControlClient(
		opts.ControlURL,
		opts.AccessToken,
		opts.ServiceAccessKey,
		opts.ServiceSecretKey,
		opts.SpaceID,
	)
	jobIDs := splitJobIDs(opts.JobID)
	if len(jobIDs) == 1 {
		return client.GetNodeBatchChange(ctx, jobIDs[0])
	}
	aggregated := &adminclient.NodeBatchChangeResponse{Job: &adminclient.NodeBatchSummary{JobID: strings.Join(jobIDs, ",")}}
	for _, jobID := range jobIDs {
		status, err := client.GetNodeBatchChange(ctx, jobID)
		if err != nil {
			return nil, fmt.Errorf("get node batch %s: %w", jobID, err)
		}
		if status == nil || status.Job == nil {
			continue
		}
		aggregated.Job.TotalCount += status.Job.TotalCount
		aggregated.Job.PendingCount += status.Job.PendingCount
		aggregated.Job.RunningCount += status.Job.RunningCount
		aggregated.Job.SuccessCount += status.Job.SuccessCount
		aggregated.Job.FailedCount += status.Job.FailedCount
		aggregated.Items = append(aggregated.Items, status.Items...)
		if aggregated.Job.CreatedAt == "" || status.Job.CreatedAt < aggregated.Job.CreatedAt {
			aggregated.Job.CreatedAt = status.Job.CreatedAt
		}
		if status.Job.CompletedAt > aggregated.Job.CompletedAt {
			aggregated.Job.CompletedAt = status.Job.CompletedAt
		}
		aggregated.Job.Status = mergeBatchStatus(aggregated.Job.Status, status.Job.Status)
	}
	if aggregated.Job.TotalCount > 0 {
		aggregated.Job.ProgressPercent = (aggregated.Job.SuccessCount + aggregated.Job.FailedCount) * 100 / aggregated.Job.TotalCount
	}
	return aggregated, nil
}

func splitJobIDs(raw string) []string {
	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		if value := strings.TrimSpace(part); value != "" {
			ids = append(ids, value)
		}
	}
	return ids
}

func mergeBatchStatus(current, next string) string {
	if current == "" {
		return next
	}
	if current == next {
		return current
	}
	if strings.Contains(current, "FAILED") || strings.Contains(next, "FAILED") || strings.Contains(current, "PARTIAL") || strings.Contains(next, "PARTIAL") {
		return "NODE_BATCH_STATUS_PARTIAL"
	}
	if strings.Contains(current, "RUNNING") || strings.Contains(next, "RUNNING") {
		return "NODE_BATCH_STATUS_RUNNING"
	}
	if strings.Contains(current, "PENDING") || strings.Contains(next, "PENDING") {
		return "NODE_BATCH_STATUS_PENDING"
	}
	return "NODE_BATCH_STATUS_PARTIAL"
}

func deleteCollectorFunctions(ctx context.Context, opts collectorDeleteOptions) (*collectorDeleteSummary, error) {
	controlURL := strings.TrimSpace(opts.ControlURL)
	if controlURL == "" {
		return nil, fmt.Errorf("--control-url is required")
	}
	spaceID := strings.TrimSpace(defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID")))
	if spaceID == "" {
		return nil, fmt.Errorf("--space-id is required")
	}
	if !opts.DryRun && !opts.Confirm {
		return nil, fmt.Errorf("refusing to delete SCF nodes without --confirm (use --dry-run to inspect targets)")
	}

	client := newControlClient(controlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, spaceID)
	region := strings.TrimSpace(opts.Region)
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		NodeType: "scf-event",
		BizType:  "market_fetcher",
		Region:   region,
	})
	if err != nil {
		return nil, fmt.Errorf("list active SCF nodes: %w", err)
	}
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.IsDeleted || strings.TrimSpace(node.NodeID) == "" {
			continue
		}
		nodeIDs = append(nodeIDs, node.NodeID)
	}
	summary := &collectorDeleteSummary{
		SpaceID:    spaceID,
		Region:     region,
		NodeType:   "scf-event",
		DryRun:     opts.DryRun,
		TotalCount: len(nodeIDs),
		NodeIDs:    nodeIDs,
	}
	if opts.DryRun || len(nodeIDs) == 0 {
		if len(nodeIDs) == 0 {
			summary.Status = "nothing_to_delete"
		} else {
			summary.Status = "dry_run"
		}
		return summary, nil
	}

	resp, err := client.SubmitDeleteNodes(ctx, nodeIDs)
	if err != nil {
		return summary, fmt.Errorf("submit SCF deletion batch: %w", err)
	}
	summary.JobID = resp.JobID
	summary.Operation = resp.Operation
	summary.TotalCount = resp.TotalCount
	summary.Status = "submitted"
	if !opts.Wait {
		return summary, nil
	}

	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	for {
		change, err := client.GetNodeBatchChange(waitCtx, resp.JobID)
		if err != nil {
			return summary, fmt.Errorf("poll SCF deletion batch %s: %w", resp.JobID, err)
		}
		summary.Status = change.Job.Status
		summary.SuccessCount = change.Job.SuccessCount
		summary.FailedCount = change.Job.FailedCount
		summary.FailedResults = nil
		for _, item := range change.Items {
			if item.Status != "NODE_BATCH_ITEM_STATUS_SUCCESS" {
				summary.FailedResults = append(summary.FailedResults, item)
			}
		}
		switch change.Job.Status {
		case "NODE_BATCH_STATUS_SUCCESS":
			return summary, nil
		case "NODE_BATCH_STATUS_FAILED", "NODE_BATCH_STATUS_PARTIAL":
			return summary, fmt.Errorf("SCF deletion batch %s finished with status %s", resp.JobID, change.Job.Status)
		}
		select {
		case <-waitCtx.Done():
			return summary, fmt.Errorf("wait SCF deletion batch %s: %w", resp.JobID, waitCtx.Err())
		case <-time.After(2 * time.Second):
		}
	}
}

type collectorProbeResult struct {
	NodeID          string `json:"node_id"`
	Region          string `json:"region"`
	FunctionName    string `json:"function_name"`
	OutboundIP      string `json:"outbound_ip,omitempty"`
	OutboundIPError string `json:"outbound_ip_error,omitempty"`
	ProviderStatus  int    `json:"provider_status,omitempty"`
	LatencyMS       int64  `json:"latency_ms,omitempty"`
	CheckedAt       string `json:"checked_at"`
	Error           string `json:"error,omitempty"`
}

type collectorProbeReport struct {
	Results             []collectorProbeResult `json:"results"`
	DistinctOutboundIPs []string               `json:"distinct_outbound_ips"`
}

func probeCollectorEgress(ctx context.Context, opts collectorProbeOptions) (*collectorProbeReport, error) {
	controlURL := strings.TrimSpace(opts.ControlURL)
	if controlURL == "" {
		return nil, fmt.Errorf("--control-url is required")
	}
	spaceID := defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID"))
	if spaceID == "" {
		return nil, fmt.Errorf("--space-id is required")
	}
	client := newControlClient(controlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, spaceID)
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{Region: opts.Region, NodeType: "scf-event", BizType: "market_fetcher"})
	if err != nil {
		return nil, err
	}
	eligible := make([]adminclient.CloudNode, 0, len(nodes))
	for _, node := range nodes {
		if !collectorProbeNodeEligible(node) {
			continue
		}
		eligible = append(eligible, node)
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no active market_fetcher SCF nodes are available for egress probe")
	}
	report := &collectorProbeReport{Results: make([]collectorProbeResult, len(eligible))}
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	var failed atomic.Int32
	for index, node := range eligible {
		index, node := index, node
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				report.Results[index] = collectorProbeResult{NodeID: node.NodeID, Region: node.Region, FunctionName: node.FunctionName, CheckedAt: time.Now().UTC().Format(time.RFC3339Nano), Error: ctx.Err().Error()}
				failed.Add(1)
				return
			}
			defer func() { <-sem }()
			started := time.Now()
			result := collectorProbeResult{NodeID: node.NodeID, Region: node.Region, FunctionName: node.FunctionName, CheckedAt: started.UTC().Format(time.RFC3339Nano)}
			response, invokeErr := client.InvokeFunction(ctx, node.NodeID, map[string]any{"action": "egress_probe", "data": map[string]any{"provider": "binance", "market_type": "spot", "node_id": node.NodeID}})
			result.LatencyMS = time.Since(started).Milliseconds()
			if invokeErr != nil {
				result.Error = invokeErr.Error()
				failed.Add(1)
			} else if data, ok := egressProbeResponseData(response); ok {
				if details, ok := data["details"].(map[string]any); ok {
					result.OutboundIP, _ = details["public_ip"].(string)
					result.OutboundIPError, _ = details["public_ip_error"].(string)
				}
				result.ProviderStatus = http.StatusOK
			} else {
				rawResponse, _ := json.Marshal(response)
				result.Error = fmt.Sprintf("SCF returned no egress probe payload: %s", rawResponse)
				failed.Add(1)
			}
			report.Results[index] = result
		}()
	}
	wg.Wait()
	ipSet := make(map[string]struct{})
	for _, result := range report.Results {
		if ip := strings.TrimSpace(result.OutboundIP); ip != "" {
			ipSet[ip] = struct{}{}
		}
	}
	for ip := range ipSet {
		report.DistinctOutboundIPs = append(report.DistinctOutboundIPs, ip)
	}
	sort.Strings(report.DistinctOutboundIPs)
	if failed.Load() > 0 {
		return report, fmt.Errorf("%d SCF egress probe(s) failed", failed.Load())
	}
	return report, nil
}

func collectorProbeNodeEligible(node adminclient.CloudNode) bool {
	if node.IsDeleted || strings.TrimSpace(node.NodeID) == "" || strings.TrimSpace(node.PackageID) == "" {
		return false
	}
	if ready, ok := node.Metadata["deployment_ready"].(bool); ok {
		return ready
	}
	switch status := strings.ToLower(strings.TrimSpace(fmt.Sprint(node.Status))); status {
	case "2", "online", "active", "running", "ready", "node_status_online", "cloud_node_status_online":
		return true
	default:
		return false
	}
}

// egressProbeResponseData accepts both the normal JSON Struct response and
// the fallback raw string emitted by CloudNode when Tencent returns a JSON
// string rather than an object. Keeping this normalization here makes the
// deployment probe useful without exposing provider-specific response shapes.
func egressProbeResponseData(response map[string]any) (map[string]any, bool) {
	if response == nil {
		return nil, false
	}
	if data, ok := response["data"].(map[string]any); ok {
		return data, true
	}
	if raw, ok := response["raw"].(string); ok {
		var decoded map[string]any
		if json.Unmarshal([]byte(raw), &decoded) == nil {
			if data, ok := decoded["data"].(map[string]any); ok {
				return data, true
			}
		}
	}
	return nil, false
}

func collectorCLSCredentials() (string, string) {
	return strings.TrimSpace(os.Getenv("MOOX_CLS_SECRET_ID")), strings.TrimSpace(os.Getenv("MOOX_CLS_SECRET_KEY"))
}

func buildCollectorCreateNodeItem(opts collectorPublishOptions, packageID string) (adminclient.NodeCreateItem, error) {
	packageName := defaultFlag(opts.PackageName, "moox-collector")
	bizType := defaultFlag(opts.BizType, "market_fetcher")
	if len(opts.JobTypes) > 0 {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher does not consume CloudNode JobItem workloads")
	}
	environment, err := collectorFunctionEnvironment(opts, packageID)
	if err != nil {
		return adminclient.NodeCreateItem{}, err
	}
	config := parseCollectorOverrides(opts.Config)
	if config == nil {
		config = make(map[string]string)
	}
	fetcher := opts.FetcherConfig
	if fetcher == nil {
		fetcher = &setupconfig.SCFFetcherSpace{MemorySize: 64, TimeoutSeconds: 15, RealtimeBatchSize: 64, RealtimeBarLimit: 3, CatchupBatchSize: 1, CatchupBarLimit: 1000, MaxInflightRequests: 16, RequestTimeoutMS: 1500, HTTPMaxAttempts: 4, StorageMaxAttempts: 1, StorageTimeoutMS: 5000, MaxRetryAttempts: 3}
	}
	if strings.TrimSpace(config["timeout"]) == "" {
		config["timeout"] = strconv.Itoa(defaultInt(fetcher.TimeoutSeconds, 15))
	}
	if strings.TrimSpace(config["memory_size"]) == "" {
		config["memory_size"] = strconv.Itoa(defaultInt(fetcher.MemorySize, 64))
	}
	if strings.TrimSpace(config["storage_timeout_ms"]) == "" {
		config["storage_timeout_ms"] = strconv.Itoa(defaultInt(fetcher.StorageTimeoutMS, 5000))
	}
	if collectorConfigInt(config, "memory_size", 64) != 64 {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher memory_size is fixed at 64MB")
	}
	if collectorConfigInt(config, "timeout", 15) != 15 {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher timeout is fixed at 15 seconds")
	}
	if err := validateCollectorRuntimeConfig(config, fetcher); err != nil {
		return adminclient.NodeCreateItem{}, err
	}
	for configKey, environmentKey := range map[string]string{
		"max_inflight_requests": "MOOX_FETCH_MAX_INFLIGHT_REQUESTS",
		"request_timeout_ms":    "MOOX_FETCH_REQUEST_TIMEOUT_MS",
		"http_max_attempts":     "MOOX_FETCH_HTTP_MAX_ATTEMPTS",
		"storage_max_attempts":  "MOOX_FETCH_STORAGE_MAX_ATTEMPTS",
		"realtime_batch_size":   "MOOX_FETCH_REALTIME_BATCH_SIZE",
		"realtime_bar_limit":    "MOOX_FETCH_REALTIME_BAR_LIMIT",
		"catchup_batch_size":    "MOOX_FETCH_CATCHUP_BATCH_SIZE",
		"catchup_bar_limit":     "MOOX_FETCH_CATCHUP_BAR_LIMIT",
		"storage_timeout_ms":    "MOOX_FETCH_STORAGE_TIMEOUT_MS",
		"max_retry_attempts":    "MOOX_FETCH_MAX_RETRY_ATTEMPTS",
	} {
		if value := strings.TrimSpace(config[configKey]); value != "" {
			environment[environmentKey] = value
		}
	}
	effectiveInt := func(key string, fallback int) int {
		return collectorConfigInt(config, key, fallback)
	}
	return adminclient.NodeCreateItem{
		CloudAccountID: opts.CloudAccountID,
		NodeType:       defaultFlag(opts.NodeType, "scf-event"),
		Runtime:        defaultFlag(opts.Runtime, "Go1"),
		Namespace:      defaultFlag(opts.Namespace, "default"),
		Handler:        defaultFlag(opts.Handler, "main"),
		Config:         config,
		Environment:    environment,
		Region:         opts.Region,
		PackageID:      packageID,
		Metadata: map[string]any{
			"function_name_prefix":  packageName,
			"biz_type":              bizType,
			"supported_workloads":   []string{},
			"memory_size":           effectiveInt("memory_size", defaultInt(fetcher.MemorySize, 64)),
			"timeout_seconds":       effectiveInt("timeout", defaultInt(fetcher.TimeoutSeconds, 15)),
			"max_inflight_requests": effectiveInt("max_inflight_requests", defaultInt(fetcher.MaxInflightRequests, 5)),
			"realtime_batch_size":   effectiveInt("realtime_batch_size", defaultInt(fetcher.RealtimeBatchSize, 10)),
			"realtime_bar_limit":    effectiveInt("realtime_bar_limit", defaultInt(fetcher.RealtimeBarLimit, 3)),
			"request_timeout_ms":    effectiveInt("request_timeout_ms", defaultInt(fetcher.RequestTimeoutMS, 2000)),
			"http_max_attempts":     effectiveInt("http_max_attempts", defaultInt(fetcher.HTTPMaxAttempts, 4)),
			"storage_max_attempts":  effectiveInt("storage_max_attempts", defaultInt(fetcher.StorageMaxAttempts, 1)),
			"storage_timeout_ms":    effectiveInt("storage_timeout_ms", defaultInt(fetcher.StorageTimeoutMS, 5000)),
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
		"MOOX_SERVICE_GATEWAY_CA_PEM_B64",
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

func selectCollectorFleetNodes(nodes []adminclient.CloudNode, prefix string, bizType string, expected int) ([]adminclient.CloudNode, error) {
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
		if node.BizType != bizType {
			return nil, fmt.Errorf(
				"fleet prefix %q node %q has biz_type %q; expected %q",
				prefix,
				node.NodeID,
				node.BizType,
				bizType,
			)
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
	if len(opts.JobTypes) > 0 {
		return nil, fmt.Errorf("market_fetcher does not consume CloudNode JobItem workloads")
	}
	packageID := ""
	if len(packageIDs) > 0 {
		packageID = packageIDs[0]
	}
	env := map[string]string{}
	setDefaultEnv(env, "MOOX_SPACE_ID", defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID")))
	fetcher := opts.FetcherConfig
	if fetcher == nil {
		fetcher = &setupconfig.SCFFetcherSpace{TimeoutSeconds: 15, MaxInflightRequests: 16, RequestTimeoutMS: 1500, HTTPMaxAttempts: 4, StorageMaxAttempts: 1, StorageTimeoutMS: 5000, RealtimeBatchSize: 64, RealtimeBarLimit: 3, CatchupBatchSize: 1, CatchupBarLimit: 1000, MaxRetryAttempts: 3}
	}
	setDefaultEnv(env, "MOOX_FETCH_TIMEOUT_SECONDS", strconv.Itoa(defaultInt(fetcher.TimeoutSeconds, 15)))
	setDefaultEnv(env, "MOOX_FETCH_MAX_INFLIGHT_REQUESTS", strconv.Itoa(defaultInt(fetcher.MaxInflightRequests, 5)))
	setDefaultEnv(env, "MOOX_FETCH_REQUEST_TIMEOUT_MS", strconv.Itoa(defaultInt(fetcher.RequestTimeoutMS, 2000)))
	setDefaultEnv(env, "MOOX_FETCH_HTTP_MAX_ATTEMPTS", strconv.Itoa(defaultInt(fetcher.HTTPMaxAttempts, 4)))
	setDefaultEnv(env, "MOOX_FETCH_STORAGE_MAX_ATTEMPTS", strconv.Itoa(defaultInt(fetcher.StorageMaxAttempts, 1)))
	setDefaultEnv(env, "MOOX_FETCH_REALTIME_BATCH_SIZE", strconv.Itoa(defaultInt(fetcher.RealtimeBatchSize, 10)))
	setDefaultEnv(env, "MOOX_FETCH_REALTIME_BAR_LIMIT", strconv.Itoa(defaultInt(fetcher.RealtimeBarLimit, 3)))
	setDefaultEnv(env, "MOOX_FETCH_CATCHUP_BATCH_SIZE", strconv.Itoa(defaultInt(fetcher.CatchupBatchSize, 1)))
	setDefaultEnv(env, "MOOX_FETCH_CATCHUP_BAR_LIMIT", strconv.Itoa(defaultInt(fetcher.CatchupBarLimit, 1000)))
	setDefaultEnv(env, "MOOX_FETCH_STORAGE_TIMEOUT_MS", strconv.Itoa(defaultInt(fetcher.StorageTimeoutMS, 5000)))
	setDefaultEnv(env, "MOOX_FETCH_MAX_RETRY_ATTEMPTS", strconv.Itoa(defaultInt(fetcher.MaxRetryAttempts, 3)))
	gatewayNodeID := firstNonEmpty(os.Getenv("MOOX_GATEWAY_NODE_ID"), os.Getenv("MOOX_GATEWAY_TARGET_NODE"))
	setDefaultEnv(env, "MOOX_GATEWAY_NODE_ID", gatewayNodeID)
	setDefaultEnv(env, "MOOX_GATEWAY_TARGET_NODE", gatewayNodeID)
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_KEY_ID", os.Getenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID"))
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_SECRET_KEY", os.Getenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY"))
	defaultCLSSecretID, defaultCLSSecretKey := collectorCLSCredentials()
	setDefaultEnv(env, "MOOX_CLS_HOST", firstNonEmpty(opts.CLSHost, os.Getenv("MOOX_CLS_HOST"), clsprepare.Host))
	setDefaultEnv(env, "MOOX_CLS_SECRET_ID", firstNonEmpty(opts.CLSSecretID, defaultCLSSecretID))
	setDefaultEnv(env, "MOOX_CLS_SECRET_KEY", firstNonEmpty(opts.CLSSecretKey, defaultCLSSecretKey))
	// The native Storage gateway verifies the caller together with the key ID.
	// The SCF uses the Collector service credential, so its caller must match
	// that credential's ACL rather than the CLI process's caller identity.
	setDefaultEnv(env, "MOOX_GATEWAY_CALLER", "collector")
	overrides := parseCollectorOverrides(opts.Env)
	managed := map[string]struct{}{
		"MOOX_EVENTBUS_NATS_URL":            {},
		"MOOX_EVENTBUS_NATS_USERNAME":       {},
		"MOOX_EVENTBUS_NATS_PASSWORD":       {},
		"MOOX_EVENTBUS_NATS_TLS_CA_PEM_B64": {},
		"MOOX_CODE_PACKAGE_ID":              {},
		"MOOX_GATEWAY_SERVICE_KEY_ID":       {},
		"MOOX_GATEWAY_SERVICE_SECRET_KEY":   {},
		"MOOX_GATEWAY_CALLER":               {},
		"MOOX_GATEWAY_NODE_ID":              {},
		"MOOX_GATEWAY_TARGET_NODE":          {},
		"MOOX_SERVICE_GATEWAY_CA_PEM_B64":   {},
		"MOOX_SPACE_ID":                     {},
		"MOOX_FETCH_TIMEOUT_SECONDS":        {},
		"MOOX_FETCH_MAX_INFLIGHT_REQUESTS":  {},
		"MOOX_FETCH_REQUEST_TIMEOUT_MS":     {},
		"MOOX_FETCH_HTTP_MAX_ATTEMPTS":      {},
		"MOOX_FETCH_STORAGE_MAX_ATTEMPTS":   {},
		"MOOX_FETCH_REALTIME_BATCH_SIZE":    {},
		"MOOX_FETCH_REALTIME_BAR_LIMIT":     {},
		"MOOX_FETCH_CATCHUP_BATCH_SIZE":     {},
		"MOOX_FETCH_CATCHUP_BAR_LIMIT":      {},
		"MOOX_FETCH_STORAGE_TIMEOUT_MS":     {},
		"MOOX_FETCH_MAX_RETRY_ATTEMPTS":     {},
		"MOOX_CLS_SECRET_ID":                {},
		"MOOX_CLS_SECRET_KEY":               {},
		"MOOX_CLS_HOST":                     {},
		"TENCENTCLOUD_SECRET_ID":            {},
		"TENCENTCLOUD_SECRET_KEY":           {},
		"TENCENT_SECRET_ID":                 {},
		"TENCENT_SECRET_KEY":                {},
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
			return nil, fmt.Errorf("market-fetch-publisher credential must contain exactly one EventBus URL")
		}
		eventBusURL, err := url.Parse(credential.URLs[0])
		if err != nil || eventBusURL.Scheme != "tls" || eventBusURL.Hostname() == "" || eventBusURL.Port() == "" {
			return nil, fmt.Errorf("market-fetch-publisher credential URL must be tls with host and port")
		}
		if host := eventBusURL.Hostname(); host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback() {
			return nil, fmt.Errorf("SCF EventBus URL must not use a loopback host")
		}
		caPath := credential.CAFile
		if caPath == "" {
			return nil, fmt.Errorf("market-fetch-publisher credential requires ca_file")
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
	if strings.TrimSpace(overrides["MOOX_SERVICE_GATEWAY_CA_FILE"]) != "" {
		return nil, fmt.Errorf("serverless environment must not contain MOOX_SERVICE_GATEWAY_CA_FILE")
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
	serviceCAFile := strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_CA_FILE"))
	serviceCAMaterial := strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_CA_PEM_B64"))
	if serviceCAFile != "" && serviceCAMaterial != "" {
		return nil, fmt.Errorf("service gateway CA file and CA PEM material are mutually exclusive")
	}
	if serviceCAFile != "" {
		pem, err := os.ReadFile(serviceCAFile)
		if err != nil {
			return nil, fmt.Errorf("read service gateway CA file: %w", err)
		}
		serviceCAMaterial = base64.StdEncoding.EncodeToString(pem)
	}
	if serviceCAMaterial != "" {
		if _, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{CAPEMBase64: serviceCAMaterial}); err != nil {
			return nil, fmt.Errorf("invalid service gateway CA material: %w", err)
		}
		env["MOOX_SERVICE_GATEWAY_CA_PEM_B64"] = serviceCAMaterial
	}
	for key, value := range overrides {
		if key == "MOOX_GATEWAY_CA_FILE" || key == "MOOX_GATEWAY_CA_PEM_B64" ||
			key == "MOOX_SERVICE_GATEWAY_CA_FILE" || key == "MOOX_SERVICE_GATEWAY_CA_PEM_B64" ||
			key == "MOOX_EVENTBUS_NATS_TLS_CA_FILE" {
			continue
		}
		env[key] = value
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

func defaultInt(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func collectorConfigInt(values map[string]string, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(values[key]))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

// validateCollectorRuntimeConfig keeps command-line function-config overrides
// inside the same short-lived execution budget as custom.toml. This is needed
// because those overrides are copied into SCF environment variables directly.
func validateCollectorRuntimeConfig(values map[string]string, fetcher *setupconfig.SCFFetcherSpace) error {
	if fetcher == nil {
		return fmt.Errorf("scf fetcher configuration is required")
	}
	batchSize, err := collectorRuntimeConfigInt(values, "realtime_batch_size", defaultInt(fetcher.RealtimeBatchSize, 64), 1)
	if err != nil {
		return err
	}
	if batchSize < 1 || batchSize > 64 {
		return fmt.Errorf("market_fetcher realtime_batch_size must be between 1 and 64")
	}
	inflight, err := collectorRuntimeConfigInt(values, "max_inflight_requests", defaultInt(fetcher.MaxInflightRequests, 16), 1)
	if err != nil {
		return err
	}
	if inflight < 1 || inflight > 64 {
		return fmt.Errorf("market_fetcher max_inflight_requests must be between 1 and 64")
	}
	requestTimeoutMS, err := collectorRuntimeConfigInt(values, "request_timeout_ms", defaultInt(fetcher.RequestTimeoutMS, 1500), 1)
	if err != nil {
		return err
	}
	storageTimeoutMS, err := collectorRuntimeConfigInt(values, "storage_timeout_ms", defaultInt(fetcher.StorageTimeoutMS, 5000), 1)
	if err != nil {
		return err
	}
	requestWaves := (batchSize + inflight - 1) / inflight
	if storageTimeoutMS != 5000 {
		return fmt.Errorf("market_fetcher storage_timeout_ms is fixed at 5000")
	}
	if requestWaves*requestTimeoutMS+storageTimeoutMS+500 >= 15_000 {
		return fmt.Errorf("market_fetcher realtime request waves + storage_timeout_ms + publish reserve must be less than the 15-second timeout")
	}
	return nil
}

func collectorRuntimeConfigInt(values map[string]string, key string, fallback, minimum int) (int, error) {
	raw, ok := values[key]
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < minimum {
		return 0, fmt.Errorf("market_fetcher %s must be an integer >= %d", key, minimum)
	}
	return value, nil
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
		BizType:          defaultFlag(opts.BizType, "market_fetcher"),
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

	deployResp, err := client.SubmitDeployNodes(ctx, []adminclient.NodeDeployItem{{
		NodeID:    opts.NodeID,
		PackageID: uploadResp.PackageID,
	}})
	if err != nil {
		return summary, err
	}
	summary.JobID = deployResp.JobID
	summary.Operation = "deploy_nodes"
	summary.TotalCount = deployResp.TotalCount
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
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", fmt.Sprintf("-s -w -X main.Version=%s", version), "-o", outPath, "./cmd/scf")
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
