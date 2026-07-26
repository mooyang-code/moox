package reporter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/avast/retry-go"
	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/sources"
	"github.com/mooyang-code/moox/modules/collector/internal/sources/binance"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go/log"
)

// ServerResponse 服务端响应结构
type ServerResponse struct {
	RetInfo *ServerRetInfo `json:"ret_info"`
}

// ServerRetInfo 对应 common.RetInfo。
type ServerRetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// ScheduledHeartbeat 框架定时器入口函数 - 定时心跳
func ScheduledHeartbeat(ctx context.Context, _ string) error {
	nodeID, version := runtimeapp.GetNodeInfo()
	log.WithContextFields(ctx, "func", "ScheduledHeartbeat", "version", version, "nodeID", nodeID)

	log.DebugContextf(ctx, "ScheduledHeartbeat Enter")
	if err := ReportHeartbeat(ctx); err != nil {
		log.ErrorContextf(ctx, "scheduled heartbeat failed: %v", err)
		return err
	}
	log.DebugContextf(ctx, "ScheduledHeartbeat Success")
	return nil
}

// ReportHeartbeat 发送心跳上报服务端
func ReportHeartbeat(ctx context.Context) error {
	serviceGatewayTarget := runtimeapp.GetServiceGatewayTarget()
	nodeID, localVersion := runtimeapp.GetNodeInfo()
	log.DebugContextf(ctx, "ReportHeartbeat 开始: service_gateway_target=%s, nodeID=%s, version=%s", serviceGatewayTarget, nodeID, localVersion)

	// 检查NodeID是否配置
	if nodeID == "" {
		log.WarnContextf(ctx, "NodeID 为空，跳过心跳上报。请确保服务端探测请求已触发 ProcessProbe")
		return nil
	}
	if serviceGatewayTarget == "" {
		log.WarnContextf(ctx, "service gateway target 未配置，跳过心跳上报")
		return nil
	}

	// 构建本节点负载信息
	payload, err := buildPayloadInfo()
	if err != nil {
		log.ErrorContextf(ctx, "failed to build heartbeat payload: %v", err)
		return fmt.Errorf("failed to build heartbeat payload: %w", err)
	}

	if err := sendToServer(ctx, payload, serviceGatewayTarget); err != nil {
		log.ErrorContextf(ctx, "failed to send heartbeat: %v", err)
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}
	return nil
}

// ProcessProbe 处理心跳探测请求【服务端来的探测请求】
func ProcessProbe(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	currentNodeID, currentVersion := runtimeapp.GetNodeInfo()
	nodeID := currentNodeID
	if event.Data != nil {
		if value, ok := event.Data["node_id"].(string); ok && strings.TrimSpace(value) != "" {
			nodeID = strings.TrimSpace(value)
		}
	}
	funcCtx, hasFunctionContext := functioncontext.FromContext(ctx)
	if nodeID == "" && hasFunctionContext {
		nodeID = strings.TrimSpace(funcCtx.FunctionName)
	}
	if nodeID != "" {
		runtimeapp.UpdateNodeInfo(nodeID, currentVersion)
		log.WithContextFields(ctx, "func", "ProcessProbe", "version", currentVersion, "nodeID", nodeID)
	} else {
		log.WarnContextf(ctx, "[ProcessProbe] 无法确定节点 ID, has_function_context=%v", hasFunctionContext)
	}

	// 更新 service gateway 配置（用于本节点主动上报心跳和拉取任务）。
	log.DebugContextf(ctx, "[ProcessProbe] event.ServiceGatewayTarget=%s", event.ServiceGatewayTarget)
	if event.ServiceGatewayTarget != "" {
		log.DebugContextf(ctx, "[ProcessProbe] 更新 service gateway target %s", event.ServiceGatewayTarget)
		runtimeapp.UpdateServiceGatewayTarget(event.ServiceGatewayTarget)
	} else {
		log.WarnContextf(ctx, "[ProcessProbe] service gateway target 缺失")
	}

	// 构建响应数据
	probeResponse, err := buildProbeResponse()
	if err != nil {
		return &model.Response{
			Success: false,
			Message: fmt.Sprintf("failed to build response: %v", err),
		}, nil
	}

	return &model.Response{
		Success:   true,
		Message:   "probe handled successfully",
		Data:      probeResponse,
		Timestamp: time.Now(),
	}, nil
}

