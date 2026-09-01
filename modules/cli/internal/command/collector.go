package command

import (
	"archive/zip"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/packages/cloudprovider/tencent"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/jetstream"
	metricsreport "github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"
)

const (
	defaultCollectorSCFTimeout = "15"
	// Manifest deployments may spread a stock_cn fleet across multiple regions;
	// keep the ad-hoc guard above one normal 200-function release while the
	// per-region platform limit remains enforced by setup validation.
	maxCollectorPublishNodeCount    = 1000
	collectorRuntimeConfigBatchSize = 100
)

type collectorRuntimeConfigPatch struct {
	NodeID             string            `json:"node_id"`
	ManagedEnvironment map[string]string `json:"managed_environment,omitempty"`
	TimerEnabled       bool              `json:"timer_enabled"`
	TimerCron          string            `json:"timer_cron"`
}

type collectorPublishedTimerFleet struct {
	opts  collectorPublishOptions
	nodes []adminclient.CloudNode
}

type collectorPackageOptions struct {
	CollectorRoot            string
	SpaceID                  string
	PackageConfigDir         string
	Version                  string
	Out                      string
	ConfigDir                string
	Entrypoint               string
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
	ServiceAccessKey        string
	ServiceSecretKey        string
	CloudAccountID          string
	Namespace               string
	Runtime                 string
	Handler                 string
	Region                  string
	ZipPath                 string
	PackageName             string
	PackageType             string
	BizType                 string
	NodeType                string
	TriggerType             string
	InstrumentSnapshotTimer bool
	EnableStockCN           bool
	StorageRPCGatewayTarget string
	JobTypes                []string
	Env                     []string
	Config                  []string
	EventBusCredentialFile  string
	NodeCount               int
	FunctionNamePrefix      string
	File                    string
	FetcherConfig           *setupconfig.SCFFetcherSpace
	CLSSecretID             string
	CLSSecretKey            string
	CLSHost                 string
	// In manifest mode these public materials are read from the control host
	// immediately before a fleet is published. They must not come from the
	// operator machine, which may still hold an old CA after a control-plane
	// certificate rotation.
	EventBusCredential  *jetstream.CredentialFile
	EventBusCAPEM       []byte
	GatewayCAPEM        []byte
	ServiceGatewayCAPEM []byte
}

type collectorSCFTrustMaterial struct {
	EventBusCredential       jetstream.CredentialFile
	EventBusCAPEM            []byte
	GatewayCAPEM             []byte
	ServiceGatewayCAPEM      []byte
	StoragePrimaryAuthSecret string
}

type collectorPublishStatusOptions struct {
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	SpaceID          string
	JobID            string
}

type collectorStockCNActivateOptions struct {
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	SpaceID          string
	File             string
	Version          string
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
	File             string
}

var collectorDeployFlags collectorDeployOptions
var collectorDeleteFlags collectorDeleteOptions
var collectorProbeFlags collectorProbeOptions
var collectorStockCNActivateFlags collectorStockCNActivateOptions

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

var collectorFunctionActivateStockCNCmd = &cobra.Command{
	Use:   "activate-stock-cn",
	Short: "通过正式门禁后启用 stock_cn Kline Timer 和规则",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		summary, err := activateStockCNCollection(cmd.Context(), collectorStockCNActivateFlags)
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		if encodeErr := enc.Encode(summary); encodeErr != nil && err == nil {
			return encodeErr
		}
		return err
	},
}

var collectorFunctionProbeCmd = &cobra.Command{
	Use:   "probe-egress",
	Short: "逐个调用短时 SCF 诊断公网出口和 Provider 连通性",
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
	ZipPath        string                          `json:"zip_path"`
	PackageID      string                          `json:"package_id"`
	CLSTopicID     string                          `json:"cls_topic_id"`
	FleetMode      string                          `json:"fleet_mode"`
	JobID          string                          `json:"job_id"`
	JobIDs         []string                        `json:"job_ids,omitempty"`
	Operation      string                          `json:"operation"`
	TotalCount     int                             `json:"total_count"`
	StockCNEnabled bool                            `json:"stock_cn_enabled,omitempty"`
	Regions        []collectorPublishRegionSummary `json:"regions,omitempty"`
}

