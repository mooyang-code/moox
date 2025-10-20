package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/mooyang-code/moox/server/internal/service/asynctask"
	asynctaskapi "github.com/mooyang-code/moox/server/internal/service/asynctask/api"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// AsyncTaskGatewayHandler 异步任务网关处理器
type AsyncTaskGatewayHandler struct {
	engine    *gin.Engine
	serviceID string
}

// NewAsyncTaskGatewayHandler 创建异步任务网关处理器
func NewAsyncTaskGatewayHandler(asyncTaskService asynctask.Service) *AsyncTaskGatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	// 添加中间件
	engine.Use(gin.Recovery())

	// API路由组
	api := engine.Group("/api")

	// 注册异步任务HTTP路由
	asynctaskapi.RegisterRoutes(api, asyncTaskService)
	log.Info("[AsyncTask Gateway] 异步任务路由注册成功")

	return &AsyncTaskGatewayHandler{
		engine:    engine,
		serviceID: "asynctask",
	}
}

// ServiceID 实现ServiceHandler接口
func (h *AsyncTaskGatewayHandler) ServiceID() string {
	return h.serviceID
}

// RouteInfo 路由信息结构
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

// ForwardRequest 实现ServiceHandler接口，转发请求到内部引擎
func (h *AsyncTaskGatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.InfoContextf(ctx, "[AsyncTask Gateway] ForwardRequest called - method: %s, headers: %+v, body: %s", method, headers, string(body))

	// 解析方法并获取路由信息
	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	log.InfoContextf(ctx, "[AsyncTask Gateway] Forwarding to engine: %s %s with body: %s", routeInfo.HTTPMethod, routeInfo.Path, string(routeInfo.Body))

	// 创建并执行HTTP请求
	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	// 执行请求并处理响应
	return h.executeRequest(ctx, req)
}

// parseMethodToRoute 解析方法名并返回路由信息
func (h *AsyncTaskGatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{}

	switch method {
	// Async Job methods (Job-Task 模型)
	case "CreateAsyncJob":
		route.Path = "/api/async/jobs"
		route.HTTPMethod = "POST"
		route.Body = body
	case "QueryAsyncJob":
		return h.buildDetailRouteWithParam("/api/async/jobs", "GET", body, "job_id")
	case "GetTaskDetail":
		return h.buildDetailRouteWithParam("/api/async-task", "GET", body, "task_id")
	case "CancelTask":
		return h.buildDetailRouteWithParamAndSuffix("/api/async-task", "POST", body, "task_id", "cancel")
	case "GetTaskDetails":
		return h.buildDetailRouteWithParamAndSuffix("/api/async-task", "GET", body, "task_id", "details")

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
	return route, nil
}

// buildDetailRouteWithParam 构建带参数名的详情路由
func (h *AsyncTaskGatewayHandler) buildDetailRouteWithParam(basePath, httpMethod string, body []byte, paramName string) (*RouteInfo, error) {
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
func (h *AsyncTaskGatewayHandler) buildDetailRouteWithParamAndSuffix(basePath, httpMethod string, body []byte, paramName, suffix string) (*RouteInfo, error) {
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
func (h *AsyncTaskGatewayHandler) buildQueryRoute(basePath string, body []byte, paramName string) (*RouteInfo, error) {
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

// createHTTPRequest 创建HTTP请求
func (h *AsyncTaskGatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
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
func (h *AsyncTaskGatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	// 创建响应记录器
	recorder := httptest.NewRecorder()

	// 使用引擎处理请求
	h.engine.ServeHTTP(recorder, req)

	// 读取响应
	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	log.InfoContextf(ctx, "[AsyncTask Gateway] Response status: %d, body: %s", statusCode, string(respBody))

	// 检查状态码
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	// 处理空响应体
	if len(respBody) == 0 {
		log.ErrorContextf(ctx, "[AsyncTask Gateway] Empty response body received")
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from API",
		}
		return json.Marshal(emptyResp)
	}
	return respBody, nil
}