func buildPayloadInfo() (*model.HeartbeatPayload, error) {
	// 从全局配置获取节点信息
	nodeID, version := runtimeapp.GetNodeInfo()

	// 获取节点指标
	nodeMetrics := collectNodeMetrics()

	// 获取已注册的采集器数据类型
	supportedCollectors := sources.GetRegistry().GetDataTypes()

	// 获取本地解析的 DNS 记录（用于心跳上报）
	localDNSRecords := buildLocalDNSRecords()

	// 构建心跳负载
	payload := &model.HeartbeatPayload{
		SpaceID:             heartbeatSpaceID(),
		NodeID:              nodeID,
		NodeType:            "scf",
		Timestamp:           time.Now(),
		RunningTasks:        []*model.TaskSummary{},
		Metrics:             nodeMetrics,
		SupportedCollectors: supportedCollectors,
		LocalDNSRecords:     localDNSRecords,
		Metadata: map[string]interface{}{
			"version":    version,
			"go_version": runtime.Version(),
			"os":         runtime.GOOS,
			"arch":       runtime.GOARCH,
		},
	}
	return payload, nil
}

func heartbeatSpaceID() string {
	if value := strings.TrimSpace(os.Getenv("MOOX_SPACE_ID")); value != "" {
		return value
	}
	binding, err := binance.ResolveStorageBinding(binance.InstTypeSPOT)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(binding.SpaceID)
}

func collectNodeMetrics() *model.NodeMetrics {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	return &model.NodeMetrics{
		CPUUsage:    0,
		MemoryUsage: float64(memStats.Alloc) / 1024 / 1024, // MB
		TaskCount:   0,
		SuccessRate: 100,
		ErrorCount:  0,
		Timestamp:   time.Now(),
	}
}

func sendToServer(ctx context.Context, payload *model.HeartbeatPayload, serviceGatewayTarget string) error {
	log.DebugContextf(ctx, "sending heartbeat, node_id: %s", payload.NodeID)
	// 检查必要参数
	if serviceGatewayTarget == "" {
		return fmt.Errorf("invalid service gateway target")
	}

	if err := executeReport(ctx, payload, serviceGatewayTarget); err != nil {
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}
	return nil
}

// executeReport 准备并发送心跳请求
func executeReport(ctx context.Context, payload *model.HeartbeatPayload, serviceGatewayTarget string) error {
	url := runtimeapp.ServiceURL(serviceGatewayTarget, "cloudnode", "ReportHeartbeat")

	// 构建请求体
	apiPayload := map[string]interface{}{
		"space_id":            payload.SpaceID,
		"node_id":             payload.NodeID,
		"node_type":           payload.NodeType,
		"metadata":            payload.Metadata,
		"supported_workloads": payload.SupportedCollectors,
	}

	log.DebugContextf(ctx, "[Heartbeat] 发送心跳: nodeID=%s", payload.NodeID)

	// 序列化请求数据
	data, err := json.Marshal(apiPayload)
	if err != nil {
		return fmt.Errorf("failed to marshal heartbeat payload: %w", err)
	}

	// 创建HTTP客户端
	httpClient, err := runtimeapp.NewGatewayHTTPClient(5*time.Second, runtimeapp.DefaultAuthConfig())
	if err != nil {
		return err
	}

	// 使用重试机制发送请求
	return retry.Do(
		func() error {
			return sendSingleHeartbeat(ctx, url, data, httpClient)
		},
		retry.Attempts(5),
		retry.Delay(1*time.Second),
		retry.DelayType(retry.BackOffDelay),
		retry.LastErrorOnly(true),
		retry.OnRetry(func(n uint, err error) {
			log.WarnContextf(ctx, "retrying heartbeat request, attempt: %d, error: %v", n+1, err)
		}),
		retry.Context(ctx),
	)
}