type collectorPublishRegionSummary struct {
	Purpose        string `json:"purpose,omitempty"`
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
	collectorFunctionCmd.AddCommand(collectorFunctionPackageCmd, collectorFunctionPublishCmd, collectorFunctionActivateStockCNCmd, collectorFunctionDeployCmd, collectorFunctionDeleteCmd, collectorFunctionProbeCmd)
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
	probeFlags.StringVar(&collectorProbeFlags.File, "file", "", "optional custom.toml; stock_cn uses it only to report the configured Timer count")
	activateFlags := collectorFunctionActivateStockCNCmd.Flags()
	activateFlags.StringVar(&collectorStockCNActivateFlags.ControlURL, "control-url", "", "Control service base URL")
	activateFlags.StringVar(&collectorStockCNActivateFlags.AccessToken, "access-token", "", "Control access token")
	activateFlags.StringVar(&collectorStockCNActivateFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名 access key")
	activateFlags.StringVar(&collectorStockCNActivateFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名 secret key")
	activateFlags.StringVar(&collectorStockCNActivateFlags.SpaceID, "space-id", "stock_cn", "space id")
	activateFlags.StringVar(&collectorStockCNActivateFlags.File, "file", "", "custom.toml manifest")
	activateFlags.StringVar(&collectorStockCNActivateFlags.Version, "version", "", "expected deployed package version")

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
	submitFlags.StringVar(&collectorPublishFlags.TriggerType, "trigger-type", "timer", "SCF trigger type: timer or invoke")
	submitFlags.BoolVar(&collectorPublishFlags.EnableStockCN, "enable-stock-cn", false, "通过全部 stock_cn 发布门禁后启用正式 Kline Timer 和规则")
	submitFlags.StringVar(&collectorPublishFlags.StorageRPCGatewayTarget, "storage-rpc-gateway-target", "", "SCF 固定访问的 Storage tRPC 地址")
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

	entrypoint := defaultFlag(opts.Entrypoint, "crypto_market")
	if err := buildCollectorLinuxBinary(ctx, collectorRoot, binaryPath, version, entrypoint); err != nil {
		return nil, err
	}
	return collectorpackager.BuildSCFPackage(collectorpackager.BuildSCFPackageOptions{
		BinaryPath:               binaryPath,
		ConfigDir:                configDir,
		OutPath:                  outPath,
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
	SecretID  string
	SecretKey string
}

func resolveCollectorCLSSink(ctx context.Context, control *adminclient.Client, account adminclient.CloudAccount, region string) (collectorCLSSink, error) {
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
	region = strings.TrimSpace(region)
	if region == "" {
		region = clsprepare.Region
	}
	api, err := newCollectorCLSAPI(secret.KeyID, secret.SecretValue, region)
	if err != nil {
		return collectorCLSSink{}, fmt.Errorf("create central CLS client for collector package: %w", err)
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	resources, err := tencent.ResolveExistingCLS(resolveCtx, api, clsprepare.LogsetName, clsprepare.TopicName)
	if err != nil {
		return collectorCLSSink{}, fmt.Errorf("resolve central CLS topic for collector function: %w", err)
	}
	return collectorCLSSink{Resources: resources, SecretID: secret.KeyID, SecretKey: secret.SecretValue}, nil
}

func publishCollectorFunction(ctx context.Context, opts collectorPublishOptions) (collectorPublishSummary, error) {
	if opts.ControlURL == "" {
		return collectorPublishSummary{}, fmt.Errorf("--control-url is required")
	}
	if opts.CloudAccountID == "" && strings.TrimSpace(opts.File) == "" {
		return collectorPublishSummary{}, fmt.Errorf("--cloud-account-id is required")
	}
	if strings.TrimSpace(opts.TriggerType) == "" {
		opts.TriggerType = "timer"
	}
	if opts.TriggerType != "timer" && opts.TriggerType != "invoke" {
		return collectorPublishSummary{}, fmt.Errorf("--trigger-type must be timer or invoke")
	}
	fetcherConfig, manifest, err := loadCollectorSCFFetcherConfigSnapshot(opts.File, opts.SpaceID)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	if opts.Region == "" && fetcherConfig == nil {
		return collectorPublishSummary{}, fmt.Errorf("--region is required")
	}
	if opts.EnableStockCN && (fetcherConfig == nil || !strings.EqualFold(fetcherConfig.SpaceID, "stock_cn")) {
		return collectorPublishSummary{}, fmt.Errorf("--enable-stock-cn requires a stock_cn manifest deployment")
	}
	if fetcherConfig == nil && (opts.NodeCount <= 0 || opts.NodeCount > maxCollectorPublishNodeCount) {
		return collectorPublishSummary{}, fmt.Errorf(
			"--node-count must be between 1 and %d",
			maxCollectorPublishNodeCount,
		)
	}
	// A manifest deployment must build the package from the authoritative
	// control-plane trust material below. Accepting an operator-supplied zip
	// here would bypass the Primary auth, EventBus endpoint and CA preflight.
	if fetcherConfig != nil && strings.TrimSpace(opts.ZipPath) != "" {
		return collectorPublishSummary{}, fmt.Errorf("--zip is not allowed with --file manifest deployments; rebuild the package from control-plane trust material")
	}
	if err := validateCollectorPublishAuth(opts); err != nil {
		return collectorPublishSummary{}, err
	}
	if fetcherConfig != nil {
		trustMaterial, trustErr := resolveCollectorSCFTrustMaterial(ctx, manifest.Manifest.ControlHost, manifest.Manifest.Paths.Resolved().ControlRoot)
		if trustErr != nil {
			return collectorPublishSummary{}, trustErr
		}
		// The credential file on the control host is intentionally an internal
		// publisher credential and commonly contains a loopback NATS URL. SCF
		// must never receive that URL: replace only the endpoint with the
		// deployment-wide public endpoint from custom.toml while retaining the
		// control-plane username/password and CA material.
		trustMaterial.EventBusCredential, trustErr = preflightCollectorSCFEventBusCredential(
			trustMaterial.EventBusCredential,
			trustMaterial.EventBusCAPEM,
			manifest.Manifest.EventBus,
		)
		if trustErr != nil {
			return collectorPublishSummary{}, trustErr
		}
		// The SCF package signs Binance Storage requests. Always take the
		// Primary secret from the control host's authoritative auth file rather
		// than inheriting a possibly stale operator environment.
		opts.StoragePrimaryAuthSecret = trustMaterial.StoragePrimaryAuthSecret
		opts.EventBusCredential = &trustMaterial.EventBusCredential
		opts.EventBusCAPEM = trustMaterial.EventBusCAPEM
		opts.GatewayCAPEM = trustMaterial.GatewayCAPEM
		opts.ServiceGatewayCAPEM = trustMaterial.ServiceGatewayCAPEM
		opts.FetcherConfig = fetcherConfig
		opts.collectorPackageOptions.SpaceID = fetcherConfig.SpaceID
		opts.collectorPackageOptions.Entrypoint = fetcherConfig.Entrypoint
		opts.collectorPackageOptions.PackageConfigDir = fetcherConfig.PackageConfigDir
		opts.Namespace = defaultFlag(fetcherConfig.Namespace, opts.Namespace)
		opts.Runtime = defaultFlag(fetcherConfig.Runtime, opts.Runtime)
		opts.FunctionNamePrefix = defaultFlag(fetcherConfig.FunctionPrefix, opts.FunctionNamePrefix)
		opts.StorageRPCGatewayTarget = defaultFlag(fetcherConfig.StorageRPCGatewayTarget, opts.StorageRPCGatewayTarget)
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
	if fetcherConfig != nil && strings.EqualFold(fetcherConfig.SpaceID, "stock_cn") {
		enabledRules, ruleErr := client.ListEnabledTaskRules(ctx, fetcherConfig.SpaceID, "equity")
		if ruleErr != nil {
			return collectorPublishSummary{}, fmt.Errorf("stock_cn publish requires a rule readback before side effects: %w", ruleErr)
		}
		if len(enabledRules) > 0 {
			ids := make([]string, 0, len(enabledRules))
			for _, rule := range enabledRules {
				ids = append(ids, rule.RuleID)
			}
			return collectorPublishSummary{}, fmt.Errorf("stock_cn publish requires all equity collector rules disabled; enabled rules: %s", strings.Join(ids, ","))
		}
	}
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
	clsRegion := clsprepare.Region
	if manifest != nil {
		clsRegion = firstNonEmpty(manifest.Manifest.TencentCloud.Region, clsRegion)
	}
	clsSink, err := resolveCollectorCLSSink(ctx, client, clsAccount, clsRegion)
	if err != nil {
		return collectorPublishSummary{}, err
	}
	opts.CLSLogsetID = clsSink.Resources.LogsetID
	opts.CLSTopicID = clsSink.Resources.TopicID
	opts.CLSSecretID, opts.CLSSecretKey = collectorCLSCredentials()
	if opts.CLSSecretID == "" || opts.CLSSecretKey == "" {
		// The selected CloudAccount credential is already required to resolve
		// the central CLS resources. Reuse it for SCF CLS ingestion when no
		// dedicated runtime identity was supplied, without reading secrets from
		// the operator environment or committing them to the package.
		opts.CLSSecretID, opts.CLSSecretKey = clsSink.SecretID, clsSink.SecretKey
	}
	if opts.CLSSecretID == "" || opts.CLSSecretKey == "" {
		return collectorPublishSummary{}, fmt.Errorf("MOOX_CLS_SECRET_ID and MOOX_CLS_SECRET_KEY are required for SCF centralized logging")
	}
	// The shared Topic is in Guangzhou but the short-lived functions run in
	// overseas regions. They must use CLS's public ingestion endpoint rather
	// than the Guangzhou VPC-only tencentyun.com address.
	opts.CLSHost = scfCLSIngestHost(clsRegion + ".cls.tencentyun.com")
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
		var publishedTimerFleets []collectorPublishedTimerFleet
		var instrumentSnapshotFleet collectorPublishedTimerFleet
		var hasInstrumentSnapshotFleet bool
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
			// The auxiliary Invoke node is deployed and exercised first. A
			// successful market_fetch canary proves the Kline SCF -> Storage path
			// before any regional Timer fleet is changed. The full-market Instrument
			// snapshot is deployed as a separate daily Timer fleet below.
			invokeOpts := regionOpts
			invokeOpts.TriggerType = "invoke"
			invokeOpts.NodeCount = 1
			invokeOpts.FunctionNamePrefix = strings.TrimSuffix(regionOpts.FunctionNamePrefix, "-") + "-invoke"
			invokeNodes, inspectInvokeErr := inspectCollectorFleet(ctx, client, invokeOpts)
			if inspectInvokeErr != nil {
				return summary, inspectInvokeErr
			}
			invokeItems, buildInvokeErr := buildCollectorFleetCreateItems(invokeOpts, packageID)
			if buildInvokeErr != nil {
				return summary, buildInvokeErr
			}
			invokeSummary, submitInvokeErr := submitCollectorFleet(ctx, client, invokeOpts, packageID, invokeItems, invokeNodes)
			if submitInvokeErr != nil {
				return summary, submitInvokeErr
			}
			if err := waitCollectorBatch(ctx, client, invokeSummary.JobID); err != nil {
				return summary, fmt.Errorf("SCF invoke canary fleet for region %s: %w", regionOpts.Region, err)
			}
			invokeNodes, inspectInvokeErr = inspectCollectorFleet(ctx, client, invokeOpts)
			if inspectInvokeErr != nil || len(invokeNodes) != 1 {
				if inspectInvokeErr != nil {
					return summary, inspectInvokeErr
				}
				return summary, fmt.Errorf("SCF invoke canary fleet for region %s has no ready node", regionOpts.Region)
			}
			if err := runCollectorSCFCanary(ctx, client, invokeOpts, invokeNodes[0].NodeID); err != nil {
				return summary, fmt.Errorf("SCF canary for region %s: %w", regionOpts.Region, err)
			}
			// The stock instrument snapshot is published as its own daily Timer
			// below. The regional Invoke node is only the Kline canary path.
			summary.TotalCount += invokeSummary.TotalCount
			if invokeSummary.JobID != "" {
				jobs = append(jobs, invokeSummary.JobID)
			}

			fleetNodes, inspectErr := inspectCollectorFleet(ctx, client, regionOpts)
			if inspectErr != nil {
				return summary, inspectErr
			}
			if strings.EqualFold(fetcherConfig.SpaceID, "stock_cn") {
				disableJobs, disableErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorTimerDisablePatches(fleetNodes))
				if disableErr != nil {
					return summary, fmt.Errorf("disable existing Timer fleet before deploy: %w", disableErr)
				}
				if err := waitCollectorBatches(ctx, client, disableJobs); err != nil {
					return summary, fmt.Errorf("disable existing Timer fleet before deploy: %w", err)
				}
				jobs = append(jobs, disableJobs...)
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
			if err := waitCollectorBatch(ctx, client, fleetSummary.JobID); err != nil {
				return summary, fmt.Errorf("SCF Timer fleet for region %s: %w", regionOpts.Region, err)
			}
			if strings.EqualFold(fetcherConfig.SpaceID, "stock_cn") {
				deployedNodes, inspectDeployedErr := inspectCollectorFleet(ctx, client, regionOpts)
				if inspectDeployedErr != nil {
					return summary, inspectDeployedErr
				}
				publishedTimerFleets = append(publishedTimerFleets, collectorPublishedTimerFleet{opts: regionOpts, nodes: deployedNodes})
			}
			summary.FleetMode = fleetSummary.FleetMode
			summary.Operation = fleetSummary.Operation
			summary.TotalCount += fleetSummary.TotalCount
			summary.PackageID = packageID
			summary.CLSTopicID = regionOpts.CLSTopicID
			summary.Regions = append(summary.Regions, collectorPublishRegionSummary{Region: regionOpts.Region, CloudAccountID: regionOpts.CloudAccountID, PackageID: packageID, CLSLogsetID: regionOpts.CLSLogsetID, CLSTopicID: regionOpts.CLSTopicID, JobID: fleetSummary.JobID, TotalCount: fleetSummary.TotalCount})
			if fleetSummary.JobID != "" {
				jobs = append(jobs, fleetSummary.JobID)
			}
			summary.JobIDs = append([]string(nil), jobs...)
			summary.JobID = strings.Join(summary.JobIDs, ",")
		}
		if strings.EqualFold(fetcherConfig.SpaceID, "stock_cn") {
			instrumentOpts := opts
			instrumentOpts.Region = fetcherConfig.InstrumentSnapshotRegion
			instrumentOpts.NodeCount = 1
			instrumentOpts.CloudAccountID = fetcherConfig.InstrumentSnapshotCloudAccountID
			instrumentOpts.FunctionNamePrefix = fetcherConfig.InstrumentSnapshotFunctionPrefix
			instrumentOpts.TriggerType = "timer"
			instrumentOpts.InstrumentSnapshotTimer = true
			instrumentPackageID, uploadErr := upload(instrumentOpts)
			if uploadErr != nil {
				return summary, fmt.Errorf("upload stock instrument snapshot package: %w", uploadErr)
			}
			instrumentNodes, inspectErr := inspectCollectorFleet(ctx, client, instrumentOpts)
			if inspectErr != nil {
				return summary, fmt.Errorf("inspect stock instrument snapshot fleet: %w", inspectErr)
			}
			if len(instrumentNodes) > 0 {
				disableJobs, disableErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorTimerDisablePatches(instrumentNodes))
				if disableErr != nil {
					return summary, fmt.Errorf("disable existing stock instrument snapshot Timer: %w", disableErr)
				}
				if err := waitCollectorBatches(ctx, client, disableJobs); err != nil {
					return summary, fmt.Errorf("disable existing stock instrument snapshot Timer: %w", err)
				}
				jobs = append(jobs, disableJobs...)
			}
			instrumentItems, buildErr := buildCollectorFleetCreateItems(instrumentOpts, instrumentPackageID)
			if buildErr != nil {
				return summary, fmt.Errorf("build stock instrument snapshot fleet: %w", buildErr)
			}
			instrumentSummary, submitErr := submitCollectorFleet(ctx, client, instrumentOpts, instrumentPackageID, instrumentItems, instrumentNodes)
			if submitErr != nil {
				return summary, fmt.Errorf("submit stock instrument snapshot fleet: %w", submitErr)
			}
			if err := waitCollectorBatch(ctx, client, instrumentSummary.JobID); err != nil {
				return summary, fmt.Errorf("stock instrument snapshot fleet: %w", err)
			}
			instrumentNodes, inspectErr = inspectCollectorFleet(ctx, client, instrumentOpts)
			if inspectErr != nil || len(instrumentNodes) != 1 {
				if inspectErr != nil {
					return summary, fmt.Errorf("inspect stock instrument snapshot fleet after deploy: %w", inspectErr)
				}
				return summary, fmt.Errorf("stock instrument snapshot fleet has no ready node")
			}
			if err := runCollectorInstrumentCanaryWithExtendedControlTimeout(ctx, client, instrumentOpts, instrumentNodes[0].NodeID); err != nil {
				return summary, fmt.Errorf("SCF instrument snapshot canary for region %s: %w", instrumentOpts.Region, err)
			}
			instrumentTimerJob, cronErr := submitCollectorTimerRuntimeConfigs(ctx, client, []collectorRuntimeConfigPatch{{
				NodeID: instrumentNodes[0].NodeID, TimerCron: fetcherConfig.InstrumentSnapshotTimerCron, TimerEnabled: false,
			}})
			if cronErr != nil {
				return summary, fmt.Errorf("configure daily stock instrument snapshot Timer: %w", cronErr)
			}
			if err := waitCollectorBatches(ctx, client, instrumentTimerJob); err != nil {
				return summary, fmt.Errorf("configure daily stock instrument snapshot Timer: %w", err)
			}
			jobs = append(jobs, instrumentSummary.JobID)
			jobs = append(jobs, instrumentTimerJob...)
			summary.Regions = append(summary.Regions, collectorPublishRegionSummary{Purpose: "instrument_snapshot_daily", Region: instrumentOpts.Region, CloudAccountID: instrumentOpts.CloudAccountID, PackageID: instrumentPackageID, CLSLogsetID: instrumentOpts.CLSLogsetID, CLSTopicID: instrumentOpts.CLSTopicID, JobID: instrumentSummary.JobID, TotalCount: instrumentSummary.TotalCount})
			summary.TotalCount += instrumentSummary.TotalCount
			instrumentSnapshotFleet = collectorPublishedTimerFleet{opts: instrumentOpts, nodes: instrumentNodes}
			hasInstrumentSnapshotFleet = true
			// Keep both Timer classes disabled until the fleet is fully deployed and
			// the independent Instrument canary has passed. Egress probing is an
			// optional diagnostic, not a release blocker.
			allTimerNodes := make([]adminclient.CloudNode, 0, fetcherConfig.TimerFunctionCount)
			for _, fleet := range publishedTimerFleets {
				allTimerNodes = append(allTimerNodes, fleet.nodes...)
			}
			if opts.EnableStockCN {
				// Enable the rule before the Timer fleet. Collector assignment is
				// reconciled from the active rule, so waiting for assignments while
				// the rule is still disabled can never become ready.
				if err := ensureStockCNKlineRule(ctx, client, fetcherConfig.SpaceID); err != nil {
					disableJobs, disableErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorTimerDisablePatches(allTimerNodes))
					if disableErr == nil {
						disableErr = waitCollectorBatches(ctx, client, disableJobs)
					}
					if disableErr != nil {
						return summary, fmt.Errorf("enable stock Kline rule: %w; rollback Timer fleet failed: %v", err, disableErr)
					}
					return summary, fmt.Errorf("enable stock Kline rule: %w", err)
				}
				enabledRules, ruleErr := client.ListEnabledTaskRules(ctx, fetcherConfig.SpaceID, "equity")
				if ruleErr != nil {
					return summary, fmt.Errorf("verify stock Kline rule enabled: %w", ruleErr)
				}
				found := false
				for _, rule := range enabledRules {
					if rule.RuleID == "builtin-stock-cn-kline-1m" && rule.Enabled {
						found = true
						break
					}
				}
				if !found {
					return summary, fmt.Errorf("stock Kline rule enable was not confirmed by control-plane readback")
				}
				if err := waitCollectorTimerFleetsAssigned(ctx, client, publishedTimerFleets); err != nil {
					_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, "builtin-stock-cn-kline-1m")
					return summary, fmt.Errorf("verify stock Kline assignments after rule enable: %w", err)
				}
				enablePatches := collectorStockCNTimerRestorePatches(allTimerNodes, *fetcherConfig)
				enableJobs, enableErr := submitCollectorTimerRuntimeConfigs(ctx, client, enablePatches)
				if enableErr != nil {
					_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, "builtin-stock-cn-kline-1m")
					return summary, fmt.Errorf("enable stock Kline Timer fleet: %w", enableErr)
				}
				if err := waitCollectorBatches(ctx, client, enableJobs); err != nil {
					_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, "builtin-stock-cn-kline-1m")
					return summary, fmt.Errorf("enable stock Kline Timer fleet: %w", err)
				}
				if err := waitCollectorTimerFleetsEnabled(ctx, client, publishedTimerFleets); err != nil {
					_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, "builtin-stock-cn-kline-1m")
					return summary, fmt.Errorf("verify stock Kline Timer fleet enabled: %w", err)
				}
				jobs = append(jobs, enableJobs...)
			}
			if hasInstrumentSnapshotFleet {
				// The daily full-market snapshot is enabled only after the Kline
				// activation decision is complete, avoiding a half-active release.
				enableJobs, enableErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorStockCNInstrumentTimerEnablePatches(instrumentSnapshotFleet.nodes, fetcherConfig.InstrumentSnapshotTimerCron))
				if enableErr != nil {
					return summary, fmt.Errorf("enable daily stock instrument snapshot Timer: %w", enableErr)
				}
				if err := waitCollectorBatches(ctx, client, enableJobs); err != nil {
					return summary, fmt.Errorf("enable daily stock instrument snapshot Timer: %w", err)
				}
				if err := waitCollectorInstrumentTimerEnabled(ctx, client, instrumentSnapshotFleet.opts); err != nil {
					return summary, fmt.Errorf("verify daily stock instrument snapshot Timer: %w", err)
				}
				jobs = append(jobs, enableJobs...)
			}
			if opts.EnableStockCN {
				summary.StockCNEnabled = true
			}
			// Without --enable-stock-cn, Kline Timer nodes and the default rule
			// remain disabled after publication. The independent Instrument Timer
			// is enabled above because its daily snapshot workflow is self-contained.
			summary.JobIDs = append([]string(nil), jobs...)
			summary.JobID = strings.Join(summary.JobIDs, ",")
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

