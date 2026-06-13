package heartbeat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"time"

	"github.com/avast/retry-go"
	"github.com/mooyang-code/moox/modules/collector/internal/collector"
	"github.com/mooyang-code/moox/modules/collector/internal/dnsproxy"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"github.com/mooyang-code/moox/modules/collector/pkg/model"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// ServerResponse 服务端响应结构
type ServerResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    []any  `json:"data"` // 统一响应格式中data为数组
	Total   *int64 `json:"total,omitempty"`
}

// HeartbeatResponseData 心跳响应数据结构
type HeartbeatResponseData struct {
	PackageVersion string                 `json:"package_version"` // 包版本信息
	TaskInstances  []TaskInstanceResponse `json:"task_instances"`  // 任务实例列表
	TasksMD5       string                 `json:"tasks_md5"`       // 服务端任务MD5值
}

// TaskInstanceResponse 任务实例响应结构（复用任务缓存结构）
type TaskInstanceResponse = config.CollectorTaskInstanceCache

// ScheduledHeartbeat 框架定时器入口函数 - 定时心跳
func ScheduledHeartbeat(c context.Context, _ string) error {
	ctx := trpc.CloneContext(c)
	nodeID, version := config.GetNodeInfo()
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
	serverIP, serverPort := config.GetServerInfo()
	nodeID, localVersion := config.GetNodeInfo()
	log.DebugContextf(ctx, "ReportHeartbeat 开始: serverIP=%s:%d, nodeID=%s, version=%s", serverIP, serverPort, nodeID, localVersion)

	// 检查NodeID是否配置
	if nodeID == "" {
		log.WarnContextf(ctx, "NodeID 为空，跳过心跳上报。请确保服务端探测请求已触发 ProcessProbe")
		return nil
	}
	if serverIP == "" {
		log.WarnContextf(ctx, "服务端 IP 未配置，跳过心跳上报")
		return nil
	}

	// 构建本节点负载信息
	payload, err := buildPayloadInfo()
	if err != nil {
		log.ErrorContextf(ctx, "failed to build heartbeat payload: %v", err)
		return fmt.Errorf("failed to build heartbeat payload: %w", err)
	}

	// 发送心跳并获取包版本信息
	packageVersion, err := sendToServer(ctx, payload, serverIP, serverPort)
	if err != nil {
		log.ErrorContextf(ctx, "failed to send heartbeat: %v", err)
		return fmt.Errorf("failed to send heartbeat: %w", err)
	}

	// 检查版本一致性，如果不一致则终止服务（避免云平台同时保留多个版本节点的运行）
	if packageVersion != "" && packageVersion != localVersion {
		log.FatalContextf(ctx, "版本不一致，终止服务 - 本地版本: %s, 服务端版本: %s", localVersion, packageVersion)
	}
	return nil
}

