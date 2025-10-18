package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	asynctaskapi "github.com/mooyang-code/moox/server/internal/service/asynctask/api"
	cloudnodeapi "github.com/mooyang-code/moox/server/internal/service/cloudnode/api"
	collectorapi "github.com/mooyang-code/moox/server/internal/service/collector/api"
	"github.com/mooyang-code/moox/server/internal/service/collector/core"
	packagemgrapi "github.com/mooyang-code/moox/server/internal/service/packagemgr/api"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// GatewayHandler 处理API请求的网关处理器
type GatewayHandler struct {
	engine      *gin.Engine
	serviceID   string
	httpClient  *http.Client
	authBaseURL string
}

// NewGatewayHandler 创建网关处理器
func NewGatewayHandler(collectorImpl *core.CollectorServiceImpl) *GatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	// 添加中间件
	engine.Use(gin.Recovery())

	// API路由组
	api := engine.Group("/api")

	// 注册采集器相关的gin路由
	if collectorImpl != nil {
		// 注册异步任务路由（使用带有执行器的服务实例）
		asynctaskapi.RegisterAsyncTaskRoutesWithService(api, collectorImpl.GetAsyncTaskService())

		// 注册采集器路由（支持多云账户）
		collectorapi.RegisterCollectorRoutes(api, collectorImpl.GetDB(), collectorImpl.GetCloudProviderByAccount)

		// 注册云节点路由
		cloudnodeapi.RegisterCloudNodeRoutes(api, collectorImpl.GetDB())

		// 注册包管理路由
		packagemgrapi.RegisterPackageManagerRoutes(api, collectorImpl.GetDB(), collectorImpl.GetAsyncTaskService())
		log.Info("[Gateway] 包管理路由注册成功")
	}

	// 创建HTTP客户端用于认证服务转发
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	return &GatewayHandler{
		engine:      engine,
		serviceID:   "collector",
		httpClient:  httpClient,
		authBaseURL: "http://127.0.0.1:20102", // 认证服务HTTP端口
	}
}

// ServiceID 实现ServiceHandler接口
func (h *GatewayHandler) ServiceID() string {
	return h.serviceID
}

// RouteInfo 路由信息结构
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

// ForwardRequest 实现ServiceHandler接口，转发请求到内部引擎
func (h *GatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.InfoContextf(ctx, "[Gateway] ForwardRequest called - method: %s, headers: %+v, body: %s", method, headers, string(body))

	// 特殊处理认证方法，通过HTTP转发到认证服务
	if authResp, isAuthMethod, err := h.handleAuthMethodByHTTP(ctx, method, headers, body); isAuthMethod {
		if err != nil {
			return nil, err
		}
		return authResp, nil
	}

	// 解析方法并获取路由信息
	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	log.InfoContextf(ctx, "[Gateway] Forwarding to engine: %s %s with body: %s", routeInfo.HTTPMethod, routeInfo.Path, string(routeInfo.Body))

	// 创建并执行HTTP请求
	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	// 执行请求并处理响应
	return h.executeRequest(ctx, req)
}

