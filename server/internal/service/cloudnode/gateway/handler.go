package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	cloudnodemgr "github.com/mooyang-code/moox/server/internal/service/cloudnode"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// CloudNodeGatewayHandler 云节点网关处理器
type CloudNodeGatewayHandler struct {
	engine    *gin.Engine
	serviceID string
}

// NewCloudNodeGatewayHandler 创建云节点网关处理器
func NewCloudNodeGatewayHandler(
	service cloudnodemgr.Service,
	asyncTaskService asynctask.Service,
) *CloudNodeGatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	// 添加中间件
	engine.Use(gin.Recovery())

	// API路由组（包含版本号）
	api := engine.Group("/api/v1")

	// 注册云节点路由（包括云账户和代码包管理路由）
	cloudnodeapi.RegisterCloudNodeRoutes(api, service, asyncTaskService)
	cloudnodeapi.RegisterPackageManagerRoutes(api, service)

	// 注册心跳服务路由
	cloudnodeapi.RegisterHeartbeatRoutes(api, service)
	log.Info("[CloudNode Gateway] 云节点、云账户、代码包管理和心跳服务路由注册成功")

	return &CloudNodeGatewayHandler{
		engine:    engine,
		serviceID: "cloudnode",
	}
}

// ServiceID 实现ServiceHandler接口
func (h *CloudNodeGatewayHandler) ServiceID() string {
	return h.serviceID
}

// RouteInfo 路由信息结构
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

// ForwardRequest 实现ServiceHandler接口，转发请求到内部引擎
func (h *CloudNodeGatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.InfoContextf(ctx, "[CloudNode Gateway] ForwardRequest called - method: %s, headers: %+v, body: %s", method, headers, string(body))

	// 解析方法并获取路由信息
	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	log.InfoContextf(ctx, "[CloudNode Gateway] Forwarding to engine: %s %s with body: %s", routeInfo.HTTPMethod, routeInfo.Path, string(routeInfo.Body))

	// 创建并执行HTTP请求
	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	// 执行请求并处理响应
	return h.executeRequest(ctx, req)
}

// parseMethodToRoute 解析方法名并返回路由信息
func (h *CloudNodeGatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{}

	switch method {
	// Cloud Node methods
	case "GetCloudNodeList", "GetNodeList", "ListNodes":
		route.Path = "/api/v1/cloud_node/list"
		route.HTTPMethod = "GET"
	case "GetCloudNodeDetail", "GetNodeDetail":
		route.Path = "/api/v1/cloud_node/detail"
		route.HTTPMethod = "GET"
	case "CreateCloudNode", "RegisterNode":
		route.Path = "/api/v1/cloud_node/register"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateCloudNode", "UpdateNode":
		route.Path = "/api/v1/cloud_node/update"
		route.HTTPMethod = "PUT"
		route.Body = body
	case "DeleteCloudNode", "RemoveNode", "DeleteNode":
		route.Path = "/api/v1/cloud_node/remove"
		route.HTTPMethod = "DELETE"
		route.Body = body
	case "Heartbeat", "ReportHeartbeat", "ReportHeartbeatInner":
		route.Path = "/api/v1/heartbeat/report"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateNodeFunction":
		route.Path = "/api/v1/cloud_node/update_function"
		route.HTTPMethod = "PUT"
		route.Body = body

	// Heartbeat Node management methods
	case "HeartbeatRegisterNode":
		route.Path = "/api/v1/heartbeat/nodes/register"
		route.HTTPMethod = "POST"
		route.Body = body
	case "HeartbeatListNodes":
		route.Path = "/api/v1/heartbeat/nodes"
		route.HTTPMethod = "GET"
	case "HeartbeatGetNode":
		route.Path = "/api/v1/heartbeat/nodes"
		route.HTTPMethod = "GET"
		// Need to extract node_id and node_type from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if nodeID, ok := params["node_id"].(string); ok && nodeID != "" {
					if nodeType, ok := params["node_type"].(string); ok && nodeType != "" {
						route.Path = fmt.Sprintf("/api/v1/heartbeat/nodes/%s/%s", nodeID, nodeType)
					}
				}
			}
		}
	case "HeartbeatUpdateNodeConfig":
		route.Path = "/api/v1/heartbeat/nodes"
		route.HTTPMethod = "PUT"
		// Need to extract node_id and node_type from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if nodeID, ok := params["node_id"].(string); ok && nodeID != "" {
					if nodeType, ok := params["node_type"].(string); ok && nodeType != "" {
						route.Path = fmt.Sprintf("/api/v1/heartbeat/nodes/%s/%s/config", nodeID, nodeType)
					}
				}
			}
		}
		route.Body = body
	case "HeartbeatUnregisterNode":
		route.Path = "/api/v1/heartbeat/nodes"
		route.HTTPMethod = "DELETE"
		// Need to extract node_id and node_type from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if nodeID, ok := params["node_id"].(string); ok && nodeID != "" {
					if nodeType, ok := params["node_type"].(string); ok && nodeType != "" {
						route.Path = fmt.Sprintf("/api/v1/heartbeat/nodes/%s/%s", nodeID, nodeType)
					}
				}
			}
		}
		route.Body = body

	// Probe methods
	case "ProbeNode":
		route.Path = "/api/v1/heartbeat/probe"
		route.HTTPMethod = "POST"
		// Need to extract node_id and node_type from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if nodeID, ok := params["node_id"].(string); ok && nodeID != "" {
					if nodeType, ok := params["node_type"].(string); ok && nodeType != "" {
						route.Path = fmt.Sprintf("/api/v1/heartbeat/probe/%s/%s", nodeID, nodeType)
					}
				}
			}
		}
		route.Body = body
	case "BatchCreateCloudNodes":
		route.Path = "/api/v1/cloud_node/batch/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "BatchDeleteCloudNodes":
		route.Path = "/api/v1/cloud_node/batch/delete"
		route.HTTPMethod = "POST"
		route.Body = body
	case "BatchDeploySCFNodes":
		route.Path = "/api/v1/cloud_node/batch/deploy"
		route.HTTPMethod = "POST"
		route.Body = body

	// Cloud Account methods
	case "GetCloudAccountList", "ListCloudAccounts", "ListAccounts":
		route.Path = "/api/v1/cloud_account/list"
		route.HTTPMethod = "GET"
	case "GetCloudAccountDetail":
		route.Path = "/api/v1/cloud_account/detail"
		route.HTTPMethod = "GET"
	case "CreateCloudAccount":
		route.Path = "/api/v1/cloud_account/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateCloudAccount":
		route.Path = "/api/v1/cloud_account/update"
		route.HTTPMethod = "PUT"
		route.Body = body
	case "DeleteCloudAccount":
		route.Path = "/api/v1/cloud_account/delete"
		route.HTTPMethod = "DELETE"
		route.Body = body

	// Function Package methods (previously packagemgr)
	case "GetPackageList":
		return h.buildMultiQueryRoute("/api/v1/function-packages", body)
	case "GetPackageDetail":
		route.Path = "/api/v1/function-packages"
		route.HTTPMethod = "GET"
		// Need to extract package_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if packageID, ok := params["package_id"].(string); ok && packageID != "" {
					route.Path = fmt.Sprintf("/api/v1/function-packages/%s", packageID)
				}
			}
		}
	case "DeletePackage":
		route.Path = "/api/v1/function-packages"
		route.HTTPMethod = "DELETE"
		// Need to extract package_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if packageID, ok := params["package_id"].(string); ok && packageID != "" {
					route.Path = fmt.Sprintf("/api/v1/function-packages/%s", packageID)
				}
			}
		}
	case "GetPackageDownloadURL":
		route.Path = "/api/v1/function-packages"
		route.HTTPMethod = "GET"
		// Need to extract package_id from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if packageID, ok := params["package_id"].(string); ok && packageID != "" {
					route.Path = fmt.Sprintf("/api/v1/function-packages/%s/download-url", packageID)
				}
			}
		}
	case "GetPackageOptions":
		return h.buildMultiQueryRoute("/api/v1/function-packages/options", body)
	case "UploadPackage":
		route.Path = "/api/v1/function-packages/upload"
		route.HTTPMethod = "POST"
		route.Body = body

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
	return route, nil
}