// ProcessProbe 处理心跳探测请求【服务端来的探测请求】
func ProcessProbe(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	// 从上下文获取云函数信息，更新NodeID
	funcCtx, ok := functioncontext.FromContext(ctx)
	if ok && funcCtx.FunctionName != "" {
		currentNodeID, currentVersion := config.GetNodeInfo()
		log.WithContextFields(ctx, "func", "ProcessProbe", "version", currentVersion, "nodeID", currentNodeID)

		// 无条件更新 NodeID 为云函数名称
		config.UpdateNodeInfo(funcCtx.FunctionName, currentVersion)
		log.DebugContextf(ctx, "[ProcessProbe] NodeID 已更新为 %s", funcCtx.FunctionName)
	} else {
		log.WarnContextf(ctx, "[ProcessProbe] 无法从上下文获取云函数信息, ok=%v", ok)
	}

	// 更新服务端连接信息的配置（用于本节点 主动上报心跳和拉取配置）
	log.DebugContextf(ctx, "[ProcessProbe] event.ServerIP=%s, event.ServerPort=%d", event.ServerIP, event.ServerPort)
	if event.ServerIP != "" && event.ServerPort > 0 {
		log.DebugContextf(ctx, "[ProcessProbe] 更新服务端地址 %s:%d", event.ServerIP, event.ServerPort)
		config.UpdateServerInfo(event.ServerIP, event.ServerPort)
		// 验证更新是否成功
		verifyIP, verifyPort := config.GetServerInfo()
		log.DebugContextf(ctx, "[ProcessProbe] 验证更新后的服务端地址: %s:%d", verifyIP, verifyPort)
	} else {
		log.WarnContextf(ctx, "[ProcessProbe] 服务端地址信息缺失 ServerIP=%s, ServerPort=%d",
			event.ServerIP, event.ServerPort)
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
	nodeID, version := config.GetNodeInfo()

	// 获取节点指标
	nodeMetrics := collectNodeMetrics()

	// 获取已注册的采集器数据类型
	supportedCollectors := collector.GetRegistry().GetDataTypes()

	// 获取当前任务MD5值
	tasksMD5 := config.GetCurrentTasksMD5()

	// 获取本地解析的 DNS 记录（用于心跳上报）
	localDNSRecords := buildLocalDNSRecords()

	// 构建心跳负载
	payload := &model.HeartbeatPayload{
		NodeID:              nodeID,
		NodeType:            "scf",
		Timestamp:           time.Now(),
		RunningTasks:        []*model.TaskSummary{},
		Metrics:             nodeMetrics,
		SupportedCollectors: supportedCollectors,
		TasksMD5:            tasksMD5,
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

func sendToServer(ctx context.Context, payload *model.HeartbeatPayload, serverIP string, serverPort int) (string, error) {
	log.DebugContextf(ctx, "sending heartbeat, node_id: %s", payload.NodeID)
	// 检查必要参数
	if serverIP == "" || serverPort <= 0 {
		return "", fmt.Errorf("invalid server address: %s:%d", serverIP, serverPort)
	}

	packageVersion, err := executeReport(ctx, payload, serverIP, serverPort)
	if err != nil {
		return "", fmt.Errorf("failed to send heartbeat: %w", err)
	}
	return packageVersion, nil
}

// executeReport 准备并发送心跳请求
func executeReport(ctx context.Context, payload *model.HeartbeatPayload, serverIP string, serverPort int) (string, error) {
	url := fmt.Sprintf("http://%s:%d/gateway/cloudnode/ReportHeartbeatInner", serverIP, serverPort)

	// 构建请求体
	apiPayload := map[string]interface{}{
		"node_id":              payload.NodeID,
		"node_type":            payload.NodeType,
		"metadata":             payload.Metadata,
		"supported_collectors": payload.SupportedCollectors,
		"tasks_md5":            payload.TasksMD5,
	}

	// 记录发送的MD5值
	log.DebugContextf(ctx, "[Heartbeat] 发送心跳: nodeID=%s, tasksMD5=%s", payload.NodeID, payload.TasksMD5)

	// 序列化请求数据
	data, err := json.Marshal(apiPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal heartbeat payload: %w", err)
	}

	// 创建HTTP客户端
	httpClient := &http.Client{Timeout: 5 * time.Second}
	var packageVersion string

	// 使用重试机制发送请求
	err = retry.Do(
		func() error {
			return sendSingleHeartbeat(ctx, url, data, httpClient, &packageVersion)
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
	return packageVersion, err
}

// sendSingleHeartbeat 发送单次心跳请求
func sendSingleHeartbeat(ctx context.Context, url string, data []byte, httpClient *http.Client, packageVersion *string) error {
	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(data))
	if err != nil {
		return fmt.Errorf("failed to create heartbeat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

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

	// 解析服务端响应
	version, parseErr := parseServerResponse(ctx, respData)
	if parseErr != nil {
		log.WarnContextf(ctx, "failed to parse server response: %v", parseErr)
		return nil // 不影响心跳上报，只记录警告
	}
	*packageVersion = version
	return nil
}

// parseServerResponse 解析服务端响应，提取包版本信息和任务实例
func parseServerResponse(ctx context.Context, respData []byte) (string, error) {
	// 1. 解析响应体
	var serverResp ServerResponse
	if err := json.Unmarshal(respData, &serverResp); err != nil {
		return "", fmt.Errorf("failed to parse server response: %w", err)
	}

	// 2. 检查响应状态码（200表示成功）
	if serverResp.Code != 200 {
		return "", fmt.Errorf("server returned error code: %d, message: %s", serverResp.Code, serverResp.Message)
	}

	// 3. 检查数据数组
	if len(serverResp.Data) == 0 {
		return "", nil // 返回空版本而不是错误
	}

	// 4. 获取并验证数据元素
	dataMap, ok := serverResp.Data[0].(map[string]interface{})
	if !ok {
		return "", nil
	}

	// 5. 提取包版本
	packageVersion := extractPackageVersion(dataMap)

	// 6. 处理任务实例列表
	processTaskInstances(ctx, dataMap)

	return packageVersion, nil
}

// extractPackageVersion 提取包版本信息
func extractPackageVersion(dataMap map[string]interface{}) string {
	pv, exists := dataMap["package_version"]
	if !exists {
		return ""
	}

	versionStr, ok := pv.(string)
	if !ok {
		return ""
	}

	return versionStr
}

// processTaskInstances 处理任务实例列表
func processTaskInstances(ctx context.Context, dataMap map[string]interface{}) {
	// 1. 检查任务实例字段是否存在
	taskInstances, exists := dataMap["task_instances"]
	if !exists || taskInstances == nil {
		log.DebugContextf(ctx, "[Heartbeat] 响应中无任务实例数据")
		return
	}

	// 2. 序列化任务实例数据
	taskInstancesJSON, err := json.Marshal(taskInstances)
	if err != nil {
		log.WarnContextf(ctx, "[Heartbeat] failed to marshal task instances: %v", err)
		return
	}

	// 3. 反序列化为任务列表
	var tasks []TaskInstanceResponse
	if err := json.Unmarshal(taskInstancesJSON, &tasks); err != nil {
		log.WarnContextf(ctx, "[Heartbeat] failed to unmarshal task instances: %v", err)
		return
	}

	// 4. 检查任务列表是否为空
	if len(tasks) == 0 {
		log.DebugContextf(ctx, "[Heartbeat] 任务MD5匹配，无需更新")
		return
	}

	// 5. 更新任务实例到内存存储
	log.InfoContextf(ctx, "[Heartbeat] 收到任务实例更新，任务数: %d", len(tasks))

	// 6. 打印每个任务的详细信息
	for i, task := range tasks {
		log.InfoContextf(ctx, "[Heartbeat] Task[%d]: ID=%d, TaskID=%s, RuleID=%s, PlannedExecNode=%s, DataType=%s, Symbol=%s, Interval=%s, TaskParams=%s, Invalid=%d",
			i, task.ID, task.TaskID, task.RuleID, task.NodeID, task.DataType, task.Symbol, task.Interval, task.TaskParams, task.Invalid)
	}

	// 7. 更新任务实例
	updateTaskInstancesFromResponse(ctx, tasks)
}

// updateTaskInstancesFromResponse 从响应中更新任务实例
func updateTaskInstancesFromResponse(ctx context.Context, tasks []TaskInstanceResponse) {
	// 转换为本地任务结构
	localTasks := make([]*config.CollectorTaskInstanceCache, 0, len(tasks))
	for i := range tasks {
		localTask := tasks[i]
		// 解析任务参数
		if err := localTask.ParseTaskParams(); err != nil {
			log.WarnContextf(ctx, "[Heartbeat] Failed to parse task params for TaskID=%s: %v", localTask.TaskID, err)
		} else {
			log.InfoContextf(ctx, "[Heartbeat] Parsed task: TaskID=%s, DataType=%s, DataSource=%s, InstType=%s, Symbol=%s, Intervals=%v",
				localTask.TaskID, localTask.DataType, localTask.DataSource, localTask.InstType, localTask.Symbol, localTask.Intervals)
		}
		localTasks = append(localTasks, &localTask)
	}

	// 更新到内存存储
	config.UpdateTaskInstances(localTasks)
	log.InfoContextf(ctx, "[Heartbeat] 任务实例已更新到内存，总任务数: %d, 当前MD5: %s",
		len(localTasks), config.GetCurrentTasksMD5())
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

	nodeID, version = config.GetNodeInfo()
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

// getRunningTasks 获取运行任务（目前返回空，未来可扩展）
func getRunningTasks() []*model.TaskSummary {
	// TODO: 从任务管理器获取实际运行的任务
	// 目前返回空切片，保持向后兼容
	return []*model.TaskSummary{}
}

// getTaskStatistics 获取任务统计信息
func getTaskStatistics() model.TaskStatsInfo {
	// TODO: 从任务管理器获取实际的统计数据
	// 目前返回默认值，保持向后兼容
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
	serverIP, serverPort := config.GetServerInfo()

	return model.HeartbeatInfo{
		LastReport:  time.Now(),
		ReportCount: probeConfig.ReportCount,
		ErrorCount:  probeConfig.ErrorCount,
		Interval:    probeConfig.Interval,
		ServerIP:    serverIP,
		ServerPort:  serverPort,
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
			model.CollectorTypeOKX,
			model.CollectorTypeHuobi,
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
	allRecords := dnsproxy.GetAllDNSRecords()
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

