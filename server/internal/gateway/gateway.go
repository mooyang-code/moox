package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
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
	router.HandleFunc(
		"/gateway/{service}/{method}",
		hr.handleGatewayRequest).
		Methods("GET", "POST", "PUT", "DELETE")

	// 健康检查接口
	router.HandleFunc("/gateway/health", hr.handleHealthCheck).Methods("GET")
	thttp.RegisterNoProtocolServiceMux(s.Service("trpc.moox.gateway.stdhttp"), router)
}

// handleGatewayRequest 处理网关请求(中间件authorize通过之后，执行流才到本函数)
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
// 优先级：X-Real-IP > X-Forwarded-For（第一个IP）> RemoteAddr
func (h *HTTPRequestHandler) getClientIP(r *http.Request) string {
	// 1. 优先使用X-Real-IP（通常由Nginx等反向代理设置，表示真实客户端IP）
	if xRealIP := r.Header.Get("X-Real-IP"); xRealIP != "" {
		return xRealIP
	}

	// 2. 使用X-Forwarded-For的第一个IP（客户端IP，后面是代理链）
	if xForwardedFor := r.Header.Get("X-Forwarded-For"); xForwardedFor != "" {
		// X-Forwarded-For 格式: client, proxy1, proxy2
		// 我们只取第一个IP（真实客户端）
		for idx := 0; idx < len(xForwardedFor); idx++ {
			if xForwardedFor[idx] == ',' {
				return xForwardedFor[:idx]
			}
		}
		return xForwardedFor
	}

	// 3. 如果没有代理头，使用RemoteAddr（可能包含端口号，需要去除）
	remoteAddr := r.RemoteAddr
	// 去除端口号
	if idx := len(remoteAddr) - 1; idx >= 0 {
		for ; idx >= 0; idx-- {
			if remoteAddr[idx] == ':' {
				return remoteAddr[:idx]
			}
		}
	}
	return remoteAddr
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
	config    ServiceConfig
}

// NewHTTPServiceHandler 创建HTTP服务处理器
func NewHTTPServiceHandler(serviceID string, serviceConfig ServiceConfig) *HTTPServiceHandler {
	return &HTTPServiceHandler{
		serviceID: serviceID,
		config:    serviceConfig,
	}
}

// ServiceID 实现ServiceHandler接口
func (h *HTTPServiceHandler) ServiceID() string {
	return h.serviceID
}

// ForwardRequest 实现ServiceHandler接口，转发请求到底层HTTP服务
func (h *HTTPServiceHandler) ForwardRequest(ctx context.Context, method string, headers map[string]string, body []byte) ([]byte, error) {
	// 构建目标URL路径
	targetURL := fmt.Sprintf("/%s/%s", h.config.ServicePath, method)
	log.InfoContextf(ctx, "ForwardRequest: %s", targetURL)

	// 准备请求体
	codecReq := &codec.Body{Data: body}

	// 构建HTTP请求头
	reqHead := h.buildRequestHeaders(ctx, headers)

	// 构建客户端选项
	opts := []client.Option{
		client.WithReqHead(reqHead),
		client.WithCurrentSerializationType(codec.SerializationTypeNoop),
	}

	// 创建HTTP代理客户端
	httpProxy := thttp.NewClientProxy(
		h.serviceID, // 服务名称(对应trpc_go.yaml中client的配置)
		opts...,
	)

	// 发送POST请求
	log.InfoContextf(ctx, "转发请求到: %s, 参数: %s", targetURL, string(body))
	codecRsp := &codec.Body{}
	if err := httpProxy.Post(ctx, targetURL, codecReq, codecRsp); err != nil {
		return nil, fmt.Errorf("调用底层服务失败: %v", err)
	}
	log.InfoContextf(ctx, "收到底层响应: %s", string(codecRsp.Data))
	return codecRsp.Data, nil
}

// buildRequestHeaders 构建HTTP请求头
func (h *HTTPServiceHandler) buildRequestHeaders(ctx context.Context, headers map[string]string) *thttp.ClientReqHeader {
	reqHead := &thttp.ClientReqHeader{}

	// 添加基础请求头
	reqHead.AddHeader("Content-Type", "application/json;charset=utf-8")

	// 添加配置中的请求头
	for key, value := range h.config.Headers {
		reqHead.AddHeader(key, value)
	}

	// 传递网关层提取的元数据到底层服务
	if clientIP, ok := headers["client_ip"]; ok && clientIP != "" {
		reqHead.AddHeader("X-Client-Ip", clientIP)
	}
	if traceID, ok := headers["trace_id"]; ok && traceID != "" {
		reqHead.AddHeader("X-Trace-Id", traceID)
	}
	if userAgent, ok := headers["user_agent"]; ok && userAgent != "" {
		reqHead.AddHeader("User-Agent", userAgent)
	}
	if accessToken, ok := headers["access_token"]; ok && accessToken != "" {
		reqHead.AddHeader("X-Access-Token", accessToken)
	}
	return reqHead
}
