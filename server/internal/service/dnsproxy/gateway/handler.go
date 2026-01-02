package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime/debug"
	"strings"

	dnsproxyapi "github.com/mooyang-code/moox/server/internal/service/dnsproxy/api"

	"github.com/gin-gonic/gin"
	"trpc.group/trpc-go/trpc-go/log"
)

// customRecovery 自定义 Recovery 中间件，返回 JSON 格式错误响应
func customRecovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if err := recover(); err != nil {
				// 记录详细的 panic 信息
				stack := string(debug.Stack())
				log.Errorf("[DNSProxy Gateway] Panic recovered: %v\nStack: %s", err, stack)

				// 返回 JSON 格式的错误响应
				c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{
					"code":    500,
					"message": fmt.Sprintf("Internal server error: %v", err),
					"data":    []interface{}{},
				})
			}
		}()
		c.Next()
	}
}

// DNSProxyGatewayHandler DNS代理网关处理器
type DNSProxyGatewayHandler struct {
	engine    *gin.Engine
	serviceID string
}

// NewGatewayHandler 创建DNS代理网关处理器
func NewGatewayHandler() *DNSProxyGatewayHandler {
	gin.SetMode(gin.ReleaseMode)
	engine := gin.New()

	// 添加自定义 Recovery 中间件，返回 JSON 格式错误响应
	engine.Use(customRecovery())

	// API路由组（包含版本号）
	api := engine.Group("/api/v1")

	// 注册DNS代理路由
	dnsproxyapi.RegisterDNSProxyRoutes(api)
	log.Info("[DNSProxy Gateway] DNS代理模块路由注册成功")

	return &DNSProxyGatewayHandler{
		engine:    engine,
		serviceID: "dnsproxy",
	}
}

// ServiceID 实现ServiceHandler接口
func (h *DNSProxyGatewayHandler) ServiceID() string {
	return h.serviceID
}

// RouteInfo 路由信息结构
type RouteInfo struct {
	Path       string
	HTTPMethod string
	Body       []byte
}

// ForwardRequest 实现ServiceHandler接口，转发请求到内部引擎
func (h *DNSProxyGatewayHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	log.DebugContextf(ctx, "[DNSProxy Gateway] ForwardRequest called - method: %s, headers: %+v, body: %s", method, headers, string(body))

	// 解析方法并获取路由信息
	routeInfo, err := h.parseMethodToRoute(method, body)
	if err != nil {
		return nil, err
	}

	log.DebugContextf(ctx, "[DNSProxy Gateway] Forwarding to engine: %s %s with body: %s", routeInfo.HTTPMethod, routeInfo.Path, string(routeInfo.Body))

	// 创建并执行HTTP请求
	req, err := h.createHTTPRequest(routeInfo, headers)
	if err != nil {
		return nil, err
	}

	// 执行请求并处理响应
	return h.executeRequest(ctx, req)
}

// parseMethodToRoute 解析方法名并返回路由信息
func (h *DNSProxyGatewayHandler) parseMethodToRoute(method string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{}

	switch method {
	// DNS Record methods (DNS解析记录)
	case "GetDNSRecordList", "ListDNSRecords":
		return h.buildMultiQueryRoute("/api/v1/dns-record/list", body)
	case "GetDNSRecordDetail":
		route.Path = "/api/v1/dns-record"
		route.HTTPMethod = "GET"
		// Need to extract domain from body and add to path
		if len(body) > 0 {
			var params map[string]interface{}
			if err := json.Unmarshal(body, &params); err == nil {
				if domain, ok := params["domain"].(string); ok && domain != "" {
					route.Path = fmt.Sprintf("/api/v1/dns-record/%s", domain)
				}
			}
		}

	default:
		return nil, fmt.Errorf("unsupported method: %s", method)
	}
	return route, nil
}

// buildMultiQueryRoute 构建多参数查询路由
func (h *DNSProxyGatewayHandler) buildMultiQueryRoute(basePath string, body []byte) (*RouteInfo, error) {
	route := &RouteInfo{
		Path:       basePath,
		HTTPMethod: "GET",
	}

	if len(body) == 0 {
		return route, nil
	}

	var params map[string]interface{}
	if err := json.Unmarshal(body, &params); err != nil {
		return route, nil
	}

	var queryParams []string
	for key, value := range params {
		if value == nil {
			continue
		}
		// 过滤掉空字符串，但保留数字0和false等有效值
		if str, ok := value.(string); ok && str == "" {
			continue
		}
		queryParams = append(queryParams, fmt.Sprintf("%s=%v", key, value))
	}

	if len(queryParams) > 0 {
		route.Path = fmt.Sprintf("%s?%s", basePath, strings.Join(queryParams, "&"))
	}
	return route, nil
}

// createHTTPRequest 创建HTTP请求
func (h *DNSProxyGatewayHandler) createHTTPRequest(routeInfo *RouteInfo, headers map[string]string) (*http.Request, error) {
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
func (h *DNSProxyGatewayHandler) executeRequest(ctx context.Context, req *http.Request) ([]byte, error) {
	// 创建响应记录器
	recorder := httptest.NewRecorder()

	// 使用引擎处理请求
	h.engine.ServeHTTP(recorder, req)

	// 读取响应
	respBody := recorder.Body.Bytes()
	statusCode := recorder.Code

	log.DebugContextf(ctx, "[DNSProxy Gateway] Response status: %d, body: %s", statusCode, string(respBody))

	// 检查状态码
	if statusCode != http.StatusOK {
		return nil, fmt.Errorf("request failed with status %d: %s", statusCode, string(respBody))
	}

	// 处理空响应体
	if len(respBody) == 0 {
		log.ErrorContextf(ctx, "[DNSProxy Gateway] Empty response body received")
		emptyResp := map[string]interface{}{
			"code": 500,
			"data": []interface{}{},
			"msg":  "Empty response from API",
		}
		return json.Marshal(emptyResp)
	}
	return respBody, nil
}
