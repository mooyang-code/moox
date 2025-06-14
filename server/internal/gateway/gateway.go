package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/mooyang-code/moox/server/internal/config"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/gorilla/mux"
)

// ============================================================================
// 接口定义
// ============================================================================

// ServiceHandler 服务转发处理器接口
type ServiceHandler interface {
	// ServiceID 获取服务ID
	ServiceID() string
	// ForwardRequest 转发请求到底层服务
	ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error)
}

// ============================================================================
// 网关管理器
// ============================================================================

var (
	gatewayHandleInstance *GatewayHandle
	gatewayHandleOnce     sync.Once
)

// GatewayHandle 网关处理器
type GatewayHandle struct {
	// 服务处理器映射
	handlers map[string]ServiceHandler
	// HTTP客户端
	httpClient *http.Client
	// HTTP请求处理器
	requestHandler *HTTPRequestHandler
}

// GetGatewayHandleInstance 返回网关处理器的全局单例实例
func GetGatewayHandleInstance() *GatewayHandle {
	gatewayHandleOnce.Do(func() {
		gatewayHandleInstance = NewGatewayHandle()
	})
	return gatewayHandleInstance
}

var NewGatewayHandle = func() *GatewayHandle {
	return &GatewayHandle{
		handlers: make(map[string]ServiceHandler),
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
		requestHandler: NewHTTPRequestHandler(),
	}
}

// Register 注册服务处理器
func (g *GatewayHandle) Register(handler ServiceHandler) {
	if g.handlers == nil {
		g.handlers = make(map[string]ServiceHandler)
	}
	g.handlers[handler.ServiceID()] = handler
}

// GetRegisteredServices 获取已注册的服务列表
func (g *GatewayHandle) GetRegisteredServices() []string {
	var services []string
	for serviceID := range g.handlers {
		services = append(services, serviceID)
	}
	return services
}

// GetHandler 根据服务ID获取处理器
func (g *GatewayHandle) GetHandler(serviceID string) (ServiceHandler, bool) {
	handler, ok := g.handlers[serviceID]
	return handler, ok
}

// ============================================================================
// HTTP路由注册
// ============================================================================

// HTTPRouter HTTP路由管理器
type HTTPRouter struct {
	gateway *GatewayHandle
}

// NewHTTPRouter 创建HTTP路由管理器
func NewHTTPRouter(gateway *GatewayHandle) *HTTPRouter {
	return &HTTPRouter{
		gateway: gateway,
	}
}

// RegisterGatewayHTTPHandlers 注册网关HTTP接口
func RegisterGatewayHTTPHandlers(s *server.Server) {
	gateway := GetGatewayHandleInstance()
	router := NewHTTPRouter(gateway)
	router.setupRoutes(s)
}

// setupRoutes 设置路由
func (hr *HTTPRouter) setupRoutes(s *server.Server) {
	router := mux.NewRouter()

	// 注册网关转发路由: /gateway/{service}/{method}
	router.HandleFunc("/gateway/{service}/{method}", hr.handleGatewayRequest).Methods("GET", "POST", "PUT", "DELETE")

	// 健康检查接口
	router.HandleFunc("/gateway/health", hr.handleHealthCheck).Methods("GET")

	thttp.RegisterNoProtocolServiceMux(s.Service("trpc.moox.gateway.stdhttp"), router)
}

// handleGatewayRequest 处理网关请求
func (hr *HTTPRouter) handleGatewayRequest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	handler := hr.gateway.requestHandler

	// 解析请求参数
	serviceID, method, err := handler.parseRequestParams(r)
	if err != nil {
		log.ErrorContextf(ctx, "解析请求参数失败: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 提取HTTP头部信息
	headers := handler.extractGatewayHeaders(r)

	// 读取请求体
	body, err := handler.readRequestBody(r)
	if err != nil {
		log.ErrorContextf(ctx, "读取请求体失败: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// 获取服务处理器
	serviceHandler, ok := hr.gateway.GetHandler(serviceID)
	if !ok {
		err := fmt.Errorf("未找到服务处理器: %s", serviceID)
		log.ErrorContextf(ctx, "%v", err)
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// 转发请求
	respBody, err := serviceHandler.ForwardRequest(ctx, method, headers, body)
	if err != nil {
		log.ErrorContextf(ctx, "转发请求失败: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 返回响应
	handler.writeResponse(w, respBody, headers)
}

// handleHealthCheck 处理健康检查请求
func (hr *HTTPRouter) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"time":     time.Now().Format("2006-01-02 15:04:05"),
		"services": hr.gateway.GetRegisteredServices(),
	})
}

// ============================================================================
// HTTP请求处理器
// ============================================================================

// HTTPRequestHandler HTTP请求处理器
type HTTPRequestHandler struct{}

// NewHTTPRequestHandler 创建HTTP请求处理器
func NewHTTPRequestHandler() *HTTPRequestHandler {
	return &HTTPRequestHandler{}
}

// parseRequestParams 解析请求参数
func (h *HTTPRequestHandler) parseRequestParams(r *http.Request) (serviceID, method string, err error) {
	vars := mux.Vars(r)

	serviceID, ok := vars["service"]
	if !ok || serviceID == "" {
		return "", "", fmt.Errorf("请求错误：未提供有效的服务名")
	}

	method, ok = vars["method"]
	if !ok || method == "" {
		return "", "", fmt.Errorf("请求错误：未提供有效的方法名")
	}

	return serviceID, method, nil
}

// readRequestBody 读取请求体
func (h *HTTPRequestHandler) readRequestBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("读取请求体失败: %v", err)
	}
	defer r.Body.Close()
	return body, nil
}

// extractGatewayHeaders 提取网关相关的HTTP头部信息
func (h *HTTPRequestHandler) extractGatewayHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)

	// 必需的认证信息
	if appID := r.Header.Get("X-App-Id"); appID != "" {
		headers["app_id"] = appID
	}
	if appKey := r.Header.Get("X-App-Key"); appKey != "" {
		headers["app_key"] = appKey
	}

	// 可选的头部信息
	if accessToken := r.Header.Get("X-Access-Token"); accessToken != "" {
		headers["access_token"] = accessToken
	}
	if traceID := r.Header.Get("X-Trace-Id"); traceID != "" {
		headers["trace_id"] = traceID
	}
	if clientIP := r.Header.Get("X-Client-Ip"); clientIP != "" {
		headers["client_ip"] = clientIP
	}
	if userAgent := r.Header.Get("User-Agent"); userAgent != "" {
		headers["user_agent"] = userAgent
	}

	// 如果没有提供客户端IP，尝试从其他头部获取
	if headers["client_ip"] == "" {
		headers["client_ip"] = h.getClientIP(r)
	}

	return headers
}