type collectorStockCNActivationSummary struct {
	SpaceID            string   `json:"space_id"`
	PackageVersion     string   `json:"package_version"`
	ExpectedTimerCount int      `json:"expected_timer_count"`
	TimerCount         int      `json:"timer_count"`
	InstrumentNodeID   string   `json:"instrument_node_id"`
	TimerJobIDs        []string `json:"timer_job_ids,omitempty"`
	InstrumentJobIDs   []string `json:"instrument_job_ids,omitempty"`
	RuleID             string   `json:"rule_id"`
	Enabled            bool     `json:"enabled"`
}

// activateStockCNCollection resumes a fleet that was published but left
// disabled by a failed or interrupted final canary. It rechecks package
// identity and the independent Instrument canary before enabling the daily
// snapshot, Kline Timers, and Kline rule. Egress probing is diagnostic only.
func activateStockCNCollection(ctx context.Context, opts collectorStockCNActivateOptions) (collectorStockCNActivationSummary, error) {
	summary := collectorStockCNActivationSummary{SpaceID: opts.SpaceID, PackageVersion: opts.Version, RuleID: "builtin-stock-cn-kline-1m"}
	if strings.TrimSpace(opts.ControlURL) == "" {
		return summary, fmt.Errorf("--control-url is required")
	}
	if strings.TrimSpace(opts.File) == "" {
		return summary, fmt.Errorf("--file is required")
	}
	if strings.TrimSpace(opts.Version) == "" {
		return summary, fmt.Errorf("--version is required to prevent activating an older package")
	}
	fetcherConfig, err := loadCollectorSCFFetcherConfig(opts.File, opts.SpaceID)
	if err != nil {
		return summary, err
	}
	if fetcherConfig == nil || !strings.EqualFold(fetcherConfig.SpaceID, "stock_cn") {
		return summary, fmt.Errorf("activation requires an enabled stock_cn manifest")
	}
	client := newControlClient(opts.ControlURL, opts.AccessToken, opts.ServiceAccessKey, opts.ServiceSecretKey, fetcherConfig.SpaceID)
	enabledRules, err := client.ListEnabledTaskRules(ctx, fetcherConfig.SpaceID, "equity")
	if err != nil {
		return summary, fmt.Errorf("stock_cn activation requires a rule readback: %w", err)
	}
	// An interrupted activation can leave the target rule enabled after its
	// Timer fleet rollback. Treat that state as resumable; unrelated equity
	// rules still block activation to avoid changing another workflow.
	for _, rule := range enabledRules {
		if rule.RuleID != summary.RuleID {
			return summary, fmt.Errorf("stock_cn activation requires no unrelated equity collector rules enabled; enabled rule: %s", rule.RuleID)
		}
	}

	var fleets []collectorPublishedTimerFleet
	var allTimerNodes []adminclient.CloudNode
	for _, region := range fetcherConfig.Regions {
		if !region.Enabled || region.FunctionCount <= 0 {
			continue
		}
		regionOpts := collectorPublishOptions{
			SpaceID:                 fetcherConfig.SpaceID,
			CloudAccountID:          region.CloudAccountID,
			Region:                  region.Region,
			NodeType:                "scf-event",
			BizType:                 "market_fetcher",
			TriggerType:             "timer",
			NodeCount:               region.FunctionCount,
			FunctionNamePrefix:      fetcherConfig.FunctionPrefix,
			StorageRPCGatewayTarget: fetcherConfig.StorageRPCGatewayTarget,
			FetcherConfig:           fetcherConfig,
		}
		nodes, inspectErr := inspectCollectorFleet(ctx, client, regionOpts)
		if inspectErr != nil {
			return summary, fmt.Errorf("inspect stock Kline fleet in %s: %w", region.Region, inspectErr)
		}
		if len(nodes) != region.FunctionCount {
			return summary, fmt.Errorf("stock Kline fleet in %s has %d nodes; expected %d", region.Region, len(nodes), region.FunctionCount)
		}
		for _, node := range nodes {
			if !strings.Contains(node.PackageID, opts.Version) {
				return summary, fmt.Errorf("stock Kline node %s is on package %q, expected version %q", node.NodeID, node.PackageID, opts.Version)
			}
		}
		fleets = append(fleets, collectorPublishedTimerFleet{opts: regionOpts, nodes: nodes})
		allTimerNodes = append(allTimerNodes, nodes...)
	}
	summary.ExpectedTimerCount = fetcherConfig.TimerFunctionCount
	summary.TimerCount = len(allTimerNodes)
	if len(allTimerNodes) != fetcherConfig.TimerFunctionCount {
		return summary, fmt.Errorf("stock Kline fleet has %d nodes; expected %d", len(allTimerNodes), fetcherConfig.TimerFunctionCount)
	}
	instrumentOpts := collectorPublishOptions{
		SpaceID:                 fetcherConfig.SpaceID,
		CloudAccountID:          fetcherConfig.InstrumentSnapshotCloudAccountID,
		Region:                  fetcherConfig.InstrumentSnapshotRegion,
		NodeType:                "scf-event",
		BizType:                 "market_fetcher",
		TriggerType:             "timer",
		InstrumentSnapshotTimer: true,
		NodeCount:               1,
		FunctionNamePrefix:      fetcherConfig.InstrumentSnapshotFunctionPrefix,
		StorageRPCGatewayTarget: fetcherConfig.StorageRPCGatewayTarget,
		FetcherConfig:           fetcherConfig,
	}
	instrumentNodes, err := inspectCollectorFleet(ctx, client, instrumentOpts)
	if err != nil {
		return summary, fmt.Errorf("inspect stock Instrument Timer: %w", err)
	}
	if len(instrumentNodes) != 1 {
		return summary, fmt.Errorf("stock Instrument Timer has %d nodes; expected 1", len(instrumentNodes))
	}
	if !strings.Contains(instrumentNodes[0].PackageID, opts.Version) {
		return summary, fmt.Errorf("stock Instrument Timer is on package %q, expected version %q", instrumentNodes[0].PackageID, opts.Version)
	}
	summary.InstrumentNodeID = instrumentNodes[0].NodeID
	if err := runCollectorInstrumentCanaryWithExtendedControlTimeout(ctx, client, instrumentOpts, instrumentNodes[0].NodeID); err != nil {
		return summary, fmt.Errorf("stock Instrument canary: %w", err)
	}
	if err := ensureStockCNKlineRule(ctx, client, fetcherConfig.SpaceID); err != nil {
		disableJobs, disableErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorTimerDisablePatches(append(allTimerNodes, instrumentNodes...)))
		if disableErr == nil {
			disableErr = waitCollectorBatches(ctx, client, disableJobs)
		}
		if disableErr != nil {
			return summary, fmt.Errorf("enable stock Kline rule: %w; rollback Timer state failed: %v", err, disableErr)
		}
		return summary, fmt.Errorf("enable stock Kline rule: %w", err)
	}
	enabledRules, err = client.ListEnabledTaskRules(ctx, fetcherConfig.SpaceID, "equity")
	if err != nil {
		return summary, fmt.Errorf("verify stock Kline rule enabled: %w", err)
	}
	for _, rule := range enabledRules {
		if rule.RuleID == summary.RuleID && rule.Enabled {
			if err := waitCollectorTimerFleetsAssigned(ctx, client, fleets); err != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("verify stock Kline assignments after rule enable: %w", err)
			}
			timerJobs, timerErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorStockCNTimerRestorePatches(allTimerNodes, *fetcherConfig))
			if timerErr != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("enable stock Kline Timer fleet: %w", timerErr)
			}
			if timerErr = waitCollectorBatches(ctx, client, timerJobs); timerErr != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("enable stock Kline Timer fleet: %w", timerErr)
			}
			if timerErr = waitCollectorTimerFleetsEnabled(ctx, client, fleets); timerErr != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("verify stock Kline Timer fleet: %w", timerErr)
			}
			summary.TimerJobIDs = append(summary.TimerJobIDs, timerJobs...)
			instrumentJobs, instrumentErr := submitCollectorTimerRuntimeConfigs(ctx, client, collectorStockCNInstrumentTimerEnablePatches(instrumentNodes, fetcherConfig.InstrumentSnapshotTimerCron))
			if instrumentErr != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("enable daily stock Instrument Timer: %w", instrumentErr)
			}
			if instrumentErr = waitCollectorBatches(ctx, client, instrumentJobs); instrumentErr != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("enable daily stock Instrument Timer: %w", instrumentErr)
			}
			if instrumentErr = waitCollectorInstrumentTimerEnabled(ctx, client, instrumentOpts); instrumentErr != nil {
				_ = client.DisableTaskRule(ctx, fetcherConfig.SpaceID, summary.RuleID)
				return summary, fmt.Errorf("verify daily stock Instrument Timer: %w", instrumentErr)
			}
			summary.InstrumentJobIDs = append(summary.InstrumentJobIDs, instrumentJobs...)
			summary.Enabled = true
			return summary, nil
		}
	}
	return summary, fmt.Errorf("stock Kline rule enable was not confirmed by control-plane readback")
}

