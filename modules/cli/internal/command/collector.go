package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	"github.com/mooyang-code/moox/modules/cli/internal/collectorpackager"
	metricsreport "github.com/mooyang-code/moox/packages/report"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/spf13/cobra"
)

const (
	// Manifest deployments may spread a stockcn fleet across multiple regions;
	// keep the ad-hoc guard above one normal 200-function release while the
	// per-region platform limit remains enforced by setup validation.
	maxCollectorPublishNodeCount    = 1000
	collectorRuntimeConfigBatchSize = 100
	collectorMaxNodeBatchItems      = 100
)

type collectorPackageOptions struct {
	CollectorRoot    string
	SpaceID          string
	PackageConfigDir string
	Version          string
	Out              string
	ConfigDir        string
	Entrypoint       string
	CLSLogsetID      string
	CLSTopicID       string
	EventBusCAPEM    []byte
	EventBusCAFile   string
}

type collectorDeleteOptions struct {
	ControlURL       string
	AccessToken      string
	ServiceAccessKey string
	ServiceSecretKey string
	File             string
	SpaceID          string
	Namespace        string
	NodePrefix       string
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

var collectorDeleteFlags collectorDeleteOptions
var collectorProbeFlags collectorProbeOptions

var collectorFunctionDeleteCmd = &cobra.Command{
	Use:   "delete",
	Short: "异步删除当前 space 中全部 SCF 云函数",
	Long: "列出当前 space 的 scf-event 节点，并通过 CloudNode 提交可恢复查询的异步删除任务；可按命名空间和地域筛选。\n" +
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

type collectorDeleteSummary struct {
	SpaceID       string                            `json:"space_id"`
	Namespace     string                            `json:"namespace"`
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

func init() {
	rootCmd.AddCommand(collectorCmd)
	collectorCmd.AddCommand(collectorFunctionCmd)
	collectorFunctionCmd.AddCommand(collectorFunctionPackageCmd, collectorFunctionDeleteCmd, collectorFunctionProbeCmd)

	addCollectorPackageFlags(collectorFunctionPackageCmd, &collectorPackageFlags)

	collectorFunctionPackageCmd.Flags().StringVar(&collectorPackageFlags.SpaceID, "space-id", "", "space id; selects the matching SCF config directory")

	deleteFlags := collectorFunctionDeleteCmd.Flags()
	deleteFlags.StringVar(&collectorDeleteFlags.ControlURL, "control-url", "", "Control service base URL")
	deleteFlags.StringVar(&collectorDeleteFlags.AccessToken, "access-token", "", "Control access token; defaults to MOOX_ACCESS_TOKEN")
	deleteFlags.StringVar(&collectorDeleteFlags.ServiceAccessKey, "service-access-key", "", "后台服务签名鉴权 key_id; 默认取 MOOX_GATEWAY_SERVICE_KEY_ID")
	deleteFlags.StringVar(&collectorDeleteFlags.ServiceSecretKey, "service-secret-key", "", "后台服务签名鉴权 secret_key; 默认取 MOOX_GATEWAY_SERVICE_SECRET_KEY")
	deleteFlags.StringVar(&collectorDeleteFlags.File, "file", "", "optional moox.toml; enables manifest-backed service auth/CA")
	deleteFlags.StringVar(&collectorDeleteFlags.SpaceID, "space-id", "", "space id; 默认取 MOOX_SPACE_ID")
	deleteFlags.StringVar(&collectorDeleteFlags.Namespace, "namespace", "", "只删除指定 SCF 命名空间中的节点")
	deleteFlags.StringVar(&collectorDeleteFlags.NodePrefix, "node-prefix", "", "只删除节点 ID 以该前缀开头的 SCF 节点")
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
	probeFlags.StringVar(&collectorProbeFlags.File, "file", "", "optional moox.toml; stockcn uses it only to report the configured Timer count")

}

func addCollectorPackageFlags(cmd *cobra.Command, opts *collectorPackageOptions) {
	cmd.Flags().StringVar(&opts.CollectorRoot, "collector-root", "", "collector module root")
	cmd.Flags().StringVar(&opts.Version, "version", "dev", "collector package version")
	cmd.Flags().StringVar(&opts.Out, "out", "", "output zip path")
	cmd.Flags().StringVar(&opts.ConfigDir, "config", "", "collector config directory")
	cmd.Flags().StringVar(&opts.CLSTopicID, "cls-topic-id", "", "central CLS topic id used by SCF warning/error logs")
	cmd.Flags().StringVar(&opts.EventBusCAFile, "eventbus-ca-file", "", "public EventBus CA PEM to package (no credentials)")
}

func packageCollectorFunction(ctx context.Context, opts collectorPackageOptions) (*collectorpackager.BuildSCFPackageResult, error) {
	if len(opts.EventBusCAPEM) == 0 && strings.TrimSpace(opts.EventBusCAFile) != "" {
		var err error
		opts.EventBusCAPEM, err = os.ReadFile(opts.EventBusCAFile)
		if err != nil {
			return nil, fmt.Errorf("read public EventBus CA file: %w", err)
		}
	}
	// EventBus CA 可选：采集计划函数不连 EventBus，只有旧链路的包才携带。
	if len(opts.EventBusCAPEM) > 0 {
		if err := collectorpackager.ValidateEventBusCAPEM(opts.EventBusCAPEM); err != nil {
			return nil, fmt.Errorf("public EventBus CA is invalid: %w", err)
		}
	}
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
		configDir = filepath.Join(collectorRoot, "configs", filepath.FromSlash(defaultFlag(opts.PackageConfigDir, defaultCollectorPackageConfigDir(spaceID))))
	}
	binaryPath := filepath.Join(os.TempDir(), fmt.Sprintf("moox-collector-scf-%d", time.Now().UnixNano()), "main")
	if err := os.MkdirAll(filepath.Dir(binaryPath), 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(binaryPath))

	entrypoint := defaultFlag(opts.Entrypoint, "market_data")
	if err := buildCollectorLinuxBinary(ctx, collectorRoot, binaryPath, version, entrypoint); err != nil {
		return nil, err
	}
	return collectorpackager.BuildSCFPackage(collectorpackager.BuildSCFPackageOptions{
		BinaryPath:    binaryPath,
		ConfigDir:     configDir,
		OutPath:       outPath,
		EventBusCAPEM: opts.EventBusCAPEM,
	})
}

func defaultCollectorPackageConfigDir(spaceID string) string {
	spaceID = strings.ToLower(strings.TrimSpace(spaceID))
	if spaceID == "crypto" {
		return filepath.ToSlash(filepath.Join("scf", "market_data"))
	}
	return filepath.ToSlash(filepath.Join("scf", spaceID))
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

func collectorUnknownBatchOutcome(message string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", errCollectorBatchOutcomeUnknown, message)
	}
	return errors.Join(errCollectorBatchOutcomeUnknown, fmt.Errorf("%s: %w", message, cause))
}

func waitCollectorBatchTerminalChange(ctx context.Context, client *adminclient.Client, jobID string) (*adminclient.NodeBatchChangeResponse, error) {
	if strings.TrimSpace(jobID) == "" {
		return nil, nil
	}
	const statusRequestTimeout = 30 * time.Second
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		var change *adminclient.NodeBatchChangeResponse
		var lastErr error
		for attempt := 0; attempt < 3; attempt++ {
			statusCtx, statusCancel := context.WithTimeout(ctx, statusRequestTimeout)
			candidate, err := client.GetNodeBatchChange(statusCtx, jobID)
			statusCancel()
			if err == nil {
				change = candidate
				lastErr = nil
				break
			}
			lastErr = err
			select {
			case <-ctx.Done():
				return nil, collectorUnknownBatchOutcome(fmt.Sprintf("wait batch %s", jobID), ctx.Err())
			case <-time.After(time.Second):
			}
		}
		if lastErr != nil {
			return nil, collectorUnknownBatchOutcome(fmt.Sprintf("GetNodeBatchChange for batch %s", jobID), lastErr)
		}
		if change == nil || change.Job == nil {
			return nil, collectorUnknownBatchOutcome(fmt.Sprintf("batch %s returned empty status", jobID), nil)
		}
		switch strings.ToUpper(strings.TrimSpace(change.Job.Status)) {
		case "NODE_BATCH_STATUS_SUCCESS", "SUCCESS", "SUCCEEDED":
			return change, nil
		case "NODE_BATCH_STATUS_FAILED", "FAILED", "NODE_BATCH_STATUS_PARTIAL", "PARTIAL":
			return change, nil
		}
		select {
		case <-ctx.Done():
			return nil, collectorUnknownBatchOutcome(fmt.Sprintf("wait batch %s", jobID), ctx.Err())
		case <-ticker.C:
		}
	}
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

func deleteCollectorFunctions(ctx context.Context, opts collectorDeleteOptions) (summaryResult *collectorDeleteSummary, retErr error) {
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
	if !opts.DryRun && !opts.Wait {
		return nil, fmt.Errorf("market_fetcher deletion requires --wait so the publish lease covers the asynchronous CloudNode batch")
	}

	gateway, err := openCommandGateway(ctx, opts.File, nil)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway, SpaceID: spaceID}
	namespace := strings.TrimSpace(opts.Namespace)
	region := strings.TrimSpace(opts.Region)
	if opts.DryRun {
		nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
			NodeType: "scf-event",
			BizType:  "market_fetcher",
			Region:   region,
		})
		if err != nil {
			return nil, fmt.Errorf("list active SCF nodes: %w", err)
		}
		nodeIDs := collectorDeleteTargetNodeIDs(nodes, namespace, strings.TrimSpace(opts.NodePrefix))
		summary := collectorDeleteResult(spaceID, namespace, region, true, nodeIDs)
		if len(nodeIDs) == 0 {
			summary.Status = "nothing_to_delete"
		} else {
			summary.Status = "dry_run"
		}
		return summary, nil
	}
	leaseCtx, leaseGuard, err := acquireCollectorPublishLease(ctx, client, spaceID)
	if err != nil {
		return nil, fmt.Errorf("acquire collector publish lease before deletion: %w", err)
	}
	accepted, terminal := false, false
	defer func() {
		if accepted && !terminal {
			leaseGuard.Abandon()
			return
		}
		retErr = errors.Join(retErr, leaseGuard.Close())
	}()
	ctx = leaseCtx
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{
		NodeType: "scf-event",
		BizType:  "market_fetcher",
		Region:   region,
	})
	if err != nil {
		return nil, fmt.Errorf("list active SCF nodes under publish lease: %w", err)
	}
	nodeIDs := collectorDeleteTargetNodeIDs(nodes, namespace, strings.TrimSpace(opts.NodePrefix))
	summary := collectorDeleteResult(spaceID, namespace, region, false, nodeIDs)
	if len(nodeIDs) == 0 {
		summary.Status = "nothing_to_delete"
		return summary, nil
	}

	jobIDs := make([]string, 0, (len(nodeIDs)+collectorMaxNodeBatchItems-1)/collectorMaxNodeBatchItems)
	var submitErr error
	unknownSubmission := false
	for offset := 0; offset < len(nodeIDs); offset += collectorMaxNodeBatchItems {
		end := min(offset+collectorMaxNodeBatchItems, len(nodeIDs))
		resp, submitErrForBatch := client.SubmitDeleteNodes(ctx, nodeIDs[offset:end])
		if submitErrForBatch != nil {
			submitErr = fmt.Errorf("submit SCF deletion batch for nodes %d-%d: %w", offset+1, end, submitErrForBatch)
			unknownSubmission = isAmbiguousCollectorPublishOutcome(submitErrForBatch)
			if unknownSubmission {
				accepted = true
			}
			break
		}
		if resp == nil || strings.TrimSpace(resp.JobID) == "" {
			submitErr = collectorUnknownBatchOutcome("SCF deletion batch returned no job id", nil)
			unknownSubmission = true
			accepted = true
			break
		}
		accepted = true
		jobIDs = append(jobIDs, resp.JobID)
	}
	summary.JobID = strings.Join(jobIDs, ",")
	summary.Operation = "delete_nodes"
	summary.Status = ""
	allTerminal := !unknownSubmission
	var waitErrs []error
	for _, jobID := range jobIDs {
		change, pollErr := waitCollectorBatchTerminalChange(ctx, client, jobID)
		if pollErr != nil {
			allTerminal = false
			waitErrs = append(waitErrs, fmt.Errorf("poll SCF deletion batch %s: %w", jobID, pollErr))
			continue
		}
		if change == nil || change.Job == nil {
			allTerminal = false
			waitErrs = append(waitErrs, collectorUnknownBatchOutcome(fmt.Sprintf("SCF deletion batch %s returned empty terminal status", jobID), nil))
			continue
		}
		summary.Status = mergeBatchStatus(summary.Status, change.Job.Status)
		summary.SuccessCount += change.Job.SuccessCount
		summary.FailedCount += change.Job.FailedCount
		for _, item := range change.Items {
			if item.Status != "NODE_BATCH_ITEM_STATUS_SUCCESS" {
				summary.FailedResults = append(summary.FailedResults, item)
			}
		}
		if strings.Contains(change.Job.Status, "FAILED") || strings.Contains(change.Job.Status, "PARTIAL") {
			waitErrs = append(waitErrs, fmt.Errorf("SCF deletion batch %s finished with status %s", jobID, change.Job.Status))
		}
	}
	terminal = allTerminal
	if unknownSubmission {
		waitErrs = append(waitErrs, collectorUnknownBatchOutcome("SCF deletion submission outcome is unknown", submitErr))
	} else if submitErr != nil {
		waitErrs = append(waitErrs, submitErr)
	}
	if summary.Status == "" {
		switch {
		case unknownSubmission:
			summary.Status = "unknown"
		case submitErr != nil:
			summary.Status = "rejected"
		default:
			summary.Status = "nothing_to_delete"
		}
	}
	retErr = errors.Join(waitErrs...)
	return summary, retErr
}