// buildMultiQueryRoute 构建多参数查询路由
func (h *CloudNodeGatewayHandler) buildMultiQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			var queryParams []string
			for key, value := range params {
				if value != nil {
					// 过滤掉空字符串，但保留数字0和false等有效值
					if str, ok := value.(string); ok && str == "" {
						continue
					}
					queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
				}
			}
			if len(queryParams) > 0 {
				route.Path = fmt.Sprintf("%s?%s", basePath, strings.Join(queryParams, "&"))
			}
		}
	}
	return route, nil
}

// createHTTPRequest 创建HTTP请求
func (h *CloudNodeGatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
	var req *http.Request
	var err error

	if routeInfo.Body != nil {
		req, err = http.NewRequest(routeInfo.HTTPMethod, routeInfo.Path, bytes.NewReader(routeInfo.Body))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
	} else {
		req, err = http.NewRequest(routeInfo.HTTPMethod, routeInfo.Path, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
	}

	// 添加请求头（规范化header key）
	for key, value := range headers {
		// 将网关传来的key转换为标准HTTP header格式
		switch key {
		case "access_token":
			req.Header.Set("X-Access-Token", value)
		case "trace_id":
			req.Header.Set("X-Trace-Id", value)
		case "client_ip":
			req.Header.Set("X-Client-Ip", value)
		case "user_agent":
			req.Header.Set("User-Agent", value)
		default:
			req.Header.Set(key, value)
		}
	}
	return req, nil
}

// executeRequest 执行请求并处理响应
func (h *CloudNodeGatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	// 创建响应记录器
	recorder := httptest.NewRecorder()

	// 使用引擎处理请求
	h.engine.ServeHTTP(recorder, req)

	// 读取响应
	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	log.InfoContextf(ctx, "[CloudNode Gateway] Response status: %d, body: %s", statusCode, string(respBody))

	// 检查状态码
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	// 处理空响应体
	if len(respBody) == 0 {
		log.ErrorContextf(ctx, "[CloudNode Gateway] Empty response body received")
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from API",
		}
		return json.Marshal(emptyResp)
	}
	return respBody, nil
}