func ensureStockCNKlineRule(ctx context.Context, client *adminclient.Client, spaceID string) error {
	const ruleID = "builtin-stock-cn-kline-1m"
	if err := client.EnableTaskRule(ctx, spaceID, ruleID); err == nil {
		return nil
	} else if !strings.Contains(err.Error(), "record not found") {
		return err
	}
	if err := client.CreateTaskRule(ctx, spaceID, ruleID, "kline", "stock_cn_multi", "equity", "moox-cli", map[string]any{
		"provider":          "stock_cn_multi",
		"market_type":       "equity",
		"symbol_source":     "dataset",
		"symbol_dataset_id": "stock_cn_instruments",
		"target_dataset_id": "stock_cn_kline",
		"frequency":         "1m",
	}); err != nil {
		return fmt.Errorf("create missing stock Kline rule: %w", err)
	}
	if err := client.EnableTaskRule(ctx, spaceID, ruleID); err != nil {
		return fmt.Errorf("enable newly created stock Kline rule: %w", err)
	}
	return nil
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

func validateCollectorZipLogging(zipPath, _ string) error {
	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("open SCF zip: %w", err)
	}
	defer archive.Close()
	mainFound, configFound := false, false
	for _, file := range archive.File {
		switch file.Name {
		case "main":
			mainFound = true
		case "config.yaml":
			configFound = true
		case "trpc_go.yaml", "example_trpc_go.yaml":
			return fmt.Errorf("SCF zip must not contain %s", file.Name)
		}
	}
	if !mainFound || !configFound {
		return fmt.Errorf("SCF zip must contain main and config.yaml")
	}
	return nil
}

func loadCollectorSCFFetcherConfig(path, spaceID string) (*setupconfig.SCFFetcherSpace, error) {
	fetcher, _, err := loadCollectorSCFFetcherConfigSnapshot(path, spaceID)
	return fetcher, err
}

func loadCollectorSCFFetcherConfigSnapshot(path, spaceID string) (*setupconfig.SCFFetcherSpace, *setupconfig.Snapshot, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil, nil
	}
	root := filepath.Dir(path)
	snapshot, err := setupconfig.Load(path, root)
	if err != nil {
		return nil, nil, fmt.Errorf("load collector SCF config: %w", err)
	}
	if !snapshot.Manifest.SCFFetcher.Enabled {
		return nil, snapshot, nil
	}
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return nil, nil, fmt.Errorf("--space-id is required to select scf_fetcher.spaces")
	}
	for index := range snapshot.Manifest.SCFFetcher.Spaces {
		space := &snapshot.Manifest.SCFFetcher.Spaces[index]
		if space.SpaceID == spaceID {
			return space, snapshot, nil
		}
	}
	return nil, nil, fmt.Errorf("scf_fetcher has no configuration for space %q", spaceID)
}

func resolveCollectorSCFTrustMaterial(ctx context.Context, controlHost setupconfig.Host, controlRoot string) (collectorSCFTrustMaterial, error) {
	transport, err := setupssh.Dial(ctx, sshTarget(controlHost), controlHost.Password, setupssh.Options{Timeout: 15 * time.Second})
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("connect control host to read SCF trust material: %w", err)
	}
	defer transport.Close()

	credentialRaw, err := readRemoteControlFile(ctx, transport, ".config/moox/eventbus/market-fetch-publisher.yaml")
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("read control EventBus publisher credential: %w", err)
	}
	credential, err := decodeEventBusCredential(string(credentialRaw))
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("decode control EventBus publisher credential: %w", err)
	}
	eventBusCA, err := readRemoteControlFile(ctx, transport, ".config/moox/eventbus/ca.pem")
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("read control EventBus CA: %w", err)
	}
	storageAuthRaw, err := readRemoteControlFile(ctx, transport, filepath.Join(controlRoot, "secrets/storage-internal-auth.env"))
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("read control Storage auth: %w", err)
	}
	storagePrimaryAuthSecret, err := collectorStoragePrimaryAuthSecret(storageAuthRaw)
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("validate control Storage auth: %w", err)
	}
	gatewayCA, err := readRemoteControlFile(ctx, transport, filepath.Join(controlRoot, "certs/gateway/peers.pem"))
	if err != nil {
		return collectorSCFTrustMaterial{}, fmt.Errorf("read control Gateway peer CA: %w", err)
	}
	// Timer-triggered market-fetch functions call the Storage tRPC endpoint
	// directly and do not use the HTTPS service gateway.  Public Caddy mode
	// intentionally has no local root.crt (the platform CA is sufficient), so
	// a missing service-gateway CA must not block publishing this short-lived
	// fleet.  Keep the material when an internal CA is present for other
	// trigger types, but treat it as optional here.
	serviceGatewayCA, _ := readRemoteControlFile(ctx, transport, filepath.Join(controlRoot, "certs/caddy/root.crt"))
	return collectorSCFTrustMaterial{
		EventBusCredential:       credential,
		EventBusCAPEM:            eventBusCA,
		GatewayCAPEM:             gatewayCA,
		ServiceGatewayCAPEM:      serviceGatewayCA,
		StoragePrimaryAuthSecret: storagePrimaryAuthSecret,
	}, nil
}

func readRemoteControlFile(ctx context.Context, transport setupssh.Client, relativePath string) ([]byte, error) {
	command := `cat "$HOME/$1"`
	if filepath.IsAbs(relativePath) {
		command = `cat "$1"`
	}
	result, err := transport.Run(ctx, []string{"sh", "-lc", command, "moox-scf-trust", relativePath}, nil)
	if err != nil || len(result.Stdout) == 0 || len(result.Stdout) > 1<<20 {
		return nil, fmt.Errorf("control_public_file_unavailable")
	}
	return []byte(result.Stdout), nil
}

func collectorStoragePrimaryAuthSecret(raw []byte) (string, error) {
	normalized, err := normalizeStorageInternalAuth(string(raw))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(normalized, "\n") {
		if value, ok := strings.CutPrefix(line, "MOOX_STORAGE_PRIMARY_AUTH_SECRET="); ok {
			return value, nil
		}
	}
	return "", fmt.Errorf("primary auth secret is missing")
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
	fleetNodes, err := selectCollectorFleetNodesForTrigger(
		catalogNodes,
		opts.FunctionNamePrefix,
		defaultFlag(opts.BizType, "market_fetcher"),
		opts.NodeCount,
		defaultFlag(opts.TriggerType, "timer"),
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
		return summary, fmt.Errorf("collector fleet is partially populated (%d existing, %d missing); refusing a mixed-version deployment", existing, len(missing))
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

// waitCollectorBatch makes publishing fail closed. CloudNode submissions are
// asynchronous; proceeding to the next fleet without observing a terminal
// success used to hide failed SCF deployments until the first production
// timer fired.
func waitCollectorBatch(ctx context.Context, client *adminclient.Client, jobID string) error {
	if strings.TrimSpace(jobID) == "" {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	const statusRequestTimeout = 30 * time.Second
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var change *adminclient.NodeBatchChangeResponse
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			statusCtx, statusCancel := context.WithTimeout(waitCtx, statusRequestTimeout)
			candidate, err := client.GetNodeBatchChange(statusCtx, jobID)
			statusCancel()
			if err == nil {
				change = candidate
				lastErr = nil
				break
			}
			lastErr = err
			select {
			case <-waitCtx.Done():
				return fmt.Errorf("wait batch %s: %w", jobID, waitCtx.Err())
			case <-time.After(time.Second):
			}
		}
		if lastErr != nil {
			return fmt.Errorf("GetNodeBatchChange for batch %s: %w", jobID, lastErr)
		}
		if change == nil || change.Job == nil {
			return fmt.Errorf("empty batch status")
		}
		switch strings.ToUpper(strings.TrimSpace(change.Job.Status)) {
		case "NODE_BATCH_STATUS_SUCCESS", "SUCCESS", "SUCCEEDED":
			return nil
		case "NODE_BATCH_STATUS_FAILED", "FAILED", "NODE_BATCH_STATUS_PARTIAL", "PARTIAL":
			return fmt.Errorf("batch %s ended with status %s failed=%d", jobID, change.Job.Status, change.Job.FailedCount)
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("wait batch %s: %w", jobID, waitCtx.Err())
		case <-ticker.C:
		}
	}
}

// runCollectorSCFCanary exercises the same bounded market_fetch path used by
// the scheduler. Crypto uses a realtime request; stock_cn uses a bounded
// historical request that stays inside the active providers' verified page
// capability. A successful response proves credentials, CA, EventBus ACL/ACK
// and Storage auth together.
func runCollectorSCFCanary(ctx context.Context, client *adminclient.Client, opts collectorPublishOptions, nodeID string) error {
	batchID := fmt.Sprintf("deploy-canary-%d", time.Now().UnixNano())
	response, err := client.InvokeFunction(ctx, nodeID, collectorSCFCanaryEvent(opts, nodeID, batchID))
	if err != nil {
		return err
	}
	if success, ok := response["success"].(bool); !ok || !success {
		raw, _ := json.Marshal(response)
		return fmt.Errorf("market_fetch canary returned unsuccessful response: %s", raw)
	}
	return nil
}

func runCollectorInstrumentCanary(ctx context.Context, client *adminclient.Client, opts collectorPublishOptions, nodeID string) error {
	snapshotAt := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339Nano)
	// The deployment gate uses one full-snapshot shard so the public-feed and
	// Storage work stays within one SCF invocation. Production scheduling still
	// uses the fixed 32-shard fan-out, whose staging/activation fence is covered
	// by the collector and Storage tests.
	canaryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	shards := make(chan int)
	errCh := make(chan error, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < stockInstrumentCanaryWorkerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for shardIndex := range shards {
				if err := runCollectorInstrumentCanaryShard(canaryCtx, client, opts, nodeID, shardIndex, snapshotAt); err != nil {
					select {
					case errCh <- err:
					default:
					}
					cancel()
					return
				}
			}
		}()
	}
	for shardIndex := 0; shardIndex < stockInstrumentCanaryShardCount; shardIndex++ {
		select {
		case shards <- shardIndex:
		case <-canaryCtx.Done():
			break
		}
		if canaryCtx.Err() != nil {
			break
		}
	}
	close(shards)
	workers.Wait()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

const (
	collectorInstrumentCanaryControlTimeout = 7 * time.Minute
	// CloudNode executes SCF trigger/environment updates in a bounded worker
	// pool. A 170-node stock fleet can legitimately need more than ten minutes
	// for assignment metadata to reach every node.
	collectorStockCNFleetWaitTimeout = 30 * time.Minute
)

