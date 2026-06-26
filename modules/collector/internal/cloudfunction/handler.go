package cloudfunction

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/executor"
	"github.com/mooyang-code/moox/modules/collector/internal/heartbeat"
	"github.com/mooyang-code/moox/modules/collector/pkg/config"
	"github.com/mooyang-code/moox/modules/collector/pkg/model"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudFunctionHandler 云函数处理器
type CloudFunctionHandler struct{}

const keepaliveHeartbeatTimeout = 8 * time.Second
const keepaliveTaskExecutionTimeout = 45 * time.Second

var reportHeartbeatAfterProbe = heartbeat.ReportHeartbeat
var executeDueTasksAfterHeartbeat = func(ctx context.Context) error {
	return executor.ExecuteDueTasks(ctx)
}

// NewCloudFunctionHandler 创建云函数处理器
func NewCloudFunctionHandler() *CloudFunctionHandler {
	return &CloudFunctionHandler{}
}

// RegisterCloudFunction 注册云函数处理器（在内部启动协程）
func RegisterCloudFunction() {
	handler := NewCloudFunctionHandler()
	go func() {
		cloudfunction.Start(handler.HandleRequest)
	}()
}

// HandleRequest 处理云函数请求 - 通用处理器【入口方法】
func (h *CloudFunctionHandler) HandleRequest(ctx context.Context, event json.RawMessage) (interface{}, error) {
	// SCF 环境下任何未恢复的 panic 都会导致 "Process exited unexpectedly"，
	// 整个实例退出、后续 invoke 全部失败、K线停采。这里统一 recover 并打印栈，
	// 保证单个事件处理异常不会拖垮整个 SCF 实例。
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContextf(ctx, "[CloudFunction] HandleRequest panic recovered: %v\n%s", r, debug.Stack())
		}
	}()

	// 从上下文获取云函数信息
	funcCtx, _ := functioncontext.FromContext(ctx)

	// 解析事件
	var cfEvent model.CloudFunctionEvent
	if err := json.Unmarshal(event, &cfEvent); err != nil {
		// 解析失败，直接报错
		fmt.Printf("解析云函数事件失败, error: %v, event: %s", err, string(event))
		return h.errorResponse("invalid_event", fmt.Sprintf("failed to parse event: %v", err)), nil
	}

	// 设置默认值
	if cfEvent.Timestamp == "" {
		cfEvent.Timestamp = time.Now().Format(time.RFC3339)
	}
	if cfEvent.RequestID == "" {
		cfEvent.RequestID = funcCtx.RequestID
	}
	h.applyRuntimeConfig(ctx, cfEvent, funcCtx)
	return h.processCloudFunctionEvent(ctx, cfEvent)
}

func (h *CloudFunctionHandler) applyRuntimeConfig(ctx context.Context, event model.CloudFunctionEvent, funcCtx *functioncontext.FunctionContext) {
	if event.ServerIP != "" && event.ServerPort > 0 {
		config.UpdateServerInfo(event.ServerIP, event.ServerPort)
		log.DebugContextf(ctx, "[CloudFunction] runtime server updated: %s:%d", event.ServerIP, event.ServerPort)
	} else if ip, port, ok := parseServerFromMooxURL(event.MooxServerURL); ok {
		// fallback：控制面 keepalive 事件可能只下发 moox_server_url 而未带
		// server_ip/server_port（历史版本兼容）。从 URL 解析出 ip:port，避免 SCF
		// 冷启动后 ServerInfo 为空导致 ReportHeartbeat 静默 return nil。
		config.UpdateServerInfo(ip, port)
		log.DebugContextf(ctx, "[CloudFunction] runtime server updated from moox_server_url: %s:%d", ip, port)
	}

	nodeID := ""
	if event.Data != nil {
		if value, ok := event.Data["node_id"].(string); ok && value != "" {
			nodeID = value
		}
	}
	if nodeID == "" && funcCtx != nil && funcCtx.FunctionName != "" {
		nodeID = funcCtx.FunctionName
	}
	if nodeID != "" {
		_, version := config.GetNodeInfo()
		config.UpdateNodeInfo(nodeID, version)
		log.DebugContextf(ctx, "[CloudFunction] runtime node updated: nodeID=%s", nodeID)
	}
}