// parseMethodToRoute 解析方法名并返回路由信息
func (h *GatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{}

	switch method {
	// Function Package Management methods
	case "UploadPackage":
		route.Path = "/api/function-packages/upload"
		route.HTTPMethod = "POST"
		route.Body = body
	case "GetPackageList":
		return h.buildMultiQueryRoute("/api/function-packages", body)
	case "GetPackageDetail":
		return h.buildDetailRoute("/api/function-packages", "GET", body)
	case "DeletePackage":
		return h.buildDetailRoute("/api/function-packages", "DELETE", body)
	case "GetPackageDownloadURL":
		return h.buildDetailRouteWithSuffix("/api/function-packages", "GET", body, "download-url")
	case "GetPackageOptions":
		return h.buildMultiQueryRoute("/api/function-packages/options", body)
	case "GetUploadTaskStatus":
		return h.buildUploadTaskStatusRoute("/api/function-packages/upload-task", body)

	// Async Task methods
	case "AsyncTaskCreate":
		route.Path = "/api/async-task/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "AsyncTaskQuery":
		return h.buildQueryRoute("/api/async-task/query", body, "task_id")
	case "GetTaskDetail":
		return h.buildDetailRouteWithParam("/api/async-task", "GET", body, "task_id")
	case "CancelTask":
		return h.buildDetailRouteWithParamAndSuffix("/api/async-task", "POST", body, "task_id", "cancel")
	case "GetTaskDetails":
		return h.buildDetailRouteWithParamAndSuffix("/api/async-task", "GET", body, "task_id", "details")

	// Collector Task Config methods
	case "GetTaskConfigList", "ListTaskConfigs":
		route.Path = "/api/task-config/list"
		route.HTTPMethod = "GET"
	case "GetTaskConfigDetail":
		return h.buildDetailRoute("/api/task-config", "GET", body)
	case "CreateTaskConfig":
		route.Path = "/api/task-config/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateTaskConfig":
		return h.buildUpdateRoute("/api/task-config", body)
	case "DeleteTaskConfig":
		return h.buildDetailRoute("/api/task-config", "DELETE", body)
	case "BatchUpdateTaskConfigEnabled":
		route.Path = "/api/task-config/batch-update-enabled"
		route.HTTPMethod = "POST"
		route.Body = body

	// Task Instance methods
	case "GetTaskInstanceList", "ListTaskInstances":
		route.Path = "/api/task-instance/list"
		route.HTTPMethod = "GET"
	case "GetTaskInstanceDetail":
		return h.buildDetailRoute("/api/task-instance", "GET", body)
	case "CreateTaskInstance":
		route.Path = "/api/task-instance/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateTaskInstance":
		return h.buildUpdateRoute("/api/task-instance", body)
	case "DeleteTaskInstance":
		return h.buildDetailRoute("/api/task-instance", "DELETE", body)
	case "StartTaskInstance":
		return h.buildDetailRouteWithSuffix("/api/task-instance", "POST", body, "start")
	case "StopTaskInstance":
		return h.buildDetailRouteWithSuffix("/api/task-instance", "POST", body, "stop")
	case "GetTaskInstanceLogs":
		return h.buildDetailRouteWithSuffix("/api/task-instance", "POST", body, "logs")
	case "RetryTaskInstance":
		return h.buildDetailRouteWithSuffix("/api/task-instance", "POST", body, "retry")
	case "CancelTaskInstance":
		return h.buildDetailRouteWithSuffix("/api/task-instance", "POST", body, "cancel")

	// Node Tasks methods
	case "GetNodeTasksList":
		route.Path = "/api/node-tasks/list"
		route.HTTPMethod = "GET"
	case "GetNodeTasksDetail":
		return h.buildDetailRoute("/api/node-tasks", "GET", body)
	case "CreateNodeTasks":
		route.Path = "/api/node-tasks/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateNodeTasks":
		return h.buildUpdateRoute("/api/node-tasks", body)
	case "DeleteNodeTasks":
		return h.buildDetailRoute("/api/node-tasks", "DELETE", body)

	// Cloud Node methods
	case "GetSCFNodeList", "GetNodeList", "ListNodes":
		route.Path = "/api/cloud_node/list"
		route.HTTPMethod = "GET"
	case "GetSCFNodeDetail", "GetNodeDetail":
		route.Path = "/api/cloud_node/detail"
		route.HTTPMethod = "GET"
	case "CreateSCFNode", "RegisterNode":
		route.Path = "/api/cloud_node/register"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateSCFNode", "UpdateNode":
		route.Path = "/api/cloud_node/update"
		route.HTTPMethod = "PUT"
		route.Body = body
	case "DeleteSCFNode", "RemoveNode", "DeleteNode":
		route.Path = "/api/cloud_node/remove"
		route.HTTPMethod = "DELETE"
		route.Body = body
	case "Heartbeat":
		route.Path = "/api/cloud_node/heartbeat"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateNodeLoad":
		route.Path = "/api/cloud_node/update_load"
		route.HTTPMethod = "PUT"
		route.Body = body
	case "UpdateNodeFunction":
		route.Path = "/api/cloud_node/update_function"
		route.HTTPMethod = "PUT"
		route.Body = body
	case "BatchCreateSCFNodes":
		route.Path = "/api/scf-nodes/batch-create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "BatchDeleteSCFNodes":
		route.Path = "/api/scf-nodes/batch-delete"
		route.HTTPMethod = "POST"
		route.Body = body
	case "BatchDeploySCFNodes":
		route.Path = "/api/scf-nodes/batch-deploy"
		route.HTTPMethod = "POST"
		route.Body = body

	// Cloud Account methods
	case "GetCloudAccountList", "ListCloudAccounts", "ListAccounts":
		route.Path = "/api/cloud_account/list"
		route.HTTPMethod = "GET"
	case "GetCloudAccountDetail":
		route.Path = "/api/cloud_account/detail"
		route.HTTPMethod = "GET"
	case "CreateCloudAccount":
		route.Path = "/api/cloud_account/create"
		route.HTTPMethod = "POST"
		route.Body = body
	case "UpdateCloudAccount":
		route.Path = "/api/cloud_account/update"
		route.HTTPMethod = "PUT"
		route.Body = body
	case "DeleteCloudAccount":
		route.Path = "/api/cloud_account/delete"
		route.HTTPMethod = "DELETE"
		route.Body = body

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
	return route, nil
}

// buildDetailRoute 构建带ID的详情路由
func (h *GatewayHandler) buildDetailRoute(basePath, httpMethod string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: httpMethod,
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if id, ok := params["id"]; ok {
				route.Path = fmt.Sprintf("%s/%v", basePath, id)
			}
		}
	}
	return route, nil
}