// sendSingleHeartbeat 发送单次心跳请求
func sendSingleHeartbeat(ctx context.Context, url string, data []byte, httpClient *http.Client) error {
	// 创建HTTP请求
	req, err := runtimeapp.NewSignedRequestWithContext(ctx, "POST", url, data, runtimeapp.DefaultAuthConfig())
	if err != nil {
		return fmt.Errorf("failed to create heartbeat request: %w", err)
	}

	// 发送请求并检查错误
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("heartbeat request failed with status: %d, response: %s", resp.StatusCode, string(respData))
	}
	log.DebugContextf(ctx, "heartbeat sent successfully, status: %d", resp.StatusCode)

	// 读取和解析响应
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if parseErr := parseServerResponse(respData); parseErr != nil {
		log.WarnContextf(ctx, "failed to parse server response: %v", parseErr)
		return nil // 不影响心跳上报，只记录警告
	}
	return nil
}

// parseServerResponse 解析服务端响应。
// CloudNode JobItem terminal state is reported separately, so heartbeat no longer carries task_instances.
func parseServerResponse(respData []byte) error {
	// 1. 解析响应体
	var serverResp ServerResponse
	if err := json.Unmarshal(respData, &serverResp); err != nil {
		return fmt.Errorf("failed to parse server response: %w", err)
	}

	// 2. 检查 ret_info.code（0=SUCCESS）
	if serverResp.RetInfo == nil || serverResp.RetInfo.Code != 0 {
		code := -1
		msg := ""
		if serverResp.RetInfo != nil {
			code = serverResp.RetInfo.Code
			msg = serverResp.RetInfo.Msg
		}
		return fmt.Errorf("server returned error code: %d, message: %s", code, msg)
	}
	return nil
}

// BuildProbeResponseOptions 构建探测响应的选项
type BuildProbeResponseOptions struct {
	Config       *ProbeResponseConfig
	IncludeTasks bool
	CustomState  string
}

// BuildProbeResponseOption 构建选项函数类型
type BuildProbeResponseOption func(*BuildProbeResponseOptions)

// buildProbeResponse 构建心跳探测响应
func buildProbeResponse(options ...BuildProbeResponseOption) (*model.ProbeResponse, error) {
	// 1. 解析配置选项
	opts := &BuildProbeResponseOptions{
		Config: DefaultProbeResponseConfig(),
	}
	for _, option := range options {
		option(opts)
	}

	// 2. 获取节点信息
	nodeID, version, err := getNodeInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to get node info: %w", err)
	}

	// 3. 获取系统信息
	systemInfo, err := getSystemInfo()
	if err != nil {
		return nil, fmt.Errorf("failed to get system info: %w", err)
	}

	// 4. 获取节点指标
	nodeMetrics, err := getNodeMetrics()
	if err != nil {
		return nil, fmt.Errorf("failed to get node metrics: %w", err)
	}

	// 5. 确定节点状态
	nodeState := determineNodeState(opts.CustomState, opts.Config.State)

	// 6. 构建运行任务信息
	var runningTasks []*model.TaskSummary
	if opts.IncludeTasks {
		runningTasks = getRunningTasks()
	}

	// 7. 获取心跳统计信息
	heartbeatInfo := getHeartbeatInfo(opts.Config)

	// 8. 构建完整的探测响应
	probeResponse := &model.ProbeResponse{
		NodeID:    nodeID,
		State:     nodeState,
		Timestamp: time.Now(),
		Details: model.ProbeDetails{
			NodeInfo:      createNodeInfo(nodeID, version),
			RunningTasks:  runningTasks,
			TaskStats:     getTaskStatistics(),
			Metrics:       nodeMetrics,
			SystemInfo:    systemInfo,
			HeartbeatInfo: heartbeatInfo,
		},
	}
	return probeResponse, nil
}

// ProbeResponseConfig 探测响应配置
type ProbeResponseConfig struct {
	State       string
	Interval    string
	ReportCount int64
	ErrorCount  int64
}

