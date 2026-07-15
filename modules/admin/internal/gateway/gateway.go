package gateway

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	adminhealth "github.com/mooyang-code/moox/modules/admin/internal/health"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/requestauth"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// ============================================================================
// 网关管理器
// ============================================================================

var (
	gatewayHandleInstance *GatewayHandle
	gatewayHandleOnce     sync.Once
	gatewayStartedAt      = time.Now()
)

// GatewayHandle 网关处理器（保留单例以承载 HTTPRequestHandler）。
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

// ============================================================================
// HTTP路由注册
// ============================================================================

// HTTPRouter HTTP路由管理器
type HTTPRouter struct {
	gateway              *GatewayHandle
	controlProvider      GatewayProvider
	adminServiceProvider AdminServiceDetailProvider
	adminNodeID          string
}

// NewHTTPRouter 创建HTTP路由管理器
func NewHTTPRouter(gateway *GatewayHandle, provider GatewayProvider, adminNodeID string) *HTTPRouter {
	return &HTTPRouter{gateway: gateway, controlProvider: provider, adminServiceProvider: provider, adminNodeID: adminNodeID}
}

// RegisterGatewayHTTPHandlers 注册网关HTTP接口
func RegisterGatewayHTTPHandlers(s *server.Server, provider GatewayProvider, adminNodeID string) error {
	gateway := GetGatewayHandleInstance()
	router := NewHTTPRouter(gateway, provider, adminNodeID)
	return router.setupRoutes(s)
}

// setupRoutes 设置路由
func (hr *HTTPRouter) setupRoutes(s *server.Server) error {
	if err := healthz.RegisterNoProtocolServiceMux(s.Service("trpc.moox.gateway.control"), hr.buildControlRouter()); err != nil {
		return err
	}
	return nil
}

func (hr *HTTPRouter) buildControlRouter() *mux.Router {
	router := mux.NewRouter()
	router.MethodNotAllowedHandler = http.NotFoundHandler()
	if hr.controlProvider != nil {
		router.HandleFunc("/api/gateway-control/routes", hr.handleGatewayRoutes).Methods(http.MethodGet)
		router.HandleFunc("/api/gateway-control/status", hr.handleGatewayStatus).Methods(http.MethodPost)
	}

	// 注册新控制台 API 路由: /api/admin/{service}/{method}
	router.HandleFunc(
		"/api/admin/{service}/{method}",
		hr.handleControlRequest).
		Methods("GET", "POST", "PUT", "DELETE")

	return router
}

// buildRouter remains the control-router test helper.
func (hr *HTTPRouter) buildRouter() *mux.Router { return hr.buildControlRouter() }

// handleControlRequest 处理管理台网关请求(中间件authorize通过之后，执行流才到本函数)
func (hr *HTTPRouter) handleControlRequest(w http.ResponseWriter, r *http.Request) {
	hr.handleGatewayRequest(w, r)
}

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
	if isMachineOnlyAdminMethod(serviceID, method) {
		http.NotFound(w, r)
		return
	}

	if !shouldSkipAdminRequestAuth(r.URL.EscapedPath()) {
		if _, usesTicket := rawRouteOperations[serviceID+"/"+method]; usesTicket {
			claims, err := validateRawRouteTicket(r, serviceID, method)
			if err != nil {
				log.WarnContextf(ctx, "admin raw request authentication failed: %v", err)
				writeAdminAuthFailure(w)
				return
			}
			ctx = context.WithValue(ctx, authmodel.CtxUserID, claims.UserID)
			r = r.WithContext(ctx)
			trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte(claims.UserID))
			trpc.SetMetaData(ctx, authmodel.CtxSessionID, []byte(claims.SessionID))
		} else {
			rawBody, err := readAndRestoreBody(r)
			if err != nil {
				writeAdminAuthFailure(w)
				return
			}
			claims, err := verifyAdminRequest(r, rawBody)
			if err != nil {
				log.WarnContextf(ctx, "admin request authentication failed sid=%s reason=%v", sessionIDForLog(r), err)
				writeAdminAuthFailure(w)
				return
			}
			ctx = context.WithValue(ctx, authmodel.CtxUserID, claims.UserID)
			r = r.WithContext(ctx)
			trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte(claims.UserID))
			trpc.SetMetaData(ctx, authmodel.CtxUsername, []byte(claims.Username))
			trpc.SetMetaData(ctx, authmodel.CtxUserRole, []byte(fmt.Sprintf("%d", claims.Role)))
			trpc.SetMetaData(ctx, authmodel.CtxSessionID, []byte(claims.SessionID))
		}
	}

	// 提取HTTP头部信息
	headers := handler.extractGatewayHeaders(r)
	// user_id 由 authorize filter 从 JWT 解析后写入 ctx（model.CtxUserID），
	// 这里取出透传给下游 trade 等需要按用户隔离的服务。
	if uid, ok := ctx.Value(authmodel.CtxUserID).(string); ok && uid != "" {
		headers["user_id"] = uid
	}
	if role := string(trpc.GetMetaData(ctx, authmodel.CtxUserRole)); role != "" {
		headers["user_role"] = role
	}

	// 裸 HTTP 处理器分派（用于 multipart/流式等不适合 PB RPC 的场景）。
	// 必须在读取请求体之前分派，避免 multipart body 被网关读干。
	// 裸处理器仍使用管理台 JWT 或一次性 raw ticket 鉴权。
	if rawAndServe(ctx, w, r, serviceID, method, headers) {
		return
	}

	// 读取请求体
	_, body, err := handler.readRequestBodyWithRaw(r)
	if err != nil {
		log.ErrorContextf(ctx, "读取请求体失败: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// 纯透传到目标服务的有协议 http 端口（本进程服务 / 远端 storage），
	// 框架服务端自动 JSON↔PB，网关不加工 body；未配置 serviceID 返回 404。
	respBody, err := forwardHTTP(ctx, hr.adminServiceProvider, hr.adminNodeID, serviceID, method, body, headers)
	if err != nil {
		writeForwardError(ctx, w, err, headers)
		return
	}
	writeForwardResponse(w, respBody, headers)
}

func isMachineOnlyAdminMethod(serviceID, method string) bool {
	if canonicalAdminSegment(method) != "revealsecret" {
		return false
	}
	switch canonicalAdminSegment(serviceID) {
	case "secret", "secretmgr", "trpcmooxopssecretmgr":
		return true
	default:
		return false
	}
}

func canonicalAdminSegment(value string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '.', '-', '_':
			return -1
		default:
			return r
		}
	}, strings.ToLower(strings.TrimSpace(value)))
}

// handleHealthCheck 处理健康检查请求
func (hr *HTTPRouter) handleHealthCheck(w http.ResponseWriter, r *http.Request) {
	adminhealth.Handler(gatewayStartedAt).ServeHTTP(w, r)
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

// readRequestBodyWithRaw reads the exact RPC body. Query parameters are not
// converted into RPC fields because doing so would create unsigned input.
func (h *HTTPRequestHandler) readRequestBodyWithRaw(r *http.Request) ([]byte, []byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %v", err)
	}
	defer r.Body.Close()
	rawBody := append([]byte(nil), body...)
	return rawBody, body, nil
}

func signedGatewayHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)
	for _, name := range []string{requestauth.HeaderAppID, requestauth.HeaderAppKey, requestauth.HeaderSpaceID} {
		if value := r.Header.Get(name); value != "" {
			headers[name] = value
		}
	}
	return headers
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
	if acceptEncoding := r.Header.Get("Accept-Encoding"); acceptEncoding != "" {
		headers["accept_encoding"] = acceptEncoding
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
