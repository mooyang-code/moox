package cloudfunction

import (
	"context"
	"encoding/json"
	"fmt"
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
func (h *CloudFunctionHandler) handleKeepalive(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	log.InfoContextf(ctx, "[handleKeepalive] 执行保活探测, source=%s, ServerIP=%s, ServerPort=%d",
		event.Source, event.ServerIP, event.ServerPort)

	// 处理心跳探测请求（服务端主动发送的探测）
	if !isKeepaliveProbeSource(event.Source) {
		// 不是moox后台来的保活探测请求，直接返回保活响应
		log.InfoContextf(ctx, "[handleKeepalive] 非探测请求，直接返回保活响应")
		return h.buildKeepaliveResponse(ctx, event)
	}

	// 调用函数式心跳模块处理探测请求
	log.InfoContextf(ctx, "[handleKeepalive] 检测到探测请求，调用 ProcessProbe")
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

	// 获取节点ID：优先使用全局配置，降级使用函数名
	nodeID, _ := config.GetNodeInfo()
	if nodeID == "" && funcCtx.FunctionName != "" {
		nodeID = funcCtx.FunctionName
	}
	if nodeID == "" {
		nodeID = "cloud-function" // 最后降级
	}

	// 构建节点信息
	nodeInfo := &model.NodeInfo{
		NodeID:       nodeID,
		NodeType:     "scf",
		Region:       funcCtx.TencentcloudRegion,
		Namespace:    funcCtx.Namespace,
		Version:      funcCtx.FunctionVersion,
		RunningTasks: make([]string, 0),
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

	// 如果是保活探测请求，使用探测源中的节点ID（优先级最高）
	if isKeepaliveProbeSource(event.Source) && event.Data != nil {
		if probeNodeID, ok := event.Data["node_id"].(string); ok && probeNodeID != "" {
			nodeInfo.NodeID = probeNodeID
		}
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

// errorResponse 创建错误响应
func (h *CloudFunctionHandler) errorResponse(code, message string) *model.Response {
	return &model.Response{
		Success:   false,
		Message:   message,
		Timestamp: time.Now(),
	}
}
