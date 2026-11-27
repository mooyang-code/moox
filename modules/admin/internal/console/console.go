package console

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/mux"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/log"
)

// ============================================================================
// 控制台处理器
// ============================================================================

// ConsoleHandle 控制台处理器，承载 HTTPRequestHandler。
type ConsoleHandle struct {
	requestHandler *HTTPRequestHandler
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
	console        *ConsoleHandle
	catalog        servicecatalog.Catalog
	local          *LocalDispatcher
	gateway        RPCForwarder
	sessions       *auth.SessionStore
	limiters       methodLimiters
	allowedOrigins []string
}

// NewHTTPRouter 创建控制台路由；sessions 是与认证服务共用的会话表，限额与开发来源取 SetConfig 设置的配置。
func NewHTTPRouter(console *ConsoleHandle, local *LocalDispatcher, gateway RPCForwarder, sessions *auth.SessionStore) (*HTTPRouter, error) {
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	if console == nil || sessions == nil {
		return nil, errors.New("console handle and session store are required")
	}
	cfg := GetConfig()
	var allowedOrigins []string
	if cfg != nil {
		allowedOrigins = cfg.Console.AllowedOrigins
	}
	return &HTTPRouter{
		console: console, catalog: catalog, local: local, gateway: gateway, sessions: sessions,
		limiters: newMethodLimiters(cfg), allowedOrigins: allowedOrigins,
	}, nil
}

// NewHandler 在两个分派器都就绪后建浏览器接口 /api/admin/{服务}/{方法} 的处理器，交给 Admin 的浏览器入口（9527）；
// sessions 是与认证服务共用的会话表。
func NewHandler(local *LocalDispatcher, gateway RPCForwarder, sessions *auth.SessionStore) (http.Handler, error) {
	if GetConfig() == nil {
		return nil, errors.New("控制台配置未初始化")
	}
	if local == nil || gateway == nil || sessions == nil {
		return nil, errors.New("控制台需要本地分派器、网关客户端及会话表")
	}
	router, err := NewHTTPRouter(NewConsoleHandle(), local, gateway, sessions)
	if err != nil {
		return nil, err
	}
	return router.buildControlRouter(), nil
}