// DefaultProbeResponseConfig 默认探测响应配置
func DefaultProbeResponseConfig() *ProbeResponseConfig {
	return &ProbeResponseConfig{
		State:       "running",
		Interval:    "30s",
		ReportCount: 0,
		ErrorCount:  0,
	}
}

// getNodeInfo 获取节点信息
func getNodeInfo() (nodeID, version string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic while getting node info: %v", r)
		}
	}()

	nodeID, version = runtimeapp.GetNodeInfo()
	if nodeID == "" {
		return "", "", fmt.Errorf("node ID is empty")
	}
	return nodeID, version, nil
}

// getSystemInfo 获取系统信息
func getSystemInfo() (model.SystemInfo, error) {
	return model.SystemInfo{
		GoVersion:    runtime.Version(),
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		NumCPU:       runtime.NumCPU(),
		NumGoroutine: runtime.NumGoroutine(),
	}, nil
}

// getNodeMetrics 获取节点指标
func getNodeMetrics() (*model.NodeMetrics, error) {
	return collectNodeMetrics(), nil
}

// determineNodeState 确定节点状态
func determineNodeState(customState, defaultState string) string {
	if customState != "" {
		return customState
	}
	return defaultState
}

// getRunningTasks 获取运行任务。
// 当前云函数运行时只通过 cloudnode/collector 协议拉取任务并上报结果，
// 不维护本地运行任务快照。
func getRunningTasks() []*model.TaskSummary {
	return []*model.TaskSummary{}
}

// getTaskStatistics 获取任务统计信息。
// 当前云函数运行时不维护本地任务统计，任务状态以 cloudnode/collector 为准。
func getTaskStatistics() model.TaskStatsInfo {
	return model.TaskStatsInfo{
		Total:   0,
		Running: 0,
		Pending: 0,
		Stopped: 0,
		Error:   0,
	}
}

// getHeartbeatInfo 获取心跳统计信息
func getHeartbeatInfo(probeConfig *ProbeResponseConfig) model.HeartbeatInfo {
	// 从全局配置获取服务器信息
	serviceGatewayTarget := runtimeapp.GetServiceGatewayTarget()

	return model.HeartbeatInfo{
		LastReport:           time.Now(),
		ReportCount:          probeConfig.ReportCount,
		ErrorCount:           probeConfig.ErrorCount,
		Interval:             probeConfig.Interval,
		ServiceGatewayTarget: serviceGatewayTarget,
	}
}

// createNodeInfo 创建节点信息
func createNodeInfo(nodeID, version string) *model.NodeInfo {
	return &model.NodeInfo{
		NodeID:       nodeID,
		NodeType:     "scf",
		Version:      version,
		RunningTasks: make([]string, 0),
		Capabilities: []model.CollectorType{
			model.CollectorTypeBinance,
		},
		Metadata: map[string]string{
			"go_version": runtime.Version(),
			"os":         runtime.GOOS,
			"arch":       runtime.GOARCH,
		},
	}
}

// buildLocalDNSRecords 构建 DNS 解析记录（用于心跳上报）
func buildLocalDNSRecords() []*model.LocalDNSReportItem {
	// 从 dnsproxy 模块获取所有 DNS 记录
	allRecords := httpclient.GetAllDNSRecords()
	if len(allRecords) == 0 {
		return nil
	}

	// 转换为心跳上报格式
	reportItems := make([]*model.LocalDNSReportItem, 0, len(allRecords))
	for domain, record := range allRecords {
		// 提取可用的 IP 列表
		availableIPs := make([]string, 0)
		for _, ipInfo := range record.IPList {
			if ipInfo.Available {
				availableIPs = append(availableIPs, ipInfo.IP)
			}
		}

		// 如果没有可用 IP，跳过
		if len(availableIPs) == 0 {
			continue
		}

		// 创建上报项
		reportItems = append(reportItems, &model.LocalDNSReportItem{
			Domain:    domain,
			IPList:    availableIPs,
			ResolveAt: record.ResolveAt,
		})
	}

	return reportItems
}