func runCollectorInstrumentCanaryWithExtendedControlTimeout(ctx context.Context, client *adminclient.Client, opts collectorPublishOptions, nodeID string) error {
	if client == nil {
		return fmt.Errorf("control client is required")
	}
	previous := client.HTTPClient
	client.HTTPClient = &http.Client{Timeout: collectorInstrumentCanaryControlTimeout}
	defer func() { client.HTTPClient = previous }()
	return runCollectorInstrumentCanary(ctx, client, opts, nodeID)
}

func runCollectorInstrumentCanaryShard(ctx context.Context, client *adminclient.Client, opts collectorPublishOptions, nodeID string, shardIndex int, snapshotAt string) error {
	var lastErr error
	for attempt := 1; attempt <= stockInstrumentCanaryMaxAttempts; attempt++ {
		batchID := fmt.Sprintf("instrument-deploy-canary-%d-%d", time.Now().UnixNano(), shardIndex)
		response, err := client.InvokeFunction(ctx, nodeID, collectorInstrumentCanaryEvent(opts, nodeID, batchID, shardIndex, stockInstrumentCanaryShardCount, snapshotAt))
		if err == nil {
			if success, ok := response["success"].(bool); ok && success {
				return nil
			}
			raw, _ := json.Marshal(response)
			lastErr = fmt.Errorf("returned unsuccessful response: %s", raw)
		} else {
			lastErr = err
		}
		if attempt < stockInstrumentCanaryMaxAttempts {
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	return fmt.Errorf("instrument_snapshot shard %d/%d failed after %d attempts: %w", shardIndex+1, stockInstrumentCanaryShardCount, stockInstrumentCanaryMaxAttempts, lastErr)
}

const (
	// A full-snapshot canary keeps one SCF invocation within the public-feed
	// budget. The stock production Instrument Timer is also one full-snapshot
	// invocation; the shard protocol remains for the shared pipeline and legacy
	// crypto scheduling, while this canary proves provider merge and activation.
	stockInstrumentCanaryShardCount  = 1
	stockInstrumentCanaryMaxAttempts = 5
	// Each shard invocation fetches the same complete snapshot before taking
	// its deterministic slice. Keep the gate bounded without exceeding the
	// source's per-egress-IP request policy when SCF scales the invoke node.
	stockInstrumentCanaryWorkerCount = 4
)

func collectorInstrumentCanaryEvent(opts collectorPublishOptions, nodeID, batchID string, shardIndex, shardCount int, snapshotAt string) map[string]any {
	spaceID := firstNonEmpty(opts.SpaceID, opts.collectorPackageOptions.SpaceID)
	return map[string]any{
		"action":                     "instrument_snapshot",
		"request_id":                 batchID,
		"storage_rpc_gateway_target": opts.StorageRPCGatewayTarget,
		"data": map[string]any{
			"batch_id":    batchID,
			"schedule_id": "deploy-canary-schedule",
			"batch_kind":  "instrument_snapshot",
			"space_id":    spaceID,
			"dataset_id":  "stock_cn_instruments",
			"frequency":   "1d",
			"provider":    "stock_cn_multi",
			"market_type": "equity",
			"region":      opts.Region,
			"node_id":     nodeID,
			"request_id":  batchID,
			"items": []map[string]any{{
				"subject_id":           "stock_cn_instruments",
				"provider":             "stock_cn_multi",
				"market_type":          "equity",
				"data_type":            "instrument",
				"dataset_id":           "stock_cn_instruments",
				"snapshot_at":          snapshotAt,
				"snapshot_shard_index": shardIndex,
				"snapshot_shard_count": shardCount,
			}},
		},
	}
}

func collectorTimerRestorePatches(nodes []adminclient.CloudNode) []collectorRuntimeConfigPatch {
	patches := make([]collectorRuntimeConfigPatch, 0, len(nodes))
	for _, node := range nodes {
		enabled, ok := collectorMetadataBool(node.Metadata, "timer_enabled")
		if !ok || !enabled {
			continue
		}
		cron := firstNonEmpty(metadataStringValue(node.Metadata, "timer_cron"), metadataStringValue(node.Metadata, "timer_actual_cron"))
		if cron == "" {
			continue
		}
		patches = append(patches, collectorRuntimeConfigPatch{NodeID: node.NodeID, TimerEnabled: true, TimerCron: cron})
	}
	return patches
}

// collectorStockCNTimerRestorePatches returns an enable patch for every
// published stock Timer. New SCF nodes do not have timer metadata yet, so use
// the same stable region/slot ordering as the Collector reconciler to give
// them a deterministic cron until the first reconciliation writes the exact
// assignment.
func collectorStockCNTimerRestorePatches(nodes []adminclient.CloudNode, configs ...setupconfig.SCFFetcherSpace) []collectorRuntimeConfigPatch {
	startSecond := setupconfig.DefaultStockCNStaggerStartSecond
	windowSeconds := setupconfig.DefaultStockCNStaggerWindowSeconds
	if len(configs) > 0 {
		if configs[0].StaggerStartSecond != 0 || configs[0].StaggerWindowSeconds != 0 {
			startSecond = configs[0].StaggerStartSecond
			windowSeconds = configs[0].StaggerWindowSeconds
		}
	}
	if windowSeconds <= 0 {
		windowSeconds = setupconfig.DefaultStockCNStaggerWindowSeconds
	}
	ordered := append([]adminclient.CloudNode(nil), nodes...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Region != ordered[j].Region {
			return ordered[i].Region < ordered[j].Region
		}
		left, leftOK := metadataIntValue(ordered[i].Metadata, "index")
		right, rightOK := metadataIntValue(ordered[j].Metadata, "index")
		if leftOK && rightOK && left != right {
			return left < right
		}
		return ordered[i].FunctionName < ordered[j].FunctionName
	})
	patches := make([]collectorRuntimeConfigPatch, 0, len(ordered))
	for index, node := range ordered {
		if strings.TrimSpace(node.NodeID) == "" {
			continue
		}
		cron := fmt.Sprintf("%d * * * * * *", startSecond+index%windowSeconds)
		patches = append(patches, collectorRuntimeConfigPatch{NodeID: node.NodeID, TimerEnabled: true, TimerCron: cron})
	}
	return patches
}

func collectorTimerDisablePatches(nodes []adminclient.CloudNode) []collectorRuntimeConfigPatch {
	patches := make([]collectorRuntimeConfigPatch, 0, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.NodeID) == "" {
			continue
		}
		cron := firstNonEmpty(metadataStringValue(node.Metadata, "timer_cron"), metadataStringValue(node.Metadata, "timer_actual_cron"), "0 * * * * * *")
		patches = append(patches, collectorRuntimeConfigPatch{NodeID: node.NodeID, TimerEnabled: false, TimerCron: cron})
	}
	return patches
}

func collectorStockCNInstrumentTimerEnablePatches(nodes []adminclient.CloudNode, cron string) []collectorRuntimeConfigPatch {
	patches := make([]collectorRuntimeConfigPatch, 0, len(nodes))
	for _, node := range nodes {
		if strings.TrimSpace(node.NodeID) == "" || !isCollectorInstrumentSnapshotNode(node) {
			continue
		}
		patches = append(patches, collectorRuntimeConfigPatch{NodeID: node.NodeID, TimerEnabled: true, TimerCron: cron})
	}
	return patches
}

