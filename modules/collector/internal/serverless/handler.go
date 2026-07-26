package serverless

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/mooyang-code/moox/modules/collector/internal/reporter"
	"github.com/mooyang-code/moox/modules/collector/internal/taskrunner"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudFunctionHandler 云函数处理器
type CloudFunctionHandler struct{}

const keepaliveHeartbeatTimeout = 8 * time.Second
const keepaliveTaskExecutionTimeout = 115 * time.Second

var reportHeartbeatAfterProbe = reporter.ReportHeartbeat
var pollJobItemsAfterHeartbeat = func(ctx context.Context) error {
	return taskrunner.RunJobItems(ctx)
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
		log.ErrorContextf(ctx, "[CloudFunction] 解析云函数事件失败: %v", err)
		return h.errorResponse("invalid_event", fmt.Sprintf("failed to parse event: %v", err)), nil
	}

	// 设置默认值
	if cfEvent.Timestamp == "" {
		cfEvent.Timestamp = time.Now().Format(time.RFC3339)
	}
	if cfEvent.RequestID == "" {
		cfEvent.RequestID = funcCtx.RequestID
	}
	cfEvent = h.applyRuntimeConfig(ctx, cfEvent, funcCtx)
	return h.processCloudFunctionEvent(ctx, cfEvent)
}

func (h *CloudFunctionHandler) applyRuntimeConfig(ctx context.Context, event model.CloudFunctionEvent, funcCtx *functioncontext.FunctionContext) model.CloudFunctionEvent {
	if serviceURL := deploymentBaseURL(event.ServiceDeployments, "service_gateway", "admin_gateway", "moox-admin", "admin"); serviceURL != "" {
		event.ServiceGatewayTarget = serviceURL
		runtimeapp.UpdateServiceGatewayTarget(serviceURL)
		log.DebugContextf(ctx, "[CloudFunction] runtime service gateway target updated from service_deployments: %s", serviceURL)
	} else if strings.TrimSpace(event.ServiceGatewayTarget) != "" {
		runtimeapp.UpdateServiceGatewayTarget(event.ServiceGatewayTarget)
		log.DebugContextf(ctx, "[CloudFunction] runtime service gateway target updated: %s", event.ServiceGatewayTarget)
	}

	metadataTarget := deploymentTRPCTarget(event.ServiceDeployments, "storage-primary")
	accessTarget := metadataTarget
	if metadataTarget != "" || accessTarget != "" {
		runtimeapp.UpdateStorageRPCGatewayTarget(metadataTarget)
		if metadataTarget != "" {
			event.StorageRPCGatewayTarget = metadataTarget
		}
		log.DebugContextf(ctx, "[CloudFunction] runtime storage gateway target updated from service_deployments: %s", metadataTarget)
	} else if runtimeapp.IsStorageTRPCTarget(event.StorageRPCGatewayTarget) {
		runtimeapp.UpdateStorageRPCGatewayTarget(event.StorageRPCGatewayTarget)
		log.DebugContextf(ctx, "[CloudFunction] runtime storage gateway target updated from event: %s", event.StorageRPCGatewayTarget)
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
		_, version := runtimeapp.GetNodeInfo()
		runtimeapp.UpdateNodeInfo(nodeID, version)
		log.DebugContextf(ctx, "[CloudFunction] runtime node updated: nodeID=%s", nodeID)
	}
	return event
}

func deploymentBaseURL(deployments map[string]model.ServiceDeployment, names ...string) string {
	for _, item := range deployments {
		for _, name := range names {
			if !deploymentMatches(item, name) {
				continue
			}
			if item.BaseURL != "" {
				return strings.TrimRight(item.BaseURL, "/")
			}
			if item.Protocol != "" && item.Host != "" && item.Port > 0 {
				return fmt.Sprintf("%s://%s:%d", item.Protocol, item.Host, item.Port)
			}
		}
	}
	for _, name := range names {
		item, ok := deployments[name]
		if !ok {
			continue
		}
		if item.BaseURL != "" {
			return strings.TrimRight(item.BaseURL, "/")
		}
		if item.Protocol != "" && item.Host != "" && item.Port > 0 {
			return fmt.Sprintf("%s://%s:%d", item.Protocol, item.Host, item.Port)
		}
	}
	return ""
}

func deploymentTRPCTarget(deployments map[string]model.ServiceDeployment, names ...string) string {
	for _, item := range deployments {
		for _, name := range names {
			if !deploymentMatches(item, name) {
				continue
			}
			if value := deploymentTRPCTargetValue(item); value != "" {
				return value
			}
		}
	}
	for _, name := range names {
		item, ok := deployments[name]
		if !ok {
			continue
		}
		if value := deploymentTRPCTargetValue(item); value != "" {
			return value
		}
	}
	return ""
}

