// Package console 是控制台 API（trpc.moox.admin.Console，11000）：浏览器的 /api/admin/<console_name>/<方法>
// 经控制台代理进入这里，完成会话鉴权、请求签名、限流和空间授权后，按组件目录转发（设计文档 3.8）：
//   - 管理后台自己的服务在进程内直接调用，不经过主机网关；
//   - 其他服务经 gatewayclient.Forward 以 console 身份透传 JSON；
//   - 浏览器能调用哪些方法，只看组件目录的 ACL 是否放行 console；
//   - SSH 终端与 SFTP 上传下载仍由进程内的原始处理器处理 HTTP/WebSocket。
package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/healthz"
	"github.com/mooyang-code/moox/packages/requestauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/errs"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"
)

// ServiceName 是控制台 API 的 tRPC 服务名。
const ServiceName = "trpc.moox.admin.Console"

// 转发给其他组件的元数据键：主机网关只丢弃 x-moox- 开头的键，这些键原样到达目标服务。
const (
	MetadataSpaceID  = "x-space-id"
	MetadataUserID   = "x-user-id"
	MetadataUserRole = "x-user-role"
	MetadataTraceID  = "x-trace-id"
)

// TradeSpaceAuthorizer 是控制台对空间作用域请求的授权：请求头里的空间不可信，实现必须核实
// 用户能否在该空间执行该方法。
type TradeSpaceAuthorizer interface {
	AuthorizeTradeRequest(ctx context.Context, userID, spaceID, method string, globalRole int32) error
}

// Forwarder 是转发到其他组件的客户端（gatewayclient，console 身份）。
type Forwarder interface {
	Forward(ctx context.Context, servicePath, method string, serialization int, body []byte, opts ...gatewayclient.CallOption) ([]byte, error)
}

// Options 是控制台的依赖。
type Options struct {
	Catalog    *servicecatalog.Catalog
	Local      *LocalServices
	Remote     Forwarder
	Authorizer TradeSpaceAuthorizer
}

// Router 处理 /api/admin/*。
type Router struct {
	catalog    *servicecatalog.Catalog
	local      *LocalServices
	remote     Forwarder
	authorizer TradeSpaceAuthorizer
}

// NewRouter 创建控制台路由。
func NewRouter(options Options) *Router {
	catalog := options.Catalog
	if catalog == nil {
		catalog = servicecatalog.Default()
	}
	local := options.Local
	if local == nil {
		local = NewLocalServices()
	}
	return &Router{catalog: catalog, local: local, remote: options.Remote, authorizer: options.Authorizer}
}

// Register 把控制台 API 注册到 trpc.moox.admin.Console 服务（http_no_protocol）上。
func Register(s *server.Server, options Options) error {
	if GetConfig() == nil {
		return fmt.Errorf("控制台配置未初始化")
	}
	return healthz.RegisterNoProtocolServiceMux(s.Service(ServiceName), NewRouter(options).Handler())
}

// Handler 返回控制台的 HTTP 处理器。
func (r *Router) Handler() http.Handler {
	router := mux.NewRouter()
	router.MethodNotAllowedHandler = http.NotFoundHandler()
	router.Handle("/api/admin/{service}/{method}", corsMiddleware(http.HandlerFunc(r.serve))).
		Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	return router
}

// spaceScopedConsoleNames 是带空间作用域的浏览器服务：请求必须带 X-Space-Id，并通过空间授权。
var spaceScopedConsoleNames = map[string]bool{"trade": true, "strategy": true, "collector": true, "cloudnode": true}