func waitCollectorInstrumentTimerEnabled(ctx context.Context, client *adminclient.Client, opts collectorPublishOptions) error {
	waitCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		nodes, err := inspectCollectorFleet(waitCtx, client, opts)
		if err != nil {
			return err
		}
		for _, node := range nodes {
			if isCollectorInstrumentSnapshotNode(node) {
				enabled, ok := collectorMetadataBool(node.Metadata, "timer_actual_enabled")
				if ok && enabled {
					return nil
				}
			}
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func waitCollectorTimerFleetsEnabled(ctx context.Context, client *adminclient.Client, fleets []collectorPublishedTimerFleet) error {
	waitDuration := 10 * time.Minute
	if collectorTimerFleetsAreStockCN(fleets) {
		waitDuration = collectorStockCNFleetWaitTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, waitDuration)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		ready := true
		for _, fleet := range fleets {
			nodes, err := inspectCollectorFleet(waitCtx, client, fleet.opts)
			if err != nil {
				return err
			}
			if len(nodes) != fleet.opts.NodeCount {
				ready = false
				break
			}
			for _, node := range nodes {
				enabled, hasEnabled := collectorMetadataBool(node.Metadata, "timer_actual_enabled")
				if !hasEnabled || !enabled {
					ready = false
					break
				}
			}
			if !ready {
				break
			}
		}
		if ready {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func waitCollectorTimerFleetsAssigned(ctx context.Context, client *adminclient.Client, fleets []collectorPublishedTimerFleet) error {
	waitDuration := 10 * time.Minute
	if collectorTimerFleetsAreStockCN(fleets) {
		waitDuration = collectorStockCNFleetWaitTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, waitDuration)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		ready := true
		for _, fleet := range fleets {
			nodes, err := inspectCollectorFleet(waitCtx, client, fleet.opts)
			if err != nil {
				return err
			}
			if len(nodes) != fleet.opts.NodeCount {
				ready = false
				break
			}
			for _, node := range nodes {
				assignmentCount, hasAssignment := metadataIntValue(node.Metadata, "assignment_count")
				if !hasAssignment || assignmentCount <= 0 {
					ready = false
					break
				}
			}
			if !ready {
				break
			}
		}
		if ready {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return waitCtx.Err()
		case <-ticker.C:
		}
	}
}

func collectorTimerFleetsAreStockCN(fleets []collectorPublishedTimerFleet) bool {
	return len(fleets) > 0 && strings.EqualFold(strings.TrimSpace(fleets[0].opts.SpaceID), "stock_cn")
}

func collectorMetadataBool(metadata map[string]any, key string) (bool, bool) {
	value, ok := metadata[key]
	if !ok {
		return false, false
	}
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}

func submitCollectorTimerRuntimeConfigs(ctx context.Context, client *adminclient.Client, patches []collectorRuntimeConfigPatch) ([]string, error) {
	if len(patches) == 0 {
		return nil, nil
	}
	jobs := make([]string, 0, (len(patches)+collectorRuntimeConfigBatchSize-1)/collectorRuntimeConfigBatchSize)
	for start := 0; start < len(patches); start += collectorRuntimeConfigBatchSize {
		end := min(start+collectorRuntimeConfigBatchSize, len(patches))
		var response struct {
			RetInfo *struct {
				Code int    `json:"code"`
				Msg  string `json:"msg"`
			} `json:"ret_info"`
			JobID string `json:"job_id"`
		}
		if err := client.CallJSON(ctx, http.MethodPost, "/api/admin/cloudnode/SubmitUpdateNodeRuntimeConfigs", map[string]any{"nodes": patches[start:end]}, &response); err != nil {
			return nil, err
		}
		if response.RetInfo == nil || response.RetInfo.Code != 0 && response.RetInfo.Code != 200 {
			if response.RetInfo == nil {
				return nil, fmt.Errorf("SubmitUpdateNodeRuntimeConfigs returned no ret_info")
			}
			return nil, fmt.Errorf("SubmitUpdateNodeRuntimeConfigs: code %d: %s", response.RetInfo.Code, response.RetInfo.Msg)
		}
		if strings.TrimSpace(response.JobID) == "" {
			return nil, fmt.Errorf("SubmitUpdateNodeRuntimeConfigs returned no job_id")
		}
		jobs = append(jobs, response.JobID)
	}
	return jobs, nil
}

func waitCollectorBatches(ctx context.Context, client *adminclient.Client, jobIDs []string) error {
	for _, jobID := range jobIDs {
		if err := waitCollectorBatch(ctx, client, jobID); err != nil {
			return err
		}
	}
	return nil
}

func collectorSCFCanaryEvent(opts collectorPublishOptions, nodeID, batchID string) map[string]any {
	spaceID := firstNonEmpty(opts.SpaceID, opts.collectorPackageOptions.SpaceID)
	datasetID, provider, marketType := "binance_spot_kline_1m", "binance", "spot"
	subjectID, symbol, barLimit := "BTC-USDT", "BTCUSDT", 2
	batchKind := "realtime"
	startTime := ""
	if strings.EqualFold(spaceID, "stock_cn") {
		datasetID, provider, marketType = "stock_cn_kline", "stock_cn_multi", "equity"
		subjectID, symbol, barLimit = "600000.XSHG", "sh600000", 1000
		batchKind = "backfill"
		// Historical requests must carry an explicit bounded start. Keep a
		// small margin below the providers' 24-hour capability because the
		// event is created before the SCF invocation reaches the feed guard.
		// The stock runtime marks this item as a canary and replaces the
		// placeholder with the latest closed calendar session, so a weekend or
		// holiday can still replay a real historical page safely.
		startTime = time.Now().UTC().Add(-23 * time.Hour).Truncate(time.Minute).Format(time.RFC3339Nano)
	}
	return map[string]any{
		"action":                     "market_fetch",
		"storage_rpc_gateway_target": opts.StorageRPCGatewayTarget,
		"data": map[string]any{
			"batch_id": batchID,
			// Completion events are validated against the scheduler's batch
			// identity. Keep a stable schedule fence on the canary as well;
			// omitting it makes the canary fail after Storage has already
			// succeeded, defeating the deployment gate.
			"schedule_id": "deploy-canary-schedule",
			"batch_kind":  batchKind,
			"space_id":    spaceID,
			"dataset_id":  datasetID,
			"frequency":   "1m",
			"provider":    provider,
			"market_type": marketType,
			"region":      opts.Region,
			"node_id":     nodeID,
			"items": []map[string]any{{
				"subject_id": subjectID, "symbol": symbol, "provider": provider, "market_type": marketType,
				// The exchange response includes the currently-open candle. A
				// one-bar request therefore has no closed bar to persist; request
				// enough bars for the selected market to validate an actual write.
				"data_type": "kline", "dataset_id": datasetID, "frequency": "1m", "bar_limit": barLimit, "start_time": startTime, "canary": strings.EqualFold(spaceID, "stock_cn"),
			}},
		},
	}
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
	GroupID         int    `json:"group_id,omitempty"`
	GroupCount      int    `json:"group_count,omitempty"`
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
	ExpectedCount       int                    `json:"expected_count,omitempty"`
	EligibleCount       int                    `json:"eligible_count,omitempty"`
	DistinctCount       int                    `json:"distinct_count,omitempty"`
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
	stockCN := strings.EqualFold(spaceID, "stock_cn")
	var fetcherConfig *setupconfig.SCFFetcherSpace
	if stockCN && strings.TrimSpace(opts.File) != "" {
		fetcherConfig, err = loadCollectorSCFFetcherConfig(opts.File, spaceID)
		if err != nil {
			return nil, err
		}
	}
	expectedCount, err := resolveCollectorProbeExpectedCount(spaceID, opts.Region, fetcherConfig)
	if err != nil {
		return nil, err
	}
	eligible := selectCollectorProbeNodes(nodes, stockCN)
	if len(eligible) == 0 {
		return nil, fmt.Errorf("no active market_fetcher SCF nodes are available for egress probe")
	}
	return probeCollectorEgressNodes(ctx, client, eligible, stockCN, expectedCount)
}

func probeCollectorEgressNodes(ctx context.Context, client *adminclient.Client, eligible []adminclient.CloudNode, stockCN bool, expectedCount int) (*collectorProbeReport, error) {
	report := &collectorProbeReport{Results: make([]collectorProbeResult, len(eligible)), ExpectedCount: expectedCount, EligibleCount: len(eligible)}
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
			groupID, _ := metadataIntValue(node.Metadata, "group_id")
			groupCount, _ := metadataIntValue(node.Metadata, "group_count")
			result := collectorProbeResult{NodeID: node.NodeID, Region: node.Region, FunctionName: node.FunctionName, GroupID: groupID, GroupCount: groupCount, CheckedAt: started.UTC().Format(time.RFC3339Nano)}
			provider, marketType := "binance", "spot"
			if stockCN {
				provider, marketType = "sina", "stock_cn"
			}
			response, invokeErr := client.InvokeFunction(ctx, node.NodeID, map[string]any{"action": "egress_probe", "data": map[string]any{"provider": provider, "market_type": marketType, "node_id": node.NodeID}})
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
	report.DistinctCount = len(report.DistinctOutboundIPs)
	if stockCN {
		publishCollectorEgressMetric(ctx, report)
	}
	if stockCN {
		// Stock egress is observational only. Provider/Kline canaries and
		// Storage readback are the release checks; IP distribution is reported
		// for diagnosis but never blocks publication or activation.
		return report, nil
	}
	if failed.Load() > 0 {
		return report, fmt.Errorf("%d SCF egress probe(s) failed", failed.Load())
	}
	return report, nil
}

func resolveCollectorProbeExpectedCount(spaceID, region string, cfg *setupconfig.SCFFetcherSpace) (int, error) {
	if !strings.EqualFold(strings.TrimSpace(spaceID), "stock_cn") {
		return 0, nil
	}
	region = strings.TrimSpace(region)
	if cfg == nil {
		if region != "" {
			return 0, nil
		}
		return setupconfig.DefaultStockCNMarketTimerFunctionCount, nil
	}
	if region == "" {
		return cfg.TimerFunctionCount, nil
	}
	for _, item := range cfg.Regions {
		if item.Enabled && item.Region == region {
			return item.FunctionCount, nil
		}
	}
	return 0, fmt.Errorf("stock_cn SCF config has no enabled region %q", region)
}

func selectCollectorProbeNodes(nodes []adminclient.CloudNode, timerOnly bool) []adminclient.CloudNode {
	eligible := make([]adminclient.CloudNode, 0, len(nodes))
	for _, node := range nodes {
		if !collectorProbeNodeEligible(node) {
			continue
		}
		if timerOnly && !strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer") {
			continue
		}
		if timerOnly && isCollectorInstrumentSnapshotNode(node) {
			continue
		}
		eligible = append(eligible, node)
	}
	return eligible
}

func selectCollectorInstrumentSnapshotProbeNodes(nodes []adminclient.CloudNode) []adminclient.CloudNode {
	eligible := make([]adminclient.CloudNode, 0, len(nodes))
	for _, node := range nodes {
		if !collectorProbeNodeEligible(node) || !strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer") || !isCollectorInstrumentSnapshotNode(node) {
			continue
		}
		eligible = append(eligible, node)
	}
	return eligible
}

func isCollectorInstrumentSnapshotNode(node adminclient.CloudNode) bool {
	if strings.EqualFold(strings.TrimSpace(fmt.Sprint(node.Metadata["function_mode"])), "instrument_snapshot") {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(node.FunctionName))
	return strings.Contains(name, "-instrument-") || strings.HasSuffix(name, "-instrument")
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

// publishCollectorEgressMetric makes the one-time diagnostic visible to the
// same Monitor stream as recurring SCF feed metrics. It is best effort.
func publishCollectorEgressMetric(parent context.Context, gate *collectorProbeReport) {
	if gate == nil || !metricsEventBusConfigured() {
		return
	}
	registry := prometheus.NewRegistry()
	functions := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "moox_collector_market_egress_functions", Help: "Expected, returned, non-empty-IP, and distinct-IP function counts."}, []string{"market_id", "route_id", "kind"})
	if err := registry.Register(functions); err != nil {
		return
	}
	nonEmpty := 0
	for _, item := range gate.Results {
		if strings.TrimSpace(item.OutboundIP) != "" {
			nonEmpty++
		}
	}
	values := map[string]int{"expected": gate.ExpectedCount, "result": len(gate.Results), "non_empty_ip": nonEmpty, "distinct_ip": gate.DistinctCount}
	for kind, value := range values {
		functions.WithLabelValues("stock_cn", "stock_cn_kline_1m_v1", kind).Set(float64(max(value, 0)))
	}
	cfg := metricsreport.DefaultConfig("collector", "moox_collector_cli")
	cfg.SpaceID = "stock_cn"
	cfg.InstanceID = firstNonEmpty(os.Getenv("MOOX_INSTANCE_ID"), "moox-cli")
	cfg.NodeID = firstNonEmpty(os.Getenv("MOOX_NODE_ID"), "moox-cli")
	cfg.BootID = "cli-" + strconv.FormatInt(time.Now().UTC().UnixNano(), 10)
	reporter, err := metricsreport.NewHandlerWithRegistry(cfg, registry)
	if err != nil {
		return
	}
	timeout := 750 * time.Millisecond
	if raw := strings.TrimSpace(os.Getenv("MOOX_METRICS_REPORT_TIMEOUT_MS")); raw != "" {
		if value, parseErr := strconv.Atoi(raw); parseErr == nil && value > 0 {
			timeout = time.Duration(value) * time.Millisecond
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	_ = reporter.Handle(ctx)
}

func metricsEventBusConfigured() bool {
	return firstNonEmpty(os.Getenv("MOOX_METRICS_EVENTBUS_URL"), os.Getenv("MOOX_EVENTBUS_URL"), os.Getenv("NATS_URL")) != ""
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

func scfCLSIngestHost(host string) string {
	host = strings.TrimSpace(host)
	if strings.HasSuffix(host, ".cls.tencentyun.com") {
		return strings.TrimSuffix(host, ".cls.tencentyun.com") + ".cls.tencentcs.com"
	}
	return host
}

func buildCollectorCreateNodeItem(opts collectorPublishOptions, packageID string) (adminclient.NodeCreateItem, error) {
	packageName := defaultFlag(opts.PackageName, "moox-collector")
	bizType := defaultFlag(opts.BizType, "market_fetcher")
	if len(opts.JobTypes) > 0 {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher does not consume CloudNode JobItem workloads")
	}
	// Timer functions do not use the EventBus or the HTTPS service gateway: they
	// call the native Storage tRPC gateway directly. Set the effective trigger
	// before building the environment so those large CA/EventBus values are not
	// accidentally copied into every Timer function and exceed Tencent's 4KB
	// environment limit.
	envOpts := opts
	envOpts.TriggerType = defaultFlag(opts.TriggerType, "timer")
	envOpts.BizType = bizType
	environment, err := collectorFunctionEnvironment(envOpts, packageID)
	if err != nil {
		return adminclient.NodeCreateItem{}, err
	}
	config := parseCollectorOverrides(opts.Config)
	if config == nil {
		config = make(map[string]string)
	}
	fetcher := opts.FetcherConfig
	if fetcher == nil {
		fetcher = defaultCollectorSCFFetcherSpace()
	}
	triggerType := defaultFlag(opts.TriggerType, "timer")
	effectiveTimeoutSeconds := defaultInt(fetcher.TimeoutSeconds, 15)
	spaceID := firstNonEmpty(opts.SpaceID, fetcher.SpaceID, opts.collectorPackageOptions.SpaceID)
	if strings.EqualFold(spaceID, "stock_cn") && opts.InstrumentSnapshotTimer {
		effectiveTimeoutSeconds = defaultInt(fetcher.InstrumentSnapshotTimeoutSeconds, setupconfig.DefaultStockCNInstrumentSnapshotTimeoutSeconds)
	} else if strings.EqualFold(spaceID, "stock_cn") && strings.EqualFold(triggerType, "invoke") {
		effectiveTimeoutSeconds = defaultInt(fetcher.InstrumentInvokeTimeoutSeconds, setupconfig.DefaultStockCNInstrumentInvokeTimeoutSeconds)
	}
	effectiveMemorySize := defaultInt(fetcher.MemorySize, 64)
	if strings.EqualFold(spaceID, "stock_cn") && opts.InstrumentSnapshotTimer {
		effectiveMemorySize = defaultInt(fetcher.InstrumentSnapshotMemorySize, setupconfig.DefaultStockCNInstrumentSnapshotMemorySize)
	}
	if strings.TrimSpace(config["timeout"]) == "" {
		config["timeout"] = strconv.Itoa(effectiveTimeoutSeconds)
	}
	if strings.TrimSpace(config["memory_size"]) == "" {
		config["memory_size"] = strconv.Itoa(effectiveMemorySize)
	}
	if strings.TrimSpace(config["storage_timeout_ms"]) == "" {
		config["storage_timeout_ms"] = strconv.Itoa(defaultInt(fetcher.StorageTimeoutMS, 5000))
	}
	if strings.TrimSpace(config["max_instance_concurrency"]) == "" {
		config["max_instance_concurrency"] = "1"
	}
	if collectorConfigInt(config, "memory_size", effectiveMemorySize) != effectiveMemorySize {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher memory_size is fixed at %dMB for %s", effectiveMemorySize, firstNonEmpty(spaceID, "unknown"))
	}
	if collectorConfigInt(config, "timeout", effectiveTimeoutSeconds) != effectiveTimeoutSeconds {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher timeout is fixed at %d seconds for %s %s", effectiveTimeoutSeconds, spaceID, triggerType)
	}
	maxInstanceConcurrency, concurrencyErr := collectorRuntimeConfigInt(config, "max_instance_concurrency", 1, 1)
	if concurrencyErr != nil {
		return adminclient.NodeCreateItem{}, concurrencyErr
	}
	if maxInstanceConcurrency != 1 {
		return adminclient.NodeCreateItem{}, fmt.Errorf("market_fetcher max_instance_concurrency is fixed at 1")
	}
	if err := validateCollectorRuntimeConfig(config, fetcher, strings.EqualFold(triggerType, "timer"), effectiveTimeoutSeconds); err != nil {
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
	functionMode := "kline"
	if opts.InstrumentSnapshotTimer {
		functionMode = "instrument_snapshot"
	}
	return adminclient.NodeCreateItem{
		CloudAccountID: opts.CloudAccountID,
		NodeType:       defaultFlag(opts.NodeType, "scf-event"),
		TriggerType:    defaultFlag(opts.TriggerType, "timer"),
		Runtime:        defaultFlag(opts.Runtime, "Go1"),
		Namespace:      defaultFlag(opts.Namespace, "default"),
		Handler:        defaultFlag(opts.Handler, "main"),
		Config:         config,
		Environment:    environment,
		Region:         opts.Region,
		PackageID:      packageID,
		Metadata: map[string]any{
			"function_name_prefix":     packageName,
			"function_mode":            functionMode,
			"biz_type":                 bizType,
			"memory_size":              effectiveInt("memory_size", effectiveMemorySize),
			"timeout_seconds":          effectiveTimeoutSeconds,
			"max_instance_concurrency": maxInstanceConcurrency,
			"max_inflight_requests":    effectiveInt("max_inflight_requests", defaultInt(fetcher.MaxInflightRequests, 10)),
			"realtime_batch_size":      effectiveInt("realtime_batch_size", defaultInt(fetcher.RealtimeBatchSize, 30)),
			"realtime_bar_limit":       effectiveInt("realtime_bar_limit", defaultInt(fetcher.RealtimeBarLimit, 3)),
			"request_timeout_ms":       effectiveInt("request_timeout_ms", defaultInt(fetcher.RequestTimeoutMS, 1000)),
			"http_max_attempts":        effectiveInt("http_max_attempts", defaultInt(fetcher.HTTPMaxAttempts, 4)),
			"storage_max_attempts":     effectiveInt("storage_max_attempts", defaultInt(fetcher.StorageMaxAttempts, 3)),
			"storage_timeout_ms":       effectiveInt("storage_timeout_ms", defaultInt(fetcher.StorageTimeoutMS, 5000)),
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
	if err := validateCollectorFleetRuntimeEnvironment(base.Environment, strings.EqualFold(defaultFlag(opts.TriggerType, "timer"), "timer"), strings.EqualFold(defaultFlag(opts.BizType, "market_fetcher"), "market_fetcher")); err != nil {
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

func validateCollectorFleetRuntimeEnvironment(environment map[string]string, timer bool, marketFetcher ...bool) error {
	marketFetch := len(marketFetcher) > 0 && marketFetcher[0]
	required := []string{
		"MOOX_SPACE_ID",
		"MOOX_CODE_PACKAGE_ID",
		"MOOX_GATEWAY_NODE_ID",
		"MOOX_GATEWAY_TARGET_NODE",
		"MOOX_GATEWAY_SERVICE_KEY_ID",
		"MOOX_GATEWAY_SERVICE_SECRET_KEY",
		"MOOX_CLS_ENABLED",
		"MOOX_CLS_ENDPOINT",
		"MOOX_CLS_TOPIC_ID",
		"MOOX_CLS_TIMEOUT_MS",
		"MOOX_CLS_SECRET_ID",
		"MOOX_CLS_SECRET_KEY",
	}
	if !timer && !marketFetch {
		required = append(required, "MOOX_GATEWAY_CA_PEM_B64")
		required = append(required, "MOOX_SERVICE_GATEWAY_CA_PEM_B64")
	} else {
		required = append(required, "MOOX_STORAGE_RPC_GATEWAY_TARGET")
	}
	for _, key := range required {
		if strings.TrimSpace(environment[key]) == "" {
			return fmt.Errorf("collector fleet runtime environment requires %s", key)
		}
	}
	if timer {
		if err := validateTimerStorageTarget(environment["MOOX_STORAGE_RPC_GATEWAY_TARGET"]); err != nil {
			return err
		}
	}
	return nil
}

func validateTimerStorageTarget(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "ip" || parsed.Hostname() == "" || parsed.Port() == "" {
		return fmt.Errorf("collector timer runtime environment requires MOOX_STORAGE_RPC_GATEWAY_TARGET as ip://host:port")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "localhost" || host == "ip6-localhost" {
		return fmt.Errorf("MOOX_STORAGE_RPC_GATEWAY_TARGET must not point to loopback")
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return fmt.Errorf("MOOX_STORAGE_RPC_GATEWAY_TARGET must not point to loopback")
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
	return selectCollectorFleetNodesForTrigger(nodes, prefix, bizType, expected, "")
}

func selectCollectorFleetNodesForTrigger(nodes []adminclient.CloudNode, prefix string, bizType string, expected int, triggerType string) ([]adminclient.CloudNode, error) {
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
		if expectedTrigger := strings.TrimSpace(triggerType); expectedTrigger != "" && !strings.EqualFold(strings.TrimSpace(node.TriggerType), expectedTrigger) {
			return nil, fmt.Errorf(
				"fleet prefix %q node %q has trigger_type %q; expected %q",
				prefix,
				node.NodeID,
				node.TriggerType,
				expectedTrigger,
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
		fetcher = defaultCollectorSCFFetcherSpace()
	}
	executionTimeoutSeconds := defaultInt(fetcher.TimeoutSeconds, 15)
	spaceID := firstNonEmpty(opts.SpaceID, fetcher.SpaceID)
	if strings.EqualFold(spaceID, "stock_cn") && opts.InstrumentSnapshotTimer {
		executionTimeoutSeconds = defaultInt(fetcher.InstrumentSnapshotTimeoutSeconds, setupconfig.DefaultStockCNInstrumentSnapshotTimeoutSeconds)
	} else if strings.EqualFold(spaceID, "stock_cn") && strings.EqualFold(strings.TrimSpace(opts.TriggerType), "invoke") {
		// Instrument and deployment canaries use the invoke fleet. Keep the
		// runtime execution budget aligned with the longer SCF timeout selected
		// by buildCollectorCreateNodeItem; otherwise a 15-second inner deadline
		// leaves only a few seconds after Storage/CLS/completion reserves.
		executionTimeoutSeconds = defaultInt(fetcher.InstrumentInvokeTimeoutSeconds, executionTimeoutSeconds)
	}
	setDefaultEnv(env, "MOOX_FETCH_TIMEOUT_SECONDS", strconv.Itoa(executionTimeoutSeconds))
	setDefaultEnv(env, "MOOX_FETCH_MAX_INFLIGHT_REQUESTS", strconv.Itoa(defaultInt(fetcher.MaxInflightRequests, 10)))
	setDefaultEnv(env, "MOOX_FETCH_REQUEST_TIMEOUT_MS", strconv.Itoa(defaultInt(fetcher.RequestTimeoutMS, 1000)))
	setDefaultEnv(env, "MOOX_FETCH_HTTP_MAX_ATTEMPTS", strconv.Itoa(defaultInt(fetcher.HTTPMaxAttempts, 4)))
	setDefaultEnv(env, "MOOX_FETCH_STORAGE_MAX_ATTEMPTS", strconv.Itoa(defaultInt(fetcher.StorageMaxAttempts, 3)))
	setDefaultEnv(env, "MOOX_FETCH_REALTIME_BATCH_SIZE", strconv.Itoa(defaultInt(fetcher.RealtimeBatchSize, 30)))
	setDefaultEnv(env, "MOOX_FETCH_REALTIME_BAR_LIMIT", strconv.Itoa(defaultInt(fetcher.RealtimeBarLimit, 3)))
	setDefaultEnv(env, "MOOX_FETCH_CATCHUP_BATCH_SIZE", strconv.Itoa(defaultInt(fetcher.CatchupBatchSize, 1)))
	setDefaultEnv(env, "MOOX_FETCH_CATCHUP_BAR_LIMIT", strconv.Itoa(defaultInt(fetcher.CatchupBarLimit, 1000)))
	setDefaultEnv(env, "MOOX_FETCH_STORAGE_TIMEOUT_MS", strconv.Itoa(defaultInt(fetcher.StorageTimeoutMS, 5000)))
	setDefaultEnv(env, "MOOX_FETCH_MAX_RETRY_ATTEMPTS", strconv.Itoa(defaultInt(fetcher.MaxRetryAttempts, 3)))
	if opts.InstrumentSnapshotTimer {
		setDefaultEnv(env, "MOOX_MARKET_FETCH_MODE", "instrument_snapshot")
	}
	setDefaultEnv(env, "MOOX_STORAGE_RPC_GATEWAY_TARGET", firstNonEmpty(opts.StorageRPCGatewayTarget, os.Getenv("MOOX_STORAGE_RPC_GATEWAY_TARGET"), os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_TARGET")))
	gatewayNodeID := firstNonEmpty(fetcher.StorageGatewayNodeID, os.Getenv("MOOX_SCF_STORAGE_GATEWAY_NODE_ID"), os.Getenv("MOOX_GATEWAY_NODE_ID"), os.Getenv("MOOX_GATEWAY_TARGET_NODE"))
	setDefaultEnv(env, "MOOX_GATEWAY_NODE_ID", gatewayNodeID)
	setDefaultEnv(env, "MOOX_GATEWAY_TARGET_NODE", gatewayNodeID)
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_KEY_ID", os.Getenv("MOOX_COLLECTOR_GATEWAY_SERVICE_KEY_ID"))
	setDefaultEnv(env, "MOOX_GATEWAY_SERVICE_SECRET_KEY", os.Getenv("MOOX_COLLECTOR_GATEWAY_SERVICE_SECRET_KEY"))
	defaultCLSSecretID, defaultCLSSecretKey := collectorCLSCredentials()
	setDefaultEnv(env, "MOOX_CLS_ENABLED", "true")
	setDefaultEnv(env, "MOOX_CLS_ENDPOINT", firstNonEmpty(opts.CLSHost, os.Getenv("MOOX_CLS_ENDPOINT"), clsprepare.Host))
	// The logset is not needed by the CLS write API, but carrying the resolved
	// initialization resource makes each deployed function diagnosable without
	// baking infrastructure IDs into the SCF zip.
	setDefaultEnv(env, "MOOX_CLS_LOGSET_ID", opts.CLSLogsetID)
	setDefaultEnv(env, "MOOX_CLS_TOPIC_ID", firstNonEmpty(opts.CLSTopicID, os.Getenv("MOOX_CLS_TOPIC_ID")))
	setDefaultEnv(env, "MOOX_CLS_TIMEOUT_MS", strconv.Itoa(setupconfig.SCFCLSReserveMilliseconds))
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
		"MOOX_GATEWAY_CA_PEM_B64":           {},
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
		"MOOX_MARKET_FETCH_MODE":            {},
		"MOOX_CLS_SECRET_ID":                {},
		"MOOX_CLS_SECRET_KEY":               {},
		"MOOX_CLS_ENABLED":                  {},
		"MOOX_CLS_ENDPOINT":                 {},
		"MOOX_CLS_LOGSET_ID":                {},
		"MOOX_CLS_TOPIC_ID":                 {},
		"MOOX_CLS_TIMEOUT_MS":               {},
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
	// Timer market-fetcher SCFs are self-contained and do not need EventBus.
	// Invoke market-fetchers publish their completion event so the scheduler
	// can advance the batch, therefore they retain NATS materials but not the
	// unrelated HTTPS gateway certificates (the 4KB SCF environment limit is
	// otherwise exceeded by the two RSA CA bundles).
	isTimer := strings.EqualFold(strings.TrimSpace(opts.TriggerType), "timer")
	isMarketFetcher := strings.EqualFold(strings.TrimSpace(opts.BizType), "market_fetcher")
	useEventBus := !isTimer
	useServiceGateway := !isTimer && !isMarketFetcher
	if packageID != "" {
		env["MOOX_CODE_PACKAGE_ID"] = packageID
	}
	if packageID != "" && useEventBus && (opts.EventBusCredential != nil || opts.EventBusCredentialFile != "") {
		credential, caPEM, err := collectorEventBusCredentialMaterial(opts)
		if err != nil {
			return nil, err
		}
		if err := validateCollectorEventBusCredential(credential); err != nil {
			return nil, err
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
	if useServiceGateway {
		caFile := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_FILE"))
		caMaterial := strings.TrimSpace(os.Getenv("MOOX_GATEWAY_CA_PEM_B64"))
		if len(opts.GatewayCAPEM) > 0 {
			caFile = ""
			caMaterial = base64.StdEncoding.EncodeToString(opts.GatewayCAPEM)
		}
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
	}
	if useServiceGateway {
		serviceCAFile := strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_CA_FILE"))
		serviceCAMaterial := strings.TrimSpace(os.Getenv("MOOX_SERVICE_GATEWAY_CA_PEM_B64"))
		if len(opts.ServiceGatewayCAPEM) > 0 {
			serviceCAFile = ""
			serviceCAMaterial = base64.StdEncoding.EncodeToString(opts.ServiceGatewayCAPEM)
		}
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

func collectorEventBusCredentialMaterial(opts collectorPublishOptions) (jetstream.CredentialFile, []byte, error) {
	if opts.EventBusCredential != nil {
		if len(opts.EventBusCAPEM) == 0 {
			return jetstream.CredentialFile{}, nil, fmt.Errorf("control EventBus publisher credential requires CA material")
		}
		return *opts.EventBusCredential, append([]byte(nil), opts.EventBusCAPEM...), nil
	}
	if opts.EventBusCredentialFile == "" {
		return jetstream.CredentialFile{}, nil, fmt.Errorf("market-fetch-publisher EventBus credential is required")
	}
	credentialPath := jetstream.ExpandCredentialPath(opts.EventBusCredentialFile)
	credential, err := jetstream.LoadCredentialFile(credentialPath)
	if err != nil {
		return jetstream.CredentialFile{}, nil, err
	}
	caPath := credential.CAFile
	if caPath == "" {
		return jetstream.CredentialFile{}, nil, fmt.Errorf("market-fetch-publisher credential requires ca_file")
	}
	if !filepath.IsAbs(caPath) {
		caPath = filepath.Join(filepath.Dir(credentialPath), caPath)
	}
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return jetstream.CredentialFile{}, nil, fmt.Errorf("read EventBus CA file: %w", err)
	}
	return credential, caPEM, nil
}

// preflightCollectorSCFEventBusCredential validates the trust material loaded
// from the control host and rewrites its endpoint from the deployment manifest.
// Control-plane credentials intentionally point at the local EventBus listener;
// that address is valid for host services but cannot be used by an SCF function.
func preflightCollectorSCFEventBusCredential(
	credential jetstream.CredentialFile,
	caPEM []byte,
	eventBus setupconfig.EventBus,
) (jetstream.CredentialFile, error) {
	if err := validateCollectorEventBusCredentialShape(credential); err != nil {
		return jetstream.CredentialFile{}, fmt.Errorf("invalid control EventBus credential: %w", err)
	}
	if len(strings.TrimSpace(string(caPEM))) == 0 {
		return jetstream.CredentialFile{}, fmt.Errorf("control EventBus credential requires CA material")
	}
	if err := validateCollectorEventBusCAPEM(caPEM); err != nil {
		return jetstream.CredentialFile{}, fmt.Errorf("invalid control EventBus CA material: %w", err)
	}
	address := strings.TrimSpace(eventBus.PublicAddress)
	if address == "" || isCollectorEventBusLoopbackHost(address) {
		return jetstream.CredentialFile{}, fmt.Errorf("eventbus.public_address must be a non-loopback host for SCF")
	}
	if eventBus.Port < 1 || eventBus.Port > 65535 {
		return jetstream.CredentialFile{}, fmt.Errorf("eventbus.port must be between 1 and 65535")
	}
	if !eventBus.TLSEnabled {
		return jetstream.CredentialFile{}, fmt.Errorf("eventbus.tls_enabled must be true for SCF")
	}

	prepared := credential
	prepared.URLs = []string{"tls://" + net.JoinHostPort(address, strconv.Itoa(eventBus.Port))}
	if err := validateCollectorEventBusCredential(prepared); err != nil {
		return jetstream.CredentialFile{}, fmt.Errorf("SCF EventBus credential does not match custom.toml: %w", err)
	}
	return prepared, nil
}

// validateCollectorEventBusCAPEM rejects a placeholder or truncated CA before
// it can be embedded in every SCF function. A CertPool append alone is not
// sufficient because AppendCertsFromPEM silently returns false for malformed
// input; parse every PEM block and require at least one CA certificate.
func validateCollectorEventBusCAPEM(raw []byte) error {
	remaining := raw
	certificates := 0
	caCertificates := 0
	for {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return err
		}
		certificates++
		if cert.IsCA {
			caCertificates++
		}
	}
	if certificates == 0 {
		return fmt.Errorf("no certificates found")
	}
	if caCertificates == 0 {
		return fmt.Errorf("no CA certificate found")
	}
	if strings.TrimSpace(string(remaining)) != "" {
		return fmt.Errorf("trailing non-PEM data")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return fmt.Errorf("certificate pool is empty")
	}
	return nil
}

func validateCollectorEventBusCredentialShape(credential jetstream.CredentialFile) error {
	if len(credential.URLs) != 1 {
		return fmt.Errorf("credential must contain exactly one EventBus URL")
	}
	eventBusURL, err := url.Parse(credential.URLs[0])
	if err != nil || eventBusURL.Scheme != "tls" || eventBusURL.Hostname() == "" || eventBusURL.Port() == "" {
		return fmt.Errorf("credential URL must be tls with host and port")
	}
	if strings.TrimSpace(credential.Username) == "" || strings.TrimSpace(credential.Password) == "" {
		return fmt.Errorf("credential requires username and password")
	}
	return nil
}

func isCollectorEventBusLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

func validateCollectorEventBusCredential(credential jetstream.CredentialFile) error {
	if err := validateCollectorEventBusCredentialShape(credential); err != nil {
		return fmt.Errorf("market-fetch-publisher %w", err)
	}
	eventBusURL, _ := url.Parse(credential.URLs[0])
	if isCollectorEventBusLoopbackHost(eventBusURL.Hostname()) {
		return fmt.Errorf("SCF EventBus URL must not use a loopback host")
	}
	return nil
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

func defaultCollectorSCFFetcherSpace() *setupconfig.SCFFetcherSpace {
	return &setupconfig.SCFFetcherSpace{
		MemorySize:          64,
		TimeoutSeconds:      15,
		RealtimeBatchSize:   30,
		RealtimeBarLimit:    3,
		CatchupBatchSize:    1,
		CatchupBarLimit:     1000,
		MaxInflightRequests: 10,
		RequestTimeoutMS:    1000,
		HTTPMaxAttempts:     4,
		StorageMaxAttempts:  3,
		StorageTimeoutMS:    5000,
		MaxRetryAttempts:    3,
	}
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
func validateCollectorRuntimeConfig(values map[string]string, fetcher *setupconfig.SCFFetcherSpace, timer bool, timeoutSeconds int) error {
	if fetcher == nil {
		return fmt.Errorf("scf fetcher configuration is required")
	}
	batchSize, err := collectorRuntimeConfigInt(values, "realtime_batch_size", defaultInt(fetcher.RealtimeBatchSize, 30), 1)
	if err != nil {
		return err
	}
	if batchSize < 1 || batchSize > 30 {
		return fmt.Errorf("market_fetcher realtime_batch_size must be between 1 and 30")
	}
	inflight, err := collectorRuntimeConfigInt(values, "max_inflight_requests", defaultInt(fetcher.MaxInflightRequests, 10), 1)
	if err != nil {
		return err
	}
	if inflight < 1 || inflight > 64 {
		return fmt.Errorf("market_fetcher max_inflight_requests must be between 1 and 64")
	}
	requestTimeoutMS, err := collectorRuntimeConfigInt(values, "request_timeout_ms", defaultInt(fetcher.RequestTimeoutMS, 1000), 1)
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
	reserve := setupconfig.SCFCLSReserveMilliseconds + setupconfig.SCFFinalResponseReserveMilliseconds
	if !timer {
		reserve += setupconfig.SCFCompletionReserveMilliseconds
	}
	if requestWaves*requestTimeoutMS+storageTimeoutMS+reserve >= timeoutSeconds*1000 {
		return fmt.Errorf("market_fetcher realtime request waves + storage_timeout_ms + configured reserves must be less than the %d-second timeout", timeoutSeconds)
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
	// A full stock Instrument snapshot can take longer than the ordinary
	// control-plane request budget while the SCF invokes public providers.
	// Keep the client bounded, but do not cancel a healthy invocation at 45s.
	client.HTTPClient = &http.Client{Timeout: 2 * time.Minute}
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

func buildCollectorLinuxBinary(ctx context.Context, collectorRoot, outPath, version, entrypoint string) error {
	if entrypoint != "crypto_market" && entrypoint != "stock_cn" {
		return fmt.Errorf("unsupported collector SCF entrypoint %q", entrypoint)
	}
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", fmt.Sprintf("-s -w -X main.Version=%s", version), "-o", outPath, "./cmd/scf/"+entrypoint)
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
		if _, err := os.Stat(filepath.Join(candidate, "cmd", "scf", "crypto_market", "main.go")); err == nil {
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