func deploymentTRPCTargetValue(item model.ServiceDeployment) string {
	if runtimeapp.IsStorageTRPCTarget(item.RPCAddress) {
		return strings.TrimRight(strings.TrimSpace(item.RPCAddress), "/")
	}
	if item.Host != "" && item.Port > 0 && !isHTTPProtocol(item.Protocol) {
		if strings.EqualFold(item.Protocol, "ip") || strings.EqualFold(item.Protocol, "ip://") {
			return fmt.Sprintf("ip://%s:%d", item.Host, item.Port)
		}
		return fmt.Sprintf("%s:%d", item.Host, item.Port)
	}
	return ""
}

func isHTTPProtocol(protocol string) bool {
	protocol = strings.TrimSpace(strings.ToLower(protocol))
	return protocol == "http" || protocol == "https"
}

func deploymentMatches(item model.ServiceDeployment, name string) bool {
	normalized := normalizeDeploymentName(name)
	serviceName := normalizeDeploymentName(item.ServiceName)
	serviceKind := normalizeDeploymentName(item.ServiceKind)
	return serviceName == normalized ||
		serviceKind == normalized ||
		strings.TrimPrefix(serviceName, "moox_") == normalized
}

func normalizeDeploymentName(name string) string {
	normalized := strings.TrimSpace(strings.ToLower(name))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	return normalized
}

// processCloudFunctionEvent 处理云函数事件
func (h *CloudFunctionHandler) processCloudFunctionEvent(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	log.DebugContextf(ctx, "[CloudFunction] 处理云函数事件, action=%s", event.Action)

	// 根据事件类型处理
	switch event.Action {
	case model.EventActionTask:
		return h.errorResponse("unsupported_event_type", "direct task execution is disabled; use CloudNode JobItem polling"), nil

	case model.EventActionKeepalive:
		return h.handleKeepalive(ctx, event)

	default:
		return h.errorResponse("unknown_event_type", "unknown event Action: "+string(event.Action)), nil
	}
}

// handleKeepalive 处理保活探测事件（包括心跳探测功能）
//
// 链路说明：
//   - 探测源标识 (source=keepalive_probe/heartbeat_probe) 来自控制面 keepalive_probe.go。
//   - service_deployments / service_gateway_target 会先被 applyRuntimeConfig 解析。
//   - 只要事件携带或解析出了服务网关地址，就尝试 ProcessProbe 和 ReportHeartbeat。
func (h *CloudFunctionHandler) handleKeepalive(ctx context.Context, event model.CloudFunctionEvent) (*model.Response, error) {
	log.InfoContextf(ctx, "[handleKeepalive] 执行保活探测, source=%s, service_gateway_target=%s, action=%s",
		event.Source, event.ServiceGatewayTarget, event.Action)

	// 判定是否需要走完整心跳回调链路
	// 1. 探测源标识匹配 → 走完整链路
	// 2. 探测源标识缺失但携带 service_gateway_target → 仍走完整链路（防御 source 解析回归）
	// 3. 既非探测源也无服务网关地址 → 仅返回保活响应（无法回调，无意义）
	probeSource := isKeepaliveProbeSource(event.Source)
	hasServiceGatewayTarget := strings.TrimSpace(event.ServiceGatewayTarget) != ""
	shouldRunHeartbeat := probeSource || hasServiceGatewayTarget

	if !shouldRunHeartbeat {
		log.WarnContextf(ctx, "[handleKeepalive] 跳过心跳回调: source=%q 无 service_gateway_target, 仅返回保活响应", event.Source)
		return h.buildKeepaliveResponse(ctx, event)
	}

	if !probeSource {
		log.WarnContextf(ctx, "[handleKeepalive] source=%q 非探测源但携带服务网关地址，仍执行心跳回调链路 (防御回归)", event.Source)
	}

	// 调用函数式心跳模块处理探测请求（更新 NodeID / ServerInfo）
	log.InfoContextf(ctx, "[handleKeepalive] 调用 ProcessProbe")
	_, err := reporter.ProcessProbe(ctx, event)
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
		}
		executeCtx, cancel := context.WithTimeout(ctx, keepaliveTaskExecutionTimeout)
		defer cancel()
		if err := pollJobItemsAfterHeartbeat(executeCtx); err != nil {
			log.WarnContextf(ctx, "[handleKeepalive] CloudNode JobItem 拉取/执行失败: %v", err)
		} else {
			log.InfoContextf(ctx, "[handleKeepalive] CloudNode JobItem 拉取/执行完成")
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
	nodeID, _ := runtimeapp.GetNodeInfo()
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

	// 构建节点信息。任务执行由 CloudNode JobItem 租约维护，keepalive 不再展示旧本地任务缓存。
	nodeInfo := &model.NodeInfo{
		NodeID:       nodeID,
		NodeType:     "scf",
		Region:       funcCtx.TencentcloudRegion,
		Namespace:    funcCtx.Namespace,
		Version:      funcCtx.FunctionVersion,
		RunningTasks: []string{},
		Capabilities: []model.CollectorType{
			model.CollectorTypeBinance,
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

// errorResponse 创建错误响应
func (h *CloudFunctionHandler) errorResponse(code, message string) *model.Response {
	return &model.Response{
		Success:   false,
		Message:   message,
		Timestamp: time.Now(),
	}
}
