package console

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
)

// 浏览器鉴权：每个请求只看会话 Cookie（HttpOnly、Secure、SameSite=Strict）。非 GET 请求和 WebSocket 握手
// 另外要求 Origin 等于本站或配置里的开发来源；缺少 Origin 一律拒绝，命令行客户端要自己带上。
// Origin 不对返回 403，没有会话或会话过期返回 401，前端只把 401 当成掉线。

// middlewareResp 控制台拒绝请求时的响应体，保持 {ret_info} 格式与业务 RPC 一致。
type middlewareResp struct {
	RetInfo *pb.RetInfo `json:"ret_info"`
}

// loginRoute 是唯一不需要会话的接口。写死在这里，不从配置读：配置写错就可能放开别的方法。
const loginRoute = "auth/Login"

// logoutRoute 退出后控制台清除 Cookie。
const logoutRoute = "auth/Logout"

// rawRoutes 是裸 HTTP 路由：三个 SSH 流式路由与归档的下载、最新数据。它们和 RPC 一样按会话 Cookie 鉴权。
var rawRoutes = map[string]bool{
	"ssh/WsConnect": true, "ssh/SftpDownload": true, "ssh/SftpUpload": true,
	"archive/PartitionDownload": true, "archive/RecentRows": true,
}

var rawSessionOwnerState struct {
	sync.RWMutex
	verify func(sessionID, userID string) bool
}

// SetRawSessionOwnerVerifier 登记 SSH 会话归属的判断：SSH 裸路由的 session_id 必须是当前登录用户建的。
func SetRawSessionOwnerVerifier(verify func(sessionID, userID string) bool) {
	rawSessionOwnerState.Lock()
	rawSessionOwnerState.verify = verify
	rawSessionOwnerState.Unlock()
}

func rawSessionBelongsToUser(sessionID, userID string) bool {
	rawSessionOwnerState.RLock()
	verify := rawSessionOwnerState.verify
	rawSessionOwnerState.RUnlock()
	return verify != nil && sessionID != "" && verify(sessionID, userID)
}

// needsOriginCheck 判断请求是否要核对 Origin：非 GET 请求与 WebSocket 握手（握手是 GET，按升级头和路由判断）。
func needsOriginCheck(r *http.Request, route string) bool {
	return r.Method != http.MethodGet || r.Header.Get("Upgrade") != "" || route == "ssh/WsConnect"
}

// originAllowed 要求恰好一个 Origin 头，且等于 https://<Host> 或配置里的开发来源。
// T10 之前 Admin 在 Caddy 后面，Caddy 原样转发 Host 并已拒绝别的 Host。
func originAllowed(r *http.Request, allowed []string) bool {
	values := r.Header.Values("Origin")
	if len(values) != 1 || values[0] == "" {
		return false
	}
	origin := values[0]
	if r.Host != "" && strings.EqualFold(origin, "https://"+r.Host) {
		return true
	}
	for _, item := range allowed {
		if strings.EqualFold(origin, strings.TrimSpace(item)) {
			return true
		}
	}
	return false
}

// requestSession 按 Cookie 查会话。Cookie 的值不写日志。
func (hr *HTTPRouter) requestSession(r *http.Request) (session.Session, bool) {
	if hr.sessions == nil {
		return session.Session{}, false
	}
	cookie, err := r.Cookie(session.CookieName)
	if err != nil {
		return session.Session{}, false
	}
	return hr.sessions.Get(cookie.Value)
}

// clientIP 是登录锁定和登录记录用的来源 IP。T10 之前请求都经 Caddy 转来，取 Caddy 追加在 X-Forwarded-For
// 最后一段的对端地址（前面几段是客户端自己能写的，不可信）；取不到时用连接的对端地址。T10 改成只用连接对端地址。
func clientIP(r *http.Request) string {
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		parts := strings.Split(values[len(values)-1], ",")
		if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
			return ip.String()
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

func setSessionCookie(w http.ResponseWriter, created session.Session, ttl int) {
	http.SetCookie(w, &http.Cookie{
		Name: session.CookieName, Value: created.ID, Path: "/",
		Expires: created.ExpiresAt, MaxAge: ttl,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

// clearSessionCookie 让浏览器删掉会话 Cookie（属性与下发时相同）。
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: session.CookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	})
}

func writeConsoleRejection(w http.ResponseWriter, status int, code pb.ErrorCode, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(&middlewareResp{RetInfo: &pb.RetInfo{Code: code, Msg: msg}})
}

func writeAdminAuthFailure(w http.ResponseWriter) {
	writeConsoleRejection(w, http.StatusUnauthorized, pb.ErrorCode_NO_AUTH, "登录已失效，请重新登录")
}

func writeOriginRejected(w http.ResponseWriter) {
	writeConsoleRejection(w, http.StatusForbidden, pb.ErrorCode_NO_AUTH, "请求来源不是管理台页面，已拒绝")
}
