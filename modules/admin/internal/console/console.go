package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/gorilla/mux"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/spacecontext"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// ============================================================================
// 控制台处理器
// ============================================================================

var (
	consoleHandleInstance *ConsoleHandle
	consoleHandleOnce     sync.Once
)

// ConsoleHandle 控制台处理器（保留单例以承载 HTTPRequestHandler）。
type ConsoleHandle struct {
	requestHandler *HTTPRequestHandler
}

// GetConsoleHandleInstance 返回控制台处理器的全局单例实例
func GetConsoleHandleInstance() *ConsoleHandle {
	consoleHandleOnce.Do(func() {
		consoleHandleInstance = NewConsoleHandle()
	})
	return consoleHandleInstance
}

var NewConsoleHandle = func() *ConsoleHandle {
	return &ConsoleHandle{
		requestHandler: NewHTTPRequestHandler(),
	}
}

// ============================================================================
// HTTP路由注册
// ============================================================================

// HTTPRouter HTTP路由管理器
type HTTPRouter struct {
	console         *ConsoleHandle
	catalog         servicecatalog.Catalog
	local           *LocalDispatcher
	gateway         RPCForwarder
	tradeAuthorizer TradeSpaceAuthorizer
}

func NewHTTPRouter(console *ConsoleHandle, local *LocalDispatcher, gateway RPCForwarder, authorizers ...TradeSpaceAuthorizer) (*HTTPRouter, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	if console == nil {
		return nil, errors.New("console handle is required")
	}
	var authorizer TradeSpaceAuthorizer
	if len(authorizers) > 0 {
		authorizer = authorizers[0]
	}
	return &HTTPRouter{console: console, catalog: catalog, local: local, gateway: gateway, tradeAuthorizer: authorizer}, nil
}

func RegisterConsoleHTTPHandlers(s *server.Server, local *LocalDispatcher, gateway RPCForwarder, authorizers ...TradeSpaceAuthorizer) error {
	router, err := NewHTTPRouter(GetConsoleHandleInstance(), local, gateway, authorizers...)
	if err != nil {
		return err
	}
	return router.setupRoutes(s)
}

// setupRoutes 设置路由
func (hr *HTTPRouter) setupRoutes(s *server.Server) error {
	if err := healthz.RegisterNoProtocolServiceMux(s.Service("trpc.moox.admin.Console"), hr.buildControlRouter()); err != nil {
		return err
	}
	return nil
}

func (hr *HTTPRouter) buildControlRouter() *mux.Router {
	router := mux.NewRouter()
	router.MethodNotAllowedHandler = http.NotFoundHandler()
	// 注册新控制台 API 路由: /api/admin/{service}/{method}
	router.Handle(
		"/api/admin/{service}/{method}",
		corsMiddleware(http.HandlerFunc(hr.handleControlRequest))).
		Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")

	return router
}

// handleControlRequest 处理控制台请求(中间件authorize通过之后，执行流才到本函数)
func (hr *HTTPRouter) handleControlRequest(w http.ResponseWriter, r *http.Request) {
	hr.handleConsoleRequest(w, r)
}