// buildDetailRouteWithSuffix 构建带ID和后缀的路由
func (h *GatewayHandler) buildDetailRouteWithSuffix(basePath, httpMethod string, body []byte, suffix string) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: httpMethod,
	}

	// 对于GET请求，不需要body
	if httpMethod == "GET" {
		route.Body = nil
	} else {
		route.Body = body
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if id, ok := params["id"]; ok {
				route.Path = fmt.Sprintf("%s/%v/%s", basePath, id, suffix)
			}
		}
	}
	return route, nil
}

// buildDetailRouteWithParam 构建带参数名的详情路由
func (h *GatewayHandler) buildDetailRouteWithParam(basePath, httpMethod string, body []byte, paramName string) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: httpMethod,
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if id, ok := params[paramName]; ok {
				route.Path = fmt.Sprintf("%s/%v", basePath, id)
			}
		}
	}
	return route, nil
}

// buildDetailRouteWithParamAndSuffix 构建带参数名和后缀的路由
func (h *GatewayHandler) buildDetailRouteWithParamAndSuffix(basePath, httpMethod string, body []byte, paramName, suffix string) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: httpMethod,
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if id, ok := params[paramName]; ok {
				route.Path = fmt.Sprintf("%s/%v/%s", basePath, id, suffix)
			}
		}
	}
	return route, nil
}

// buildQueryRoute 构建查询参数路由
func (h *GatewayHandler) buildQueryRoute(basePath string, body []byte, paramName string) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if value, ok := params[paramName].(string); ok {
				route.Path = fmt.Sprintf("%s?%s=%s", basePath, paramName, value)
			}
		}
	}
	return route, nil
}

// buildMultiQueryRoute 构建多参数查询路由
func (h *GatewayHandler) buildMultiQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			queryParams := make([]string, 0)
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

// buildUpdateRoute 构建更新路由
func (h *GatewayHandler) buildUpdateRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "PUT",
		Body:       body,
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if id, ok := params["id"]; ok {
				route.Path = fmt.Sprintf("%s/%v", basePath, id)
			}
		}
	}
	return route, nil
}

// buildUploadTaskStatusRoute 构建上传任务状态查询路由
func (h *GatewayHandler) buildUploadTaskStatusRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}

	if len(body) > 0 {
		var params map[string]interface{}
		if err := json.Unmarshal(body, &params); err == nil {
			if taskId, ok := params["task_id"].(string); ok && taskId != "" {
				route.Path = fmt.Sprintf("%s/%s/status", basePath, taskId)
			}
		}
	}
	return route, nil
}

// createHTTPRequest 创建HTTP请求
func (h *GatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
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
func (h *GatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	// 创建响应记录器
	recorder := httptest.NewRecorder()

	// 使用引擎处理请求
	h.engine.ServeHTTP(recorder, req)

	// 读取响应
	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	// 检查是否是二进制文件响应（通过Content-Type判断）
	contentType := recorder.Header().Get("Content-Type")
	if contentType == "application/zip" || contentType == "application/octet-stream" {
		log.InfoContextf(ctx, "[Gateway] Response status: %d, binary file content length: %d bytes", statusCode, len(respBody))
	} else {
		log.InfoContextf(ctx, "[Gateway] Response status: %d, body: %s", statusCode, string(respBody))
	}

	// 检查状态码
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	// 处理空响应体
	if len(respBody) == 0 {
		log.ErrorContextf(ctx, "[Gateway] Empty response body received")
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from API",
		}
		return json.Marshal(emptyResp)
	}
	return respBody, nil
}

// handleAuthMethodByHTTP 通过HTTP转发处理认证方法
func (h *GatewayHandler) handleAuthMethodByHTTP(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, bool, error) {
	// 检查是否为认证方法
	var authPath string
	switch method {
	case "GetLoginSalt":
		authPath = "/auth/GetLoginSalt"
	case "Login":
		authPath = "/auth/Login"
	case "Register":
		authPath = "/auth/Register"
	case "GetUserInfo":
		authPath = "/auth/GetUserInfo"
	default:
		return nil, false, nil // 不是认证方法
	}

	// 构建完整的认证服务URL
	fullURL := h.authBaseURL + authPath
	log.InfoContextf(ctx, "[Gateway] Forwarding auth request to: %s with body: %s", fullURL, string(body))

	// 创建HTTP请求
	req, err := http.NewRequestWithContext(ctx, "POST", fullURL, bytes.NewReader(body))
	if err != nil {
		return nil, true, fmt.Errorf("创建HTTP请求失败: %w", err)
	}

	// 设置请求头
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 执行HTTP请求
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, true, fmt.Errorf("HTTP请求失败: %w", err)
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, true, fmt.Errorf("读取响应失败: %w", err)
	}

	log.InfoContextf(ctx, "[Gateway] Auth response status: %d, body: %s", resp.StatusCode, string(respBody))

	// 检查状态码
	if resp.StatusCode != http.StatusOK {
		return nil, true, fmt.Errorf("认证服务请求失败，状态码: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	return respBody, true, nil
}