func collectorDeleteTargetNodeIDs(nodes []adminclient.CloudNode, namespace, nodePrefix string) []string {
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		if node.IsDeleted || strings.TrimSpace(node.NodeID) == "" || (namespace != "" && strings.TrimSpace(node.Namespace) != namespace) || (nodePrefix != "" && !strings.HasPrefix(node.NodeID, nodePrefix)) {
			continue
		}
		nodeIDs = append(nodeIDs, node.NodeID)
	}
	return nodeIDs
}

func collectorDeleteResult(spaceID, namespace, region string, dryRun bool, nodeIDs []string) *collectorDeleteSummary {
	return &collectorDeleteSummary{
		SpaceID:    spaceID,
		Namespace:  namespace,
		Region:     region,
		NodeType:   "scf-event",
		DryRun:     dryRun,
		TotalCount: len(nodeIDs),
		NodeIDs:    nodeIDs,
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
	spaceID := defaultFlag(opts.SpaceID, os.Getenv("MOOX_SPACE_ID"))
	if spaceID == "" {
		return nil, fmt.Errorf("--space-id is required")
	}
	gateway, err := openCommandGateway(ctx, opts.File, nil)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway, SpaceID: spaceID}
	nodes, err := client.ListCloudNodes(ctx, adminclient.CloudNodeListFilter{Region: opts.Region, NodeType: "scf-event", BizType: "market_fetcher"})
	if err != nil {
		return nil, err
	}
	stockCN := strings.EqualFold(spaceID, "stockcn")
	expectedCount := 0
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
				provider, marketType = "sina", "stockcn"
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

func selectCollectorProbeNodes(nodes []adminclient.CloudNode, timerOnly bool) []adminclient.CloudNode {
	eligible := make([]adminclient.CloudNode, 0, len(nodes))
	for _, node := range nodes {
		if !collectorProbeNodeEligible(node) {
			continue
		}
		if timerOnly && !strings.EqualFold(strings.TrimSpace(node.TriggerType), "timer") {
			continue
		}
		eligible = append(eligible, node)
	}
	return eligible
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
		functions.WithLabelValues("stockcn", "stockcn_equity_kline_1m_v4", kind).Set(float64(max(value, 0)))
	}
	cfg := metricsreport.DefaultConfig("collector", "moox-cli")
	cfg.SpaceID = "stockcn"
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

func buildCollectorLinuxBinary(ctx context.Context, collectorRoot, outPath, version, entrypoint string) error {
	if entrypoint != "market_data" {
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
		if _, err := os.Stat(filepath.Join(candidate, "cmd", "scf", "market_data", "main.go")); err == nil {
			return filepath.Abs(candidate)
		}
	}
	return "", fmt.Errorf("collector root not found; pass --collector-root")
}