// parseServerFromMooxURL 从 moox_server_url（形如 http://ip:port）解析出 ip 和 port。
// 解析失败或字段缺失时返回 ok=false。
func parseServerFromMooxURL(rawURL string) (string, int, bool) {
	if rawURL == "" {
		return "", 0, false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "", 0, false
	}
	host := u.Hostname()
	portStr := u.Port()
	if host == "" || portStr == "" {
		return "", 0, false
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 {
		return "", 0, false
	}
	return host, port, true
}

// processCloudFunctionEvent 处理云函数事件
func (h *CloudFunctionHandler) processCloudFunctionEvent(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	fmt.Printf("处理云函数事件, action: %s, data: %v", event.Action, event.Data)

	// 根据事件类型处理
	switch event.Action {
	case model.EventActionTask:
		return h.handleTask(ctx, event)

	case model.EventActionKeepalive:
		return h.handleKeepalive(ctx, event)

	default:
		return h.errorResponse("unknown_event_type", "unknown event Action: "+string(event.Action)), nil
	}
}

// handleTask 处理任务事件（服务端触发的任务立即执行）
func (h *CloudFunctionHandler) handleTask(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	log.InfoContextf(ctx, "[handleTask] 收到任务执行请求, data: %v", event.Data)

	// 1. 解析任务执行事件
	var taskEvent model.TaskExecuteEvent
	eventDataJSON, err := json.Marshal(event.Data)
	if err != nil {
		errMsg := fmt.Sprintf("failed to marshal event data: %v", err)
		log.ErrorContextf(ctx, "[handleTask] %s", errMsg)
		return h.errorResponse("invalid_task_data", errMsg), nil
	}

	if err := json.Unmarshal(eventDataJSON, &taskEvent); err != nil {
		errMsg := fmt.Sprintf("failed to unmarshal task event: %v", err)
		log.ErrorContextf(ctx, "[handleTask] %s", errMsg)
		return h.errorResponse("invalid_task_data", errMsg), nil
	}

	// 2. 验证必要字段
	if taskEvent.TaskID == "" {
		errMsg := "task_id is required"
		log.ErrorContextf(ctx, "[handleTask] %s", errMsg)
		return h.errorResponse("invalid_task_data", errMsg), nil
	}

	log.InfoContextf(ctx, "[handleTask] 开始执行任务: taskID=%s, symbol=%s, intervals=%v",
		taskEvent.TaskID, taskEvent.Symbol, taskEvent.Intervals)

	// 3. 调用 executor 立即执行任务
	result, err := executor.ExecuteTaskImmediately(ctx, &taskEvent)
	if err != nil {
		log.ErrorContextf(ctx, "[handleTask] 任务执行失败: taskID=%s, error=%v", taskEvent.TaskID, err)
		return &model.Response{
			Success:   false,
			Message:   fmt.Sprintf("task execution failed: %v", err),
			Data:      map[string]interface{}{"task_id": taskEvent.TaskID, "result": result},
			RequestID: event.RequestID,
			Timestamp: time.Now(),
		}, nil
	}

	log.InfoContextf(ctx, "[handleTask] 任务执行完成: taskID=%s, result=%s", taskEvent.TaskID, result)

	return &model.Response{
		Success: true,
		Message: "task executed successfully",
		Data: map[string]interface{}{
			"task_id": taskEvent.TaskID,
			"result":  result,
		},
		RequestID: event.RequestID,
		Timestamp: time.Now(),
	}, nil
}

// handleKeepalive 处理保活探测事件（包括心跳探测功能）
//
// 链路说明：
//   - 探测源标识 (source=keepalive_probe/heartbeat_probe) 来自控制面 keepalive_probe.go
//   - 只要 event 携带 ServerIP/ServerPort，无论 source 是否为探测源，都应尝试
//     ProcessProbe → ReportHeartbeat → ExecuteDueTasks，避免因 source 解析丢失
//     导致整条回调链路静默中断（历史故障：SCF 冷启动后 source 为空，collector
//     不再回调控制面，任务列表永不更新，K线视图停在某个时刻）
func (h *CloudFunctionHandler) handleKeepalive(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	log.InfoContextf(ctx, "[handleKeepalive] 执行保活探测, source=%s, ServerIP=%s, ServerPort=%d, action=%s",
		event.Source, event.ServerIP, event.ServerPort, event.Action)

	// 判定是否需要走完整心跳回调链路
	// 1. 探测源标识匹配 → 走完整链路
	// 2. 探测源标识缺失但携带 ServerIP → 仍走完整链路（防御 source 解析回归）
	// 3. 既非探测源也无 ServerIP → 仅返回保活响应（无法回调，无意义）
	probeSource := isKeepaliveProbeSource(event.Source)
	hasServerAddr := event.ServerIP != "" && event.ServerPort > 0
	shouldRunHeartbeat := probeSource || hasServerAddr

	if !shouldRunHeartbeat {
		log.WarnContextf(ctx, "[handleKeepalive] 跳过心跳回调: source=%q 无 ServerIP, 仅返回保活响应", event.Source)
		return h.buildKeepaliveResponse(ctx, event)
	}

	if !probeSource {
		log.WarnContextf(ctx, "[handleKeepalive] source=%q 非探测源但携带 ServerIP，仍执行心跳回调链路 (防御回归)", event.Source)
	}

	// 调用函数式心跳模块处理探测请求（更新 NodeID / ServerInfo）
	log.InfoContextf(ctx, "[handleKeepalive] 调用 ProcessProbe")
	_, err := heartbeat.ProcessProbe(ctx, event)
	if err != nil {
		log.ErrorContextf(ctx, "[handleKeepalive] 处理心跳探测请求失败: %v", err)
		// 探测处理失败不影响保活响应
	} else {
		log.InfoContextf(ctx, "[handleKeepalive] ProcessProbe 执行成功")
		heartbeatCtx, cancel := context.WithTimeout(ctx, keepaliveHeartbeatTimeout)
		defer cancel()
		if err := reportHeartbeatAfterProbe(heartbeatCtx); err != nil {
			log.WarnContextf(ctx, "[handleKeepalive] 心跳上报失败: %v", err)
		} else {
			log.InfoContextf(ctx, "[handleKeepalive] 心跳上报成功")
			executeCtx, cancel := context.WithTimeout(ctx, keepaliveTaskExecutionTimeout)
			defer cancel()
			if err := executeDueTasksAfterHeartbeat(executeCtx); err != nil {
				log.WarnContextf(ctx, "[handleKeepalive] 任务执行调度失败: %v", err)
			} else {
				log.InfoContextf(ctx, "[handleKeepalive] 任务执行调度完成")
			}
		}
	}

	// 构建保活响应
	return h.buildKeepaliveResponse(ctx, event)
}

func isKeepaliveProbeSource(source string) bool {
	return source == "keepalive_probe" || source == "heartbeat_probe"
}

// buildKeepaliveResponse 构建保活响应
func (h *CloudFunctionHandler) buildKeepaliveResponse(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	// 从云函数上下文获取信息
	funcCtx, _ := functioncontext.FromContext(ctx)

	// 获取节点ID：优先使用探测源携带的 node_id，其次全局配置，降级使用函数名
	nodeID, _ := config.GetNodeInfo()
	if isKeepaliveProbeSource(event.Source) && event.Data != nil {
		if probeNodeID, ok := event.Data["node_id"].(string); ok && probeNodeID != "" {
			nodeID = probeNodeID
		}
	}
	if nodeID == "" && funcCtx.FunctionName != "" {
		nodeID = funcCtx.FunctionName
	}
	if nodeID == "" {
		nodeID = "cloud-function" // 最后降级
	}

	// 构建节点信息
	// RunningTasks 反映本地真实任务缓存（按 nodeID 过滤），便于控制面观测 SCF 实际持有哪些任务
	nodeInfo := &model.NodeInfo{
		NodeID:       nodeID,
		NodeType:     "scf",
		Region:       funcCtx.TencentcloudRegion,
		Namespace:    funcCtx.Namespace,
		Version:      funcCtx.FunctionVersion,
		RunningTasks: h.collectRunningTaskIDs(nodeID),
		Capabilities: []model.CollectorType{
			model.CollectorTypeBinance,
			model.CollectorTypeOKX,
			model.CollectorTypeHuobi,
		},
		Metadata: map[string]string{
			"function_name": funcCtx.FunctionName,
			"request_id":    event.RequestID,
		},
	}

	return &model.Response{
		Success: true,
		Message: "keepalive ok",
		Data: map[string]interface{}{
			"node_info": nodeInfo,
			"timestamp": time.Now(),
			"status":    "keepalive",
		},
		RequestID: event.RequestID,
		Timestamp: time.Now(),
	}, nil
}

// collectRunningTaskIDs 收集本地缓存中属于该节点的任务 ID 列表
// 用于 keepalive 响应的 RunningTasks 字段，反映 SCF 实际持有的任务
func (h *CloudFunctionHandler) collectRunningTaskIDs(nodeID string) []string {
	tasks := config.GetTaskInstancesByNode(nodeID)
	ids := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t != nil && t.TaskID != "" {
			ids = append(ids, t.TaskID)
		}
	}
	return ids
}

// errorResponse 创建错误响应
func (h *CloudFunctionHandler) errorResponse(code, message string) *model.Response {
	return &model.Response{
		Success:   false,
		Message:   message,
		Timestamp: time.Now(),
	}
}