func (hr *HTTPRouter) handleConsoleRequest(w http.ResponseWriter, r *http.Request) {
	ctx, message := codec.WithCloneMessage(r.Context())
	defer codec.PutBackMessage(message)
	r = r.WithContext(ctx)
	handler := hr.console.requestHandler

	// 解析请求参数
	serviceID, method, err := handler.parseRequestParams(r)
	if err != nil {
		log.ErrorContextf(ctx, "解析请求参数失败: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// RPC aliases and permissions come only from the shared catalog. SSH
	// streaming routes are a separate fixed surface bound to one-time tickets.
	_, rawRoute := rawRouteOperations[serviceID+"/"+method]
	spec, allowed := hr.catalog.ConsoleService(serviceID, method)
	if rawRoute {
		if _, registered := LookupRawHandler(serviceID, method); !registered {
			http.NotFound(w, r)
			return
		}
	} else if !allowed {
		http.NotFound(w, r)
		return
	}

	if rawRoute || !shouldSkipAdminRequestAuth(r.URL.EscapedPath()) {
		if rawRoute {
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
				writeRequestBodyError(w, err)
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
	if serviceID == "storage" && method == "RequestViewRebuild" {
		role, err := strconv.ParseInt(string(trpc.GetMetaData(ctx, authmodel.CtxUserRole)), 10, 32)
		if err != nil || role < 2 {
			log.WarnContextf(ctx, "manual view rebuild denied: administrator role required")
			http.Error(w, "administrator role required", http.StatusForbidden)
			return
		}
	}
	if isSpaceScopedService(serviceID) {
		if err := hr.authorizeSpaceRequest(ctx, r, method); err != nil {
			log.WarnContextf(ctx, "space-scoped request denied: %v", err)
			http.Error(w, "space access denied", http.StatusForbidden)
			return
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

	ctx = spacecontext.InjectFromHeaders(ctx, headers)
	ctx = gatewayclient.WithCallMetadata(ctx, gatewayclient.CallMetadata{
		SpaceID: headers["space_id"], UserID: headers["user_id"], UserRole: headers["user_role"], TraceID: headers["trace_id"],
	})
	if rawRoute {
		if !rawAndServe(ctx, w, r, serviceID, method, headers) {
			http.NotFound(w, r)
		}
		return
	}

	// 读取请求体
	_, body, err := handler.readRequestBodyWithRaw(r)
	if err != nil {
		log.ErrorContextf(ctx, "读取请求体失败: %v", err)
		writeRequestBodyError(w, err)
		return
	}
	if isSpaceScopedService(serviceID) {
		if err := validateSpaceScopedBody(r.Header.Get("X-Space-Id"), body); err != nil {
			writeForwardError(ctx, w, err, headers)
			return
		}
	}
	var target RPCForwarder = hr.gateway
	admin, _ := hr.catalog.Component("admin")
	for _, service := range admin.Services {
		if service.Path == spec.Path {
			if hr.local == nil {
				writeForwardError(ctx, w, errors.New("local Admin dispatcher is unavailable"), headers)
				return
			}
			target = hr.local
			break
		}
	}
	if target == nil {
		writeForwardError(ctx, w, errors.New("console gateway is unavailable"), headers)
		return
	}
	response, err := target.Forward(ctx, spec.Path, method, codec.SerializationTypeJSON, body)
	if err != nil {
		writeForwardError(ctx, w, err, headers)
		return
	}
	writeForwardResponse(w, response, headers)
}

func isSpaceScopedService(serviceID string) bool {
	switch serviceID {
	case "trade", "strategy", "collector", "cloudnode":
		return true
	default:
		return false
	}
}

// validateSpaceScopedBody prevents a caller from authenticating one space in
// the header while placing another space in a nested protobuf-JSON request.
// Collector requests use both top-level and nested space_id fields.
func validateSpaceScopedBody(header string, body []byte) error {
	header = strings.TrimSpace(header)
	if header == "" || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return fmt.Errorf("invalid space-scoped request body: %w", err)
	}
	var walk func(any) error
	walk = func(item any) error {
		switch node := item.(type) {
		case map[string]any:
			for key, child := range node {
				if strings.EqualFold(key, "space_id") || strings.EqualFold(key, "spaceId") {
					if value, ok := child.(string); ok && strings.TrimSpace(value) != "" && strings.TrimSpace(value) != header {
						return fmt.Errorf("space_id %q does not match X-Space-Id %q", value, header)
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range node {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(value)
}

func (hr *HTTPRouter) authorizeSpaceRequest(ctx context.Context, r *http.Request, method string) error {
	if hr.tradeAuthorizer == nil {
		return errors.New("trade space authorizer is unavailable")
	}
	userID, _ := ctx.Value(authmodel.CtxUserID).(string)
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return errors.New("authenticated user is required")
	}
	spaceID := strings.TrimSpace(r.Header.Get("X-Space-Id"))
	if spaceID == "" {
		return errors.New("space_id is required")
	}
	role, _ := strconv.ParseInt(string(trpc.GetMetaData(ctx, authmodel.CtxUserRole)), 10, 32)
	return hr.tradeAuthorizer.AuthorizeTradeRequest(ctx, userID, spaceID, method, int32(role))
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
	body, err := readBoundedBody(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %w", err)
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