func (r *Router) serve(w http.ResponseWriter, req *http.Request) {
	ctx := req.Context()
	vars := mux.Vars(req)
	consoleName, method := strings.TrimSpace(vars["service"]), strings.TrimSpace(vars["method"])
	if consoleName == "" || method == "" {
		http.Error(w, "请求错误：未提供有效的服务名和方法名", http.StatusBadRequest)
		return
	}
	path := req.URL.EscapedPath()
	if !allowRequest(ctx, path) {
		writeJSONResult(w, http.StatusTooManyRequests, createRateLimitResponse())
		return
	}
	_, isRaw := LookupRawHandler(consoleName, method)
	servicePath, routed := r.catalog.ConsoleTarget(consoleName, method)
	if !isRaw && !routed {
		http.NotFound(w, req)
		return
	}

	headers := extractConsoleHeaders(req)
	if !shouldSkipAdminRequestAuth(path) {
		if _, usesTicket := rawRouteOperations[consoleName+"/"+method]; usesTicket {
			claims, err := validateRawRouteTicket(req, consoleName, method)
			if err != nil {
				log.WarnContextf(ctx, "控制台原始请求鉴权失败: %v", err)
				writeAdminAuthFailure(w)
				return
			}
			ctx = context.WithValue(ctx, authmodel.CtxUserID, claims.UserID)
			trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte(claims.UserID))
			trpc.SetMetaData(ctx, authmodel.CtxSessionID, []byte(claims.SessionID))
			headers.userID = claims.UserID
		} else {
			rawBody, err := readAndRestoreBody(req)
			if err != nil {
				writeRequestBodyError(w, err)
				return
			}
			claims, err := verifyAdminRequest(req, rawBody)
			if err != nil {
				log.WarnContextf(ctx, "控制台请求鉴权失败 sid=%s reason=%v", sessionIDForLog(req), err)
				writeAdminAuthFailure(w)
				return
			}
			ctx = context.WithValue(ctx, authmodel.CtxUserID, claims.UserID)
			trpc.SetMetaData(ctx, authmodel.CtxUserID, []byte(claims.UserID))
			trpc.SetMetaData(ctx, authmodel.CtxUsername, []byte(claims.Username))
			trpc.SetMetaData(ctx, authmodel.CtxUserRole, []byte(strconv.Itoa(int(claims.Role))))
			trpc.SetMetaData(ctx, authmodel.CtxSessionID, []byte(claims.SessionID))
			headers.userID, headers.userRole = claims.UserID, strconv.Itoa(int(claims.Role))
		}
		req = req.WithContext(ctx)
	}
	if consoleName == "storage" && method == "RequestViewRebuild" {
		if role, err := strconv.Atoi(headers.userRole); err != nil || role < 2 {
			log.WarnContextf(ctx, "手动重建视图被拒绝：需要管理员角色")
			http.Error(w, "administrator role required", http.StatusForbidden)
			return
		}
	}
	if spaceScopedConsoleNames[consoleName] {
		if err := r.authorizeSpaceRequest(ctx, headers, method); err != nil {
			log.WarnContextf(ctx, "空间作用域请求被拒绝: %v", err)
			http.Error(w, "space access denied", http.StatusForbidden)
			return
		}
	}

	// 原始处理器必须在读取请求体之前分派，避免 multipart 请求体被读干。
	if rawAndServe(ctx, w, req, consoleName, method) {
		return
	}
	body, err := readBoundedBody(req.Body)
	if err != nil {
		writeRequestBodyError(w, err)
		return
	}
	if spaceScopedConsoleNames[consoleName] {
		if err := validateSpaceScopedBody(headers.spaceID, body); err != nil {
			writeForwardError(ctx, w, errs.New(int(pb.ErrorCode_INVALID_PARAM), err.Error()), headers)
			return
		}
	}
	if consoleName == "storage" {
		if body, err = storageBFFBody(servicePath, body); err != nil {
			writeForwardError(ctx, w, err, headers)
			return
		}
	}
	var response []byte
	if r.local.Has(servicePath) {
		response, err = r.local.Invoke(ctx, servicePath, method, body)
	} else {
		response, err = r.forward(ctx, servicePath, method, body, headers)
	}
	if err != nil {
		writeForwardError(ctx, w, err, headers)
		return
	}
	writeForwardResponse(w, response, headers)
}

func (r *Router) forward(ctx context.Context, servicePath, method string, body []byte, headers consoleHeaders) ([]byte, error) {
	if r.remote == nil {
		return nil, errors.New("控制台没有配置 gateway_client，无法转发到其他组件")
	}
	options := []gatewayclient.CallOption{gatewayclient.WithTimeout(r.forwardTimeout(servicePath))}
	for key, value := range map[string]string{
		MetadataSpaceID: headers.spaceID, MetadataUserID: headers.userID, MetadataUserRole: headers.userRole, MetadataTraceID: headers.traceID,
	} {
		if value != "" {
			options = append(options, gatewayclient.WithMetadata(key, []byte(value)))
		}
	}
	return r.remote.Forward(ctx, servicePath, method, codec.SerializationTypeJSON, body, options...)
}

// forwardTimeout 比路由超时多留 5 秒，让主机网关先给出明确的超时错误。
func (r *Router) forwardTimeout(servicePath string) time.Duration {
	timeout := 5 * time.Second
	if service, _, ok := r.catalog.Service(servicePath); ok && service.TimeoutMS > 0 {
		timeout = time.Duration(service.TimeoutMS) * time.Millisecond
	}
	return timeout + 5*time.Second
}

func (r *Router) authorizeSpaceRequest(ctx context.Context, headers consoleHeaders, method string) error {
	if r.authorizer == nil {
		return errors.New("空间授权不可用")
	}
	userID := strings.TrimSpace(headers.userID)
	if userID == "" {
		return errors.New("需要已登录的用户")
	}
	if headers.spaceID == "" {
		return errors.New("缺少 space_id")
	}
	role, _ := strconv.ParseInt(headers.userRole, 10, 32)
	return r.authorizer.AuthorizeTradeRequest(ctx, userID, headers.spaceID, method, int32(role))
}

// validateSpaceScopedBody 防止请求头声明一个空间、请求体里嵌套另一个空间。
func validateSpaceScopedBody(header string, body []byte) error {
	header = strings.TrimSpace(header)
	if header == "" || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return fmt.Errorf("空间作用域请求体无效: %w", err)
	}
	var walk func(any) error
	walk = func(item any) error {
		switch node := item.(type) {
		case map[string]any:
			for key, child := range node {
				if strings.EqualFold(key, "space_id") || strings.EqualFold(key, "spaceId") {
					if value, ok := child.(string); ok && strings.TrimSpace(value) != "" && strings.TrimSpace(value) != header {
						return fmt.Errorf("space_id %q 与 X-Space-Id %q 不一致", value, header)
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

// consoleHeaders 是控制台从浏览器请求和登录态中取出的信息。
type consoleHeaders struct {
	spaceID, traceID, origin, acceptEncoding string
	userID, userRole                         string
}

func extractConsoleHeaders(r *http.Request) consoleHeaders {
	return consoleHeaders{
		spaceID: strings.TrimSpace(r.Header.Get(requestauth.HeaderSpaceID)), traceID: strings.TrimSpace(r.Header.Get("X-Trace-Id")),
		origin: r.Header.Get("Origin"), acceptEncoding: r.Header.Get("Accept-Encoding"),
	}
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

func writeJSONResult(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
