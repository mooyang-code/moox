package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/mux"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// ============================================================================
// 网关管理器
// ============================================================================

var (
	gatewayHandleInstance *GatewayHandle
	gatewayHandleOnce     sync.Once
)

// GatewayHandle 网关处理器（保留单例以承载 HTTPRequestHandler 与健康检查服务列表）。
type GatewayHandle struct {
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
		requestHandler: NewHTTPRequestHandler(),
	}
}

// GetConfiguredServiceIDs 获取 gateway.yaml 配置的全部 serviceID（供健康检查展示）。
func (g *GatewayHandle) GetConfiguredServiceIDs() []string {
	cfg := GetConfig()
	if cfg == nil {
		return nil
	}
	return cfg.GetAllServiceIDs()
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
	router := hr.buildRouter()
	thttp.RegisterNoProtocolServiceMux(s.Service("trpc.moox.gateway.stdhttp"), router)
}

func (hr *HTTPRouter) buildRouter() *mux.Router {
	router := mux.NewRouter()

	// 注册新控制台 API 路由: /api/admin/{service}/{method}
	router.HandleFunc(
		"/api/admin/{service}/{method}",
		hr.handleControlRequest).
		Methods("GET", "POST", "PUT", "DELETE")

	// 注册后台服务 API 路由: /api/service/{service}/{method}
	router.HandleFunc(
		"/api/service/{service}/{method}",
		hr.handleServiceRequest).
		Methods("GET", "POST", "PUT", "DELETE")

	// 健康检查接口
	router.HandleFunc("/api/admin/health", hr.handleHealthCheck).Methods("GET")
	return router
}

// handleControlRequest 处理管理台网关请求(中间件authorize通过之后，执行流才到本函数)
func (hr *HTTPRouter) handleControlRequest(w http.ResponseWriter, r *http.Request) {
	hr.handleGatewayRequest(w, r, false)
}

// handleServiceRequest 处理后台服务请求，使用 Auth HMAC 签名鉴权。
func (hr *HTTPRouter) handleServiceRequest(w http.ResponseWriter, r *http.Request) {
	hr.handleGatewayRequest(w, r, true)
}

func (hr *HTTPRouter) handleGatewayRequest(w http.ResponseWriter, r *http.Request, requireServiceAuth bool) {
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
	// user_id 由 authorize filter 从 JWT 解析后写入 ctx（model.CtxUserID），
	// 这里取出透传给下游 trade 等需要按用户隔离的服务。
	if uid, ok := ctx.Value(authmodel.CtxUserID).(string); ok && uid != "" {
		headers["user_id"] = uid
	}

	// 裸 HTTP 处理器分派（用于 multipart/流式等不适合 PB RPC 的场景）。
	// 必须在读取请求体之前分派，避免 multipart body 被网关读干。
	// 仅管理台侧（JWT）支持裸处理器；后台服务侧（HMAC）需先读 body 验签，不走此路径。
	if !requireServiceAuth {
		if rawAndServe(ctx, w, r, serviceID, method, headers) {
			return
		}
	}

	// 读取请求体
	rawBody, body, err := handler.readRequestBodyWithRaw(r)
	if err != nil {
		log.ErrorContextf(ctx, "读取请求体失败: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if requireServiceAuth {
		if err := handler.validateServiceAuth(r, rawBody); err != nil {
			log.WarnContextf(ctx, "后台服务请求鉴权失败: %v", err)
			http.Error(w, "service auth failed", http.StatusUnauthorized)
			return
		}
	}

	// 纯透传到目标服务的有协议 http 端口（本进程服务 / 远端 storage），
	// 框架服务端自动 JSON↔PB，网关不加工 body；未配置 serviceID 返回 404。
	respBody, err := forwardHTTP(ctx, serviceID, method, body, headers)
	if err != nil {
		writeForwardError(ctx, w, err, headers)
		return
	}
	writeForwardResponse(w, respBody, headers)
}

// handleHealthCheck 处理健康检查请求
func (hr *HTTPRouter) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":   "ok",
		"time":     time.Now().Format("2006-01-02 15:04:05"),
		"services": hr.gateway.GetConfiguredServiceIDs(),
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

// readRequestBodyWithRaw 读取原始请求体，同时返回合并 URL query 参数后的转发请求体。
// 优先级：body 中的参数 > URL query 参数。
func (h *HTTPRequestHandler) readRequestBodyWithRaw(r *http.Request) ([]byte, []byte, error) {
	// 读取 body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %v", err)
	}
	defer r.Body.Close()
	rawBody := append([]byte(nil), body...)

	// 解析 URL query 参数
	queryParams := r.URL.Query()
	if len(queryParams) == 0 {
		return rawBody, body, nil
	}

	// 将 query 参数转为 map
	queryMap := make(map[string]interface{})
	for key, values := range queryParams {
		if len(values) == 1 {
			queryMap[key] = values[0]
		} else {
			queryMap[key] = values
	}
}

	// 如果 body 为空，直接使用 query 参数
	if len(body) == 0 {
		mergedBody, err := json.Marshal(queryMap)
		return rawBody, mergedBody, err
	}

	// 如果 body 不为空，尝试合并
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(body, &bodyMap); err != nil {
		// body 不是有效 JSON，直接返回 body
		return rawBody, body, nil
	}

	// 合并：body 参数优先（覆盖 query 参数）
	for key, value := range bodyMap {
		queryMap[key] = value
	}

	mergedBody, err := json.Marshal(queryMap)
	return rawBody, mergedBody, err
}

func (h *HTTPRequestHandler) validateServiceAuth(r *http.Request, rawBody []byte) error {
	cfg, err := currentServiceAuthConfig()
	if err != nil {
		return err
	}
	return validateServiceAuthHeader(r.Header.Get("Auth"), rawBody, time.Now(), cfg)
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
	if origin := r.Header.Get("Origin"); origin != "" {
		headers["origin"] = origin
	}
	// space_id：硬隔离维度，透传给已迁移的 RPC 服务（spacecontext 从 ctx 读取）
	if spaceID := r.Header.Get("X-Space-Id"); spaceID != "" {
		headers["space_id"] = spaceID
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