func (hr *HTTPRouter) buildControlRouter() *mux.Router {
	router := mux.NewRouter()
	router.MethodNotAllowedHandler = http.NotFoundHandler()
	// 控制台 API 路由: /api/admin/{service}/{method}。RPC 只收 POST；GET 只给裸路由（终端握手、下载、预览）。
	router.HandleFunc("/api/admin/{service}/{method}", hr.handleConsoleRequest).Methods(http.MethodGet, http.MethodPost)
	return router
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
	route := serviceID + "/" + method
	// 限流先于鉴权：免会话的 Login 也必须受限。
	if !hr.limiters.allow(ctx, apiAdminPathPrefix+route) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	// RPC 别名与权限只来自服务目录；SSH 流式路由与归档下载、最新数据是另一组固定的裸路由。
	rawRoute := rawRoutes[route]
	spec, allowed := hr.catalog.ConsoleService(serviceID, method)
	if rawRoute {
		if _, registered := LookupRawHandler(serviceID, method); !registered {
			http.NotFound(w, r)
			return
		}
	} else if !allowed {
		http.NotFound(w, r)
		return
	} else if r.Method != http.MethodPost {
		// GET 不带请求体也能触发写方法，同站页面的链接、图片就能借浏览器的 Cookie 发起，所以 RPC 只收 POST。
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if needsOriginCheck(r, route) && !originAllowed(r, hr.allowedOrigins) {
		log.WarnContextf(ctx, "console request origin rejected route=%s origin=%q host=%q", route, r.Header.Get("Origin"), r.Host)
		writeOriginRejected(w)
		return
	}

	var current auth.Session
	if route != loginRoute {
		found, ok := hr.requestSession(r)
		if !ok {
			writeAdminAuthFailure(w)
			return
		}
		current = found
		ctx = context.WithValue(ctx, auth.CtxUserID, current.UserID)
		r = r.WithContext(ctx)
		trpc.SetMetaData(ctx, auth.CtxUserID, []byte(current.UserID))
		trpc.SetMetaData(ctx, auth.CtxUsername, []byte(current.Username))
		trpc.SetMetaData(ctx, auth.CtxSessionID, []byte(current.ID))
	}
	headers := handler.extractGatewayHeaders(r)
	if current.UserID != "" {
		// 透传给下游 trade 等需要按用户隔离的服务。
		headers["user_id"] = current.UserID
	}

	ctx = gatewayclient.WithCallMetadata(ctx, gatewayclient.CallMetadata{
		SpaceID: headers["space_id"], UserID: headers["user_id"], TraceID: headers["trace_id"],
	})
	if rawRoute {
		if !rawAndServe(ctx, w, r, serviceID, method) {
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
	authorizedSpace, injectSpace, status, err := hr.authorizeRequest(r, serviceID, method, body)
	if err != nil {
		log.WarnContextf(ctx, "console request denied service=%s method=%s: %v", serviceID, method, err)
		if status == http.StatusBadRequest {
			http.Error(w, err.Error(), status)
		} else {
			http.Error(w, "access denied", status)
		}
		return
	}
	// 按授权结果统一同步下游元数据空间：纯全局方法会清空误带的 X-Space-Id，
	// 请求头未带空间而由请求体声明时则把授权通过的空间写回下游元数据。
	if headers["space_id"] != authorizedSpace {
		headers["space_id"] = authorizedSpace
		ctx = gatewayclient.WithCallMetadata(ctx, gatewayclient.CallMetadata{
			SpaceID: authorizedSpace, UserID: headers["user_id"], TraceID: headers["trace_id"],
		})
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
	if serviceID == "storage" && injectSpace {
		injected, err := injectStorageSpace(body, authorizedSpace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body = injected
	}
	var login *auth.LoginContext
	if route == loginRoute {
		login = &auth.LoginContext{ClientIP: clientIP(r), UserAgent: r.UserAgent()}
		ctx = auth.WithLogin(ctx, login)
	}
	response, err := target.Forward(ctx, spec.Path, method, codec.SerializationTypeJSON, body)
	if route == logoutRoute {
		// 不管服务端删没删成，浏览器这边都退出。
		clearSessionCookie(w)
	}
	if err != nil {
		writeForwardError(ctx, w, err, headers)
		return
	}
	if login != nil && login.Issued != nil {
		setSessionCookie(w, *login.Issued, int(hr.sessions.TTL().Seconds()))
	}
	writeForwardResponse(w, response, headers)
}

// authorizeRequest 在转发前按方法级三档空间策略（global / space_optional / space_required）定下请求作用的空间（可能来自请求体），
// 仅核对请求头与请求体里的空间一致；系统只有一个超级用户，空间是否存在、是否停用不在这里判断（见管理后台.md 已知差别）。
func (hr *HTTPRouter) authorizeRequest(r *http.Request, serviceID, method string, body []byte) (string, bool, int, error) {
	policy := methodSpacePolicy(serviceID, method)
	if policy == spacePolicyGlobal {
		return "", false, 0, nil
	}
	var spaceID string
	var inject bool
	var err error
	switch {
	case serviceID == "storage":
		spaceID, inject, err = requestBodySpace(r.Header.Get("X-Space-Id"), body, storageTopLevelSpaceMethods[method])
	default:
		spaceID, err = effectiveSpaceID(r.Header.Get("X-Space-Id"), body)
	}
	if err != nil {
		return "", false, http.StatusBadRequest, err
	}
	if spaceID == "" {
		if policy == spacePolicyRequired {
			return "", false, http.StatusForbidden, errors.New("space_id is required")
		}
		return "", false, 0, nil
	}
	return spaceID, inject, 0, nil
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

// readRequestBodyWithRaw 原样读取 RPC 请求体；query 参数不会变成 RPC 字段。
func (h *HTTPRequestHandler) readRequestBodyWithRaw(r *http.Request) ([]byte, []byte, error) {
	body, err := readBoundedBody(r.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("读取请求体失败: %w", err)
	}
	defer r.Body.Close()
	rawBody := append([]byte(nil), body...)
	return rawBody, body, nil
}

// extractGatewayHeaders 取透传给下游与写响应要用的请求头。用户与会话只来自 Cookie，请求头里的一概不认。
func (h *HTTPRequestHandler) extractGatewayHeaders(r *http.Request) map[string]string {
	headers := make(map[string]string)
	if traceID := r.Header.Get("X-Trace-Id"); traceID != "" {
		headers["trace_id"] = traceID
	}
	if acceptEncoding := r.Header.Get("Accept-Encoding"); acceptEncoding != "" {
		headers["accept_encoding"] = acceptEncoding
	}
	// space_id：硬隔离维度，经 gatewayclient 元数据透传给下游业务服务
	if spaceID := strings.TrimSpace(r.Header.Get("X-Space-Id")); spaceID != "" {
		headers["space_id"] = spaceID
	}
	return headers
}
