package serverless

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	runtimeapp "github.com/mooyang-code/moox/modules/collector/internal/app/runtime"
	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/modules/collector/internal/model"
	"github.com/tencentyun/scf-go-lib/cloudfunction"
	"github.com/tencentyun/scf-go-lib/functioncontext"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudFunctionHandler 云函数处理器
type CloudFunctionHandler struct{}

// NewCloudFunctionHandler 创建云函数处理器
func NewCloudFunctionHandler() *CloudFunctionHandler {
	return &CloudFunctionHandler{}
}

// RegisterCloudFunction blocks in Tencent's invocation server. The process has
// no resident worker: each event executes one bounded action and returns.
func RegisterCloudFunction() {
	handler := NewCloudFunctionHandler()
	cloudfunction.Start(handler.HandleRequest)
}

// HandleRequest 处理云函数请求 - 通用处理器【入口方法】
func (h *CloudFunctionHandler) HandleRequest(ctx context.Context, event json.RawMessage) (response interface{}, err error) {
	// SCF 环境下任何未恢复的 panic 都会导致 "Process exited unexpectedly"，
	// 整个实例退出、后续 invoke 全部失败、K线停采。这里统一 recover 并打印栈，
	// 保证单个事件处理异常不会拖垮整个 SCF 实例。
	defer func() {
		if r := recover(); r != nil {
			log.ErrorContextf(ctx, "[CloudFunction] HandleRequest panic recovered: %v\n%s", r, debug.Stack())
			response = h.errorResponse("internal_error", "SCF handler panic recovered")
			err = nil
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

	switch event.Action {
	case model.EventActionMarketFetch:
		return marketfetch.NewHandler().Handle(ctx, event)
	case model.EventActionEgressProbe:
		provider, market := "binance", "spot"
		if event.Data != nil {
			if value, ok := event.Data["provider"].(string); ok && strings.TrimSpace(value) != "" {
				provider = value
			}
			if value, ok := event.Data["market_type"].(string); ok && strings.TrimSpace(value) != "" {
				market = value
			}
		}
		return marketfetch.EgressProbe(ctx, provider, market)
	default:
		return h.errorResponse("unknown_event_type", "unknown event Action: "+string(event.Action)), nil
	}
}

// errorResponse 创建错误响应
func (h *CloudFunctionHandler) errorResponse(code, message string) *model.Response {
	return &model.Response{
		Success:   false,
		Message:   message,
		Timestamp: time.Now(),
	}
}
