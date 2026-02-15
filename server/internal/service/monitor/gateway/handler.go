package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/mooyang-code/moox/server/internal/service/monitor"
	monitorapi "github.com/mooyang-code/moox/server/internal/service/monitor/api"
	"trpc.group/trpc-go/trpc-go/log"
)

// MonitorGatewayHandler 监控服务网关处理器
type MonitorGatewayHandler struct {
	engine    *gin.Engine
	serviceID string
}

// NewMonitorGatewayHandler 创建监控网关处理器
func NewMonitorGatewayHandler(svc monitor.Service) *MonitorGatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()
	engine.Use(gin.Recovery())

	api := engine.Group("/api/v1")
	monitorapi.RegisterRoutes(api, svc)
	log.Info("[Monitor Gateway] Monitor 路由注册成功")

	return &MonitorGatewayHandler{
		engine:    engine,
		serviceID: "monitor",
	}
}

// ServiceID 实现 ServiceHandler 接口
func (h *MonitorGatewayHandler) ServiceID() string {
	return h.serviceID
}

// ForwardRequest 实现 ServiceHandler 接口
func (h *MonitorGatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.DebugContextf(ctx, "[Monitor Gateway] ForwardRequest: method=%s", method)

	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	return h.executeRequest(ctx, req)
}

// RouteInfo 路由信息
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

func (h *MonitorGatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{Body: body}

	switch method {
	// 监控配置
	case "EnableMonitor":
		return h.extractHostIDRoute("/api/v1/monitor/enable", "POST", body)
	case "DisableMonitor":
		return h.extractHostIDRoute("/api/v1/monitor/disable", "POST", body)
	case "GetMonitorStatus":
		return h.extractHostIDRoute("/api/v1/monitor/status", "GET", body)

	// 监控数据
	case "GetCurrentMetrics":
		route.Path = "/api/v1/monitor/current"
		route.HTTPMethod = "GET"
		// 支持 host_ids 查询参数
		if len(body) > 0 {
			route = h.appendQueryParams(route, body)
		}
	case "GetHistoryMetrics":
		return h.extractHostAddressWithQueryRoute("/api/v1/monitor/history", body)
	case "TestNodeExporter":
		return h.extractHostIDRoute("/api/v1/monitor/test", "POST", body)

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
	return route, nil
}

// extractHostIDRoute 从 body 中提取 host_id 并构建路由
func (h *MonitorGatewayHandler) extractHostIDRoute(basePath, httpMethod string, body []byte) (*RouteInfo, error) {
	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err != nil {
		return nil, fmt.Errorf("failed to parse body: %w", err)
	}

	hostID, ok := params["host_id"]
	if !ok {
		return nil, fmt.Errorf("host_id not found in request")
	}

	return &RouteInfo{
		Path:       fmt.Sprintf("%s/%v", basePath, hostID),
		HTTPMethod: httpMethod,
		Body:       body,
	}, nil
}

// extractHostIDWithQueryRoute 提取 host_id 作为路径参数，其他参数作为查询参数
func (h *MonitorGatewayHandler) extractHostIDWithQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err != nil {
		return nil, fmt.Errorf("failed to parse body: %w", err)
	}

	hostID, ok := params["host_id"]
	if !ok {
		return nil, fmt.Errorf("host_id not found in request")
	}

	path := fmt.Sprintf("%s/%v", basePath, hostID)

	// 添加其他查询参数
	var queryParams []string
	for key, value := range params {
		if key == "host_id" {
			continue
		}
		if value != nil {
			queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
		}
	}
	if len(queryParams) > 0 {
		path = fmt.Sprintf("%s?%s", path, strings.Join(queryParams, "&"))
	}

	return &RouteInfo{
		Path:       path,
		HTTPMethod: "GET",
	}, nil
}

// extractHostAddressWithQueryRoute 提取 host_address 作为路径参数，其他参数作为查询参数
func (h *MonitorGatewayHandler) extractHostAddressWithQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err != nil {
		return nil, fmt.Errorf("failed to parse body: %w", err)
	}

	hostAddress, ok := params["host_address"]
	if !ok {
		return nil, fmt.Errorf("host_address not found in request")
	}

	path := fmt.Sprintf("%s/%v", basePath, hostAddress)

	// 添加其他查询参数
	var queryParams []string
	for key, value := range params {
		if key == "host_address" {
			continue
		}
		if value != nil {
			queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
		}
	}
	if len(queryParams) > 0 {
		path = fmt.Sprintf("%s?%s", path, strings.Join(queryParams, "&"))
	}

	return &RouteInfo{
		Path:       path,
		HTTPMethod: "GET",
	}, nil
}

// appendQueryParams 将 body 中的参数作为查询参数添加到路由
func (h *MonitorGatewayHandler) appendQueryParams(route *RouteInfo, body []byte) *RouteInfo {
	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err == nil {
		var queryParams []string
		for key, value := range params {
			if value != nil {
				queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
			}
		}
		if len(queryParams) > 0 {
			route.Path = fmt.Sprintf("%s?%s", route.Path, strings.Join(queryParams, "&"))
		}
	}
	return route
}

func (h *MonitorGatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
	var req *http.Request
	var err error

	if routeInfo.Body != nil && len(routeInfo.Body) > 0 {
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

	for key, value := range headers {
		switch key {
		case "access_token":
			req.Header.Set("X-Access-Token", value)
		case "trace_id":
			req.Header.Set("X-Trace-Id", value)
		case "client_ip":
			req.Header.Set("X-Client-Ip", value)
		default:
			req.Header.Set(key, value)
		}
	}
	return req, nil
}

func (h *MonitorGatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	recorder := httptest.NewRecorder()
	h.engine.ServeHTTP(recorder, req)

	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	log.DebugContextf(ctx, "[Monitor Gateway] Response status: %d", statusCode)

	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	if len(respBody) == 0 {
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from Monitor API",
		}
		return json.Marshal(emptyResp)
	}
	return respBody, nil
}