// getClientIP 获取客户端IP
func (h *HTTPRequestHandler) getClientIP(r *http.Request) string {
	if xForwardedFor := r.Header.Get("X-Forwarded-For"); xForwardedFor != "" {
		return xForwardedFor
	}
	if xRealIP := r.Header.Get("X-Real-IP"); xRealIP != "" {
		return xRealIP
	}
	return r.RemoteAddr
}

// writeResponse 写入响应
func (h *HTTPRequestHandler) writeResponse(w http.ResponseWriter, respBody []byte, headers map[string]string) {
	// 设置响应头
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-App-Id, X-App-Key, X-Access-Token, X-Trace-Id")

	// 设置追踪ID到响应头
	if traceID := headers["trace_id"]; traceID != "" {
		w.Header().Set("X-Trace-Id", traceID)
	}

	w.Write(respBody)
}

// ============================================================================
// HTTP服务处理器
// ============================================================================

// HTTPServiceHandler HTTP服务转发处理器
type HTTPServiceHandler struct {
	serviceID string
	config    config.ServiceConfig
	client    *http.Client
	// 请求构建器
	requestBuilder *HTTPRequestBuilder
}

// NewHTTPServiceHandler 创建HTTP服务处理器
func NewHTTPServiceHandler(serviceID string, serviceConfig config.ServiceConfig) *HTTPServiceHandler {
	return &HTTPServiceHandler{
		serviceID: serviceID,
		config:    serviceConfig,
		client: &http.Client{
			Timeout: serviceConfig.Timeout,
		},
		requestBuilder: NewHTTPRequestBuilder(serviceID, serviceConfig),
	}
}

// ServiceID 实现ServiceHandler接口
func (h *HTTPServiceHandler) ServiceID() string {
	return h.serviceID
}

// ForwardRequest 实现ServiceHandler接口，转发请求到底层HTTP服务
func (h *HTTPServiceHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	// 构建目标URL
	targetURL := h.requestBuilder.buildTargetURL(method)

	// 准备请求体
	requestBody, err := h.requestBuilder.buildRequestBody(headers, body)
	if err != nil {
		return nil, err
	}

	// 创建HTTP请求
	req, err := h.requestBuilder.createHTTPRequest(ctx, targetURL, requestBody, headers)
	if err != nil {
		return nil, err
	}

	// 发送请求并处理响应
	return h.sendRequest(ctx, req, targetURL, requestBody)
}

// sendRequest 发送请求并处理响应
func (h *HTTPServiceHandler) sendRequest(ctx context.Context, req *http.Request, targetURL string, requestBody []byte) ([]byte, error) {
	// 记录请求日志
	log.InfoContextf(ctx, "转发请求到: %s, 参数: %s", targetURL, string(requestBody))

	// 发送请求
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用底层服务失败: %v", err)
	}
	defer resp.Body.Close()

	// 读取响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	// 检查HTTP状态码
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("底层服务返回错误状态: %d, 响应: %s", resp.StatusCode, string(respBody))
	}

	log.InfoContextf(ctx, "收到底层响应: %s", string(respBody))
	return respBody, nil
}

// ============================================================================
// HTTP请求构建器
// ============================================================================

// HTTPRequestBuilder HTTP请求构建器
type HTTPRequestBuilder struct {
	serviceID string
	config    config.ServiceConfig
}

// NewHTTPRequestBuilder 创建HTTP请求构建器
func NewHTTPRequestBuilder(serviceID string, serviceConfig config.ServiceConfig) *HTTPRequestBuilder {
	return &HTTPRequestBuilder{
		serviceID: serviceID,
		config:    serviceConfig,
	}
}

// buildTargetURL 构建目标URL
func (b *HTTPRequestBuilder) buildTargetURL(method string) string {
	// 直接使用配置文件中的服务路径
	return fmt.Sprintf("%s/%s/%s", b.config.BaseURL, b.config.ServicePath, method)
}

// buildRequestBody 构建请求体
func (b *HTTPRequestBuilder) buildRequestBody(headers map[string]string, body []byte) ([]byte, error) {
	// 直接使用原始请求体，不做任何修改
	return body, nil
}

// createHTTPRequest 创建HTTP请求
func (b *HTTPRequestBuilder) createHTTPRequest(ctx context.Context, targetURL string, requestBody []byte, headers map[string]string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", targetURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("创建HTTP请求失败: %v", err)
	}

	// 设置请求头
	for key, value := range b.config.Headers {
		req.Header.Set(key, value)
	}

	// 添加追踪ID
	if traceID := headers["trace_id"]; traceID != "" {
		req.Header.Set("X-Trace-ID", traceID)
	}
	return req, nil
}
