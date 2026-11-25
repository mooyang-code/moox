package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth/session"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
)

type forwardFunc func(context.Context, string, string, int, []byte) ([]byte, error)

func (f forwardFunc) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	return f(ctx, service, method, serialization, body)
}

func catalogReadOnly(t *testing.T, path, method string) bool {
	t.Helper()
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	return catalog.ReadOnly(path, method)
}

// newTestRouter 建控制台路由；store 为空时用一张新的会话表。
func newTestRouter(t *testing.T, store *session.Store, local *LocalDispatcher, gateway RPCForwarder) *HTTPRouter {
	t.Helper()
	if store == nil {
		var err error
		store, err = session.NewStore(time.Hour)
		require.NoError(t, err)
	}
	router, err := NewHTTPRouter(NewConsoleHandle(), local, gateway, store)
	require.NoError(t, err)
	return router
}

func TestConsoleNamesResolveToCatalogAndPreserveExactJSON(t *testing.T) {
	for _, tc := range []struct{ name, method, path string }{
		{"storage", "ListDatasets", "trpc.moox.storage.Metadata"},
		{"storage", "UpsertFields", "trpc.moox.storage.PrimaryStore"},
		{"storage", "QueryTimeSeriesRows", "trpc.moox.storage.DataView"},
		{"collector", "GetTaskList", "trpc.moox.collector.CollectMgr"},
		{"factor-mgr", "ListFactors", "trpc.moox.factor.FactorMgr"},
		{"monitor", "GetHealthOverview", "trpc.moox.monitor.MonitorMgr"},
		{"trade", "ListOrders", "trpc.moox.trade.TradeConsoleService"},
	} {
		t.Run(tc.name+"/"+tc.method, func(t *testing.T) {
			store, current := setupSessionTest(t)
			body := []byte(" {\n \"space_id\": \"space-1\", \"page\": {\"page\": 1}, \"value\": 1e+03 }\n")
			response := []byte(" {\"ret_info\": {\"code\": 0}, \"data\": [ 1, 2 ]} \n")
			calls := 0
			gateway := forwardFunc(func(ctx context.Context, service, method string, serialization int, raw []byte) ([]byte, error) {
				calls++
				require.Equal(t, tc.path, service)
				require.Equal(t, tc.method, method)
				require.Equal(t, codec.SerializationTypeJSON, serialization)
				// 请求体已声明空间，不需要回填，Storage 与其他服务一样原样转发。
				require.Equal(t, body, raw)
				require.Equal(t, gatewayclient.CallMetadata{SpaceID: "space-1", UserID: "u1", TraceID: "trace-1"}, gatewayclient.CallMetadataFromContext(ctx))
				return response, nil
			})
			router := newTestRouter(t, store, nil, gateway).buildControlRouter()
			r := browserRequest(t, current, "/api/admin/"+tc.name+"/"+tc.method, "space-1", body)
			r.Header.Set("X-Trace-Id", "trace-1")
			r.Header.Set("X-User-Id", "spoofed")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)
			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, response, rr.Body.Bytes())
			require.Equal(t, 1, calls)
		})
	}
}

func TestConsoleRejectsMachineMethodsAndNonCatalogAliasesBeforeDispatch(t *testing.T) {
	SetConfig(&Config{})
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return nil, nil })
	router := newTestRouter(t, nil, nil, gateway).buildControlRouter()
	for _, suffix := range []string{
		"secret/GetSecretValue", "SecretMgr/getsecretvalue", "trpc.moox.ops.SecretMgr/GetSecretValue",
		"secret/GetCloudCredential", "SecretMgr/getcloudcredential", "trpc.moox.ops.SecretMgr/GetCloudCredential",
		"sysdeploy/SyncTopology",
		"trade/CloseTargetSession", "trade_console/ListOrders", "collectmgr/GetTaskList",
		"factormgr/ListFactors", "moox_monitor/GetHealthOverview", "collector_market_runtime/ClaimTimerBatch",
		"storage-primary/ListDatasets", "storage-view/QueryTimeSeriesRows", "storage/ApplyReplica",
		"auth/login", "auth/Unknown", "unknown/ListDatasets",
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/"+suffix, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusNotFound, rr.Code, suffix)
	}
	require.Zero(t, calls)
}

// 服务目录里 operators 不含 console 的方法（写了 [] 或 [moox-cli]）经控制台调用一律 404，转发之前就拒绝；
// 同一服务上 console 能调的方法走到鉴权（未登录返回 401），说明拒绝来自目录而不是路由不存在。
func TestConsoleRejectsMethodsWhoseOperatorsExcludeConsole(t *testing.T) {
	SetConfig(&Config{})
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return nil, nil })
	router := newTestRouter(t, nil, nil, gateway).buildControlRouter()
	post := func(name, method string) int {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, browserRequest(t, session.Session{}, "/api/admin/"+name+"/"+method, "", []byte(`{}`)))
		return rr.Code
	}
	// Storage 的三个服务共用一个控制台名：只要其中一个服务允许 console，这个方法就合法地解析到那个服务。
	consoleCallable := func(name, method string) bool {
		for _, component := range catalog.Components {
			for _, service := range component.Services {
				if service.ConsoleName == name && catalog.Allowed(servicecatalog.OperatorConsole, service.Path, method) {
					return true
				}
			}
		}
		return false
	}
	kinds := map[string]int{}
	for _, component := range catalog.Components {
		for _, service := range component.Services {
			if service.ConsoleName == "" {
				continue
			}
			checkedAllowed := false
			for _, method := range service.Methods {
				route := service.ConsoleName + "/" + method.Name
				if rawRoutes[route] {
					continue
				}
				if catalog.Allowed(servicecatalog.OperatorConsole, service.Path, method.Name) {
					if !checkedAllowed && route != loginRoute {
						require.Equal(t, http.StatusUnauthorized, post(service.ConsoleName, method.Name), "%s", route)
						checkedAllowed = true
					}
					continue
				}
				if consoleCallable(service.ConsoleName, method.Name) {
					continue
				}
				kinds[strings.Join(method.OperatorList(), ",")]++
				require.Equal(t, http.StatusNotFound, post(service.ConsoleName, method.Name), "%s（%s）", route, service.Path)
			}
		}
	}
	require.Zero(t, calls, "被拒绝的方法不能转发")
	require.Positive(t, kinds[""], "要覆盖 operators: [] 的方法")
	require.Positive(t, kinds[servicecatalog.OperatorMooxCLI], "要覆盖 operators: [moox-cli] 的方法")
	t.Logf("经控制台被拒的方法：%v", kinds)
}

type localAuth struct {
	pb.UnimplementedAuth
	sessions *session.Store
	username string
	calls    int
	sid      string
}

// Login 模拟认证服务：按登录上下文在会话表里建会话，响应体不带会话 ID。
func (a *localAuth) Login(ctx context.Context, req *pb.LoginReq) (*pb.LoginRsp, error) {
	a.calls++
	a.username = req.Username
	login := session.LoginFrom(ctx)
	if login == nil || login.ClientIP != "203.0.113.9" {
		return nil, errors.New("console did not pass the login context")
	}
	created, err := a.sessions.Create("u1", req.Username)
	if err != nil {
		return nil, err
	}
	login.Issued = &created
	return &pb.LoginRsp{RetInfo: &pb.RetInfo{}, ExpiresAt: created.ExpiresAt.Unix()}, nil
}

func (a *localAuth) GetUserInfo(ctx context.Context, _ *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	a.calls++
	received := gatewayclient.CallMetadataFromContext(ctx)
	if received.UserID != "u1" || string(trpc.GetMetaData(ctx, authmodel.CtxUserID)) != "u1" || string(trpc.GetMetaData(ctx, authmodel.CtxSessionID)) != a.sid {
		return nil, errors.New("verified user/session was not injected")
	}
	return &pb.GetUserInfoRsp{RetInfo: &pb.RetInfo{}}, nil
}

func TestConsoleUsesGeneratedLocalHandlersAndVerifiedUserContext(t *testing.T) {
	store, _ := setupSessionTest(t)
	local, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &localAuth{sessions: store}
	require.NoError(t, local.Register(&pb.AuthServer_ServiceDesc, implementation))
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
		t.Fatal("Admin service went through gateway")
		return nil, nil
	})
	router := newTestRouter(t, store, local, gateway).buildControlRouter()
	rr := httptest.NewRecorder()
	login := browserRequest(t, session.Session{}, "/api/admin/auth/Login", "", []byte(`{"username":"alice","password":"x"}`))
	login.RemoteAddr = "203.0.113.9:40000"
	login.Header.Set("X-Forwarded-For", "6.6.6.6")
	router.ServeHTTP(rr, login)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "alice", implementation.username)
	cookies := rr.Result().Cookies()
	require.Len(t, cookies, 1)
	implementation.sid = cookies[0].Value
	require.NotContains(t, rr.Body.String(), implementation.sid, "会话 ID 不进响应体")

	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, session.Session{ID: implementation.sid}, "/api/admin/auth/GetUserInfo", "", []byte(`{}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Empty(t, rr.Header().Get("trpc-ret"))
	require.Equal(t, 2, implementation.calls)
}

// 不按空间放行，只核对请求头与请求体里的空间一致；必须带空间的服务没有空间时拒绝。
func TestConsoleRejectsMissingOrMismatchedSpace(t *testing.T) {
	for _, tc := range []struct {
		name, space, body string
		status            int
	}{
		{"missing space", "", `{}`, http.StatusForbidden},
		{"nested snake space", "space-1", `{"request":{"space_id":"space-2"}}`, http.StatusBadRequest},
		{"nested camel space", "space-1", `{"requests":[{"spaceId":"space-2"}]}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, current := setupSessionTest(t)
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
				t.Fatal("rejected request reached gateway")
				return nil, nil
			})
			rr := httptest.NewRecorder()
			newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(rr, browserRequest(t, current, "/api/admin/collector/GetTaskList", tc.space, []byte(tc.body)))
			require.Equal(t, tc.status, rr.Code, rr.Body.String())
		})
	}
}

func TestConsoleAuthenticationPrecedesRemoteDispatch(t *testing.T) {
	store, current := setupSessionTest(t)
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
	router := newTestRouter(t, store, nil, gateway).buildControlRouter()
	body := []byte(`{"space_id":"space-1","offset":1}`)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/storage/ListDatasets", "", body))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, calls)

	noCookie := browserRequest(t, session.Session{}, "/api/admin/storage/ListDatasets", "", body)
	unknown := browserRequest(t, session.Session{ID: "not-a-session"}, "/api/admin/storage/ListDatasets", "", body)
	headerToken := browserRequest(t, session.Session{}, "/api/admin/storage/ListDatasets", "", body)
	headerToken.Header.Set("Authorization", current.ID)
	for _, request := range []*http.Request{noCookie, unknown, headerToken} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, request)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	}
	// 退出（服务端删掉会话）之后，同一个 Cookie 再来就是 401。
	store.Delete(current.ID)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/storage/ListDatasets", "", body))
	require.Equal(t, http.StatusUnauthorized, rr.Code)
	require.Equal(t, 1, calls)
}

// 别的来源的页面发来的 POST（即使带着 Cookie）在鉴权之前就被拒，返回 403 而不是 401；缺少 Origin 同样拒绝。
func TestConsoleRejectsCrossOriginWrites(t *testing.T) {
	store, current := setupSessionTest(t)
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
	router := newTestRouter(t, store, nil, gateway).buildControlRouter()
	for name, origin := range map[string]string{"other site": "https://evil.example", "plain http": "http://" + testHost, "missing": ""} {
		r := browserRequest(t, current, "/api/admin/monitor/GetHealthOverview", "", []byte(`{}`))
		r.Header.Del("Origin")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, r)
		require.Equal(t, http.StatusForbidden, rr.Code, name)
	}
	login := browserRequest(t, session.Session{}, "/api/admin/auth/Login", "", []byte(`{}`))
	login.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, login)
	require.Equal(t, http.StatusForbidden, rr.Code, "登录也要核对来源")
	require.Zero(t, calls)

	// RPC 只收 POST：GET 不带请求体也会触发方法，同站的链接就能借 Cookie 发起。
	get := httptest.NewRequest(http.MethodGet, "/api/admin/monitor/GetHealthOverview", nil)
	get.Host = testHost
	get.AddCookie(&http.Cookie{Name: session.CookieName, Value: current.ID})
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, get)
	require.Equal(t, http.StatusMethodNotAllowed, rr.Code)

	// 本机开发经开发服务器代理时，配置的开发来源可以通过。
	SetConfig(&Config{Console: ConsoleConfig{AllowedOrigins: []string{"http://localhost:9527"}}})
	dev := newTestRouter(t, store, nil, gateway).buildControlRouter()
	r := browserRequest(t, current, "/api/admin/monitor/GetHealthOverview", "", []byte(`{}`))
	r.Header.Set("Origin", "http://localhost:9527")
	rr = httptest.NewRecorder()
	dev.ServeHTTP(rr, r)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, calls)
}

// 每条裸路由（SSH 流式与归档）都按会话 Cookie 鉴权，把原样的请求体与用户交给处理器；没有 Cookie 一律 401，
// SSH 路由的 session_id 不是当前用户的返回 403。
func TestConsoleRawRoutesUseSessionCookie(t *testing.T) {
	for route := range rawRoutes {
		t.Run(route, func(t *testing.T) {
			store, current := setupSessionTest(t)
			SetRawSessionOwnerVerifier(func(sshSession, user string) bool { return sshSession == "ssh-1" && user == "u1" })
			t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
			query := "session_id=ssh-1"
			if strings.HasPrefix(route, "archive/") {
				query = "space=crypto&dataset=ds"
			}
			parts := strings.Split(route, "/")
			body := []byte("--multipart\r\nstreamed bytes\x00\xff\r\n")
			calls := 0
			RegisterRawHandler(parts[0], parts[1], func(w http.ResponseWriter, r *http.Request) {
				calls++
				got, err := io.ReadAll(r.Body)
				require.NoError(t, err)
				require.Equal(t, body, got)
				require.Equal(t, "u1", r.Context().Value(authmodel.CtxUserID))
				w.WriteHeader(http.StatusAccepted)
			})
			t.Cleanup(func() { rawHandlersMutex.Lock(); delete(rawHandlers[parts[0]], parts[1]); rawHandlersMutex.Unlock() })
			router := newTestRouter(t, store, nil, nil).buildControlRouter()
			path := fmt.Sprintf("/api/admin/%s?%s", route, query)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, browserRequest(t, current, path, "", body))
			require.Equal(t, http.StatusAccepted, rr.Code)
			rr = httptest.NewRecorder()
			router.ServeHTTP(rr, browserRequest(t, session.Session{}, path, "", body))
			require.Equal(t, http.StatusUnauthorized, rr.Code)
			if parts[0] == "ssh" {
				rr = httptest.NewRecorder()
				router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/"+route+"?session_id=ssh-other", "", body))
				require.Equal(t, http.StatusForbidden, rr.Code)
			}
			require.Equal(t, 1, calls)
		})
	}
}

// 归档下载是普通 GET：浏览器同源导航与 fetch 自带 Cookie，不需要票据；没有 Cookie 被拒。
func TestConsoleArchiveDownloadUsesCookieOnGet(t *testing.T) {
	store, current := setupSessionTest(t)
	calls := 0
	RegisterRawHandler("archive", "PartitionDownload", func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusOK) })
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers["archive"], "PartitionDownload")
		rawHandlersMutex.Unlock()
	})
	router := newTestRouter(t, store, nil, nil).buildControlRouter()
	get := func(cookie string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/archive/PartitionDownload?space=crypto&dataset=ds&partition=2026-10-15", nil)
		r.Host = testHost
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookie})
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, r)
		return rr.Code
	}
	require.Equal(t, http.StatusOK, get(current.ID))
	require.Equal(t, http.StatusUnauthorized, get(""))
	require.Equal(t, 1, calls)
}

func TestConsoleDoesNotExposeArbitraryRawHandlerRegistration(t *testing.T) {
	RegisterRawHandler("demo", "Ping", func(http.ResponseWriter, *http.Request) { t.Fatal("unlisted raw route exposed") })
	t.Cleanup(func() { rawHandlersMutex.Lock(); delete(rawHandlers, "demo"); rawHandlersMutex.Unlock() })
	rr := httptest.NewRecorder()
	newTestRouter(t, nil, nil, nil).buildControlRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/demo/Ping", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)
}

// 终端 WebSocket 握手是 GET，同样核对 Origin（缺少或不是本站返回 403），再看 Cookie（没有返回 401）。
func TestConsoleWebSocketHandshakeRequiresOriginAndCookie(t *testing.T) {
	store, current := setupSessionTest(t)
	SetRawSessionOwnerVerifier(func(sshSession, user string) bool { return sshSession == "ssh-1" && user == "u1" })
	t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
	calls := 0
	RegisterRawHandler("ssh", "WsConnect", func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusSwitchingProtocols) })
	t.Cleanup(func() { rawHandlersMutex.Lock(); delete(rawHandlers["ssh"], "WsConnect"); rawHandlersMutex.Unlock() })
	router := newTestRouter(t, store, nil, nil).buildControlRouter()
	handshake := func(origin, cookie string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/ssh/WsConnect?session_id=ssh-1&w=80&h=24", nil)
		r.Host = testHost
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Connection", "Upgrade")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		if cookie != "" {
			r.AddCookie(&http.Cookie{Name: session.CookieName, Value: cookie})
		}
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, r)
		return rr.Code
	}
	require.Equal(t, http.StatusForbidden, handshake("https://evil.example", current.ID))
	require.Equal(t, http.StatusForbidden, handshake("", current.ID))
	require.Equal(t, http.StatusUnauthorized, handshake(testOrigin, ""))
	require.Zero(t, calls)
	require.Equal(t, http.StatusSwitchingProtocols, handshake(testOrigin, current.ID))
	require.Equal(t, 1, calls)
}

// Storage / Factor 请求受空间可用性约束；无空间的全局请求直接放行（只有一个超级用户）。
func TestConsoleStorageAndFactorAreSpaceAuthorized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		space  string
		body   string
		status int
	}{
		{"reads own space", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-1"}`, http.StatusOK},
		{"body space without header", "/api/admin/storage/ListDatasets", "", `{"space_id":"space-2"}`, http.StatusOK},
		{"header and body mismatch", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-2"}`, http.StatusBadRequest},
		{"space method without space", "/api/admin/storage/ListDatasets", "", `{}`, http.StatusBadRequest},
		{"global storage method without space", "/api/admin/storage/ListSpaces", "", `{}`, http.StatusOK},
		{"factor request with space", "/api/admin/factor-mgr/DeleteFactorTask", "space-1", `{}`, http.StatusOK},
		{"factor global request without space", "/api/admin/factor-mgr/ListFactors", "", `{}`, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, current := setupSessionTest(t)
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
			rr := httptest.NewRecorder()
			newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(rr, browserRequest(t, current, tc.path, tc.space, []byte(tc.body)))
			require.Equal(t, tc.status, rr.Code, rr.Body.String())
		})
	}
}

// 限流接入真实 HTTP 路由：免会话的登录接口超过突发额度后返回 429，且不到达转发层。
func TestConsoleRateLimitsConfiguredMethodsOnRealRoute(t *testing.T) {
	store, current := setupSessionTest(t)
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
	router := newTestRouter(t, store, nil, gateway).buildControlRouter()
	codes := map[int]int{}
	for i := 0; i < 8; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, browserRequest(t, session.Session{}, "/api/admin/auth/Login", "", []byte(`{}`)))
		codes[rr.Code]++
	}
	require.NotZero(t, codes[http.StatusTooManyRequests], "超额请求必须被限流: %v", codes)
	// 未配置接口级限额的方法不被限流。
	for i := 0; i < 40; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/monitor/GetHealthOverview", "", []byte(`{}`)))
		require.NotEqual(t, http.StatusTooManyRequests, rr.Code)
	}
}

// Storage 方法的授权空间必须由请求体声明，防止请求头授权 S 而后端按空 space_id 查询全部空间。
func TestConsoleStorageBindsAuthorizedSpace(t *testing.T) {
	for _, tc := range []struct {
		name, path, space, body string
		status                  int
	}{
		{"header space injected when body has none", "/api/admin/storage/ListDatasets", "space-1", `{}`, http.StatusOK},
		{"empty space id is replaced by header space", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":""}`, http.StatusOK},
		{"body space ok", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-1"}`, http.StatusOK},
		{"null body", "/api/admin/storage/ListDatasets", "", `null`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, current := setupSessionTest(t)
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
			rr := httptest.NewRecorder()
			newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(rr, browserRequest(t, current, tc.path, tc.space, []byte(tc.body)))
			require.Equal(t, tc.status, rr.Code, rr.Body.String())
		})
	}
}

// 仅由请求体声明的空间，授权通过后必须传给下游元数据。
func TestConsolePropagatesBodyDeclaredSpaceToDownstream(t *testing.T) {
	store, current := setupSessionTest(t)
	var got gatewayclient.CallMetadata
	gateway := forwardFunc(func(ctx context.Context, _, _ string, _ int, _ []byte) ([]byte, error) {
		got = gatewayclient.CallMetadataFromContext(ctx)
		return []byte(`{}`), nil
	})
	rr := httptest.NewRecorder()
	newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(rr, browserRequest(t, current, "/api/admin/strategy/ListStrategyInstances", "", []byte(`{"space_id":"space-9"}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "space-9", got.SpaceID)
}

// 空间回填对 camelCase 别名只输出一份 space_id；请求体不是 JSON 对象时报 400 用的错误。
func TestInjectStorageSpaceNormalizesProtoJSONAlias(t *testing.T) {
	for _, body := range []string{``, `{"spaceId":"","page":{"page":1}}`, `{"page":{"page":1}}`} {
		out, err := injectStorageSpace([]byte(body), "s")
		require.NoError(t, err)
		var got map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(out, &got))
		require.NotContains(t, got, "spaceId")
		require.JSONEq(t, `"s"`, string(got["space_id"]))
	}
	_, err := injectStorageSpace([]byte(`null`), "s")
	require.ErrorIs(t, err, errInvalidStorageBody)
}

func TestConsoleInjectsHeaderSpaceIntoStorageBodyWhenAbsent(t *testing.T) {
	store, current := setupSessionTest(t)
	var raw []byte
	gateway := forwardFunc(func(_ context.Context, _, _ string, _ int, body []byte) ([]byte, error) {
		raw = body
		return []byte(`{}`), nil
	})
	rr := httptest.NewRecorder()
	newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(rr, browserRequest(t, current, "/api/admin/storage/ListDatasets", "space-1", []byte(`{"page":{"page":1}}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	// 空间在嵌套消息里的方法（dataset.space_id）不能回填顶层字段，缺省时直接 400。
	rr = httptest.NewRecorder()
	newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(rr, browserRequest(t, current, "/api/admin/storage/UpdateDataset", "space-1", []byte(`{"dataset":{"dataset_id":"dataset_v1"}}`)))
	require.Equal(t, http.StatusBadRequest, rr.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "space-1", got["space_id"])
}

// 回填清单必须与 .proto 一致：只有请求消息带顶层 space_id 的方法才允许回填。
func TestStorageTopLevelSpaceMethodsMatchProto(t *testing.T) {
	root := filepath.Join("..", "..", "..", "storage", "proto")
	files, err := filepath.Glob(filepath.Join(root, "*.proto"))
	require.NoError(t, err)
	messages := map[string]bool{}
	requests := map[string]string{}
	messageStart := regexp.MustCompile(`message\s+(\w+)\s*\{`)
	rpcRe := regexp.MustCompile(`rpc\s+(\w+)\s*\(\s*(\w+)\s*\)`)
	spaceField := regexp.MustCompile(`\bspace_id\s*=\s*\d+`)
	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		text := string(raw)
		for _, match := range messageStart.FindAllStringSubmatchIndex(text, -1) {
			depth, top := 1, strings.Builder{}
			for i := match[1]; i < len(text) && depth > 0; i++ {
				switch text[i] {
				case '{':
					depth++
				case '}':
					depth--
				default:
					if depth == 1 {
						top.WriteByte(text[i])
					}
				}
			}
			messages[text[match[2]:match[3]]] = spaceField.MatchString(top.String())
		}
		for _, match := range rpcRe.FindAllStringSubmatch(text, -1) {
			requests[match[1]] = match[2]
		}
	}
	for method, allowed := range storageTopLevelSpaceMethods {
		require.True(t, allowed)
		request, ok := requests[method]
		require.True(t, ok, "%s 不在 storage proto 中", method)
		require.True(t, messages[request], "%s 的请求消息 %s 没有顶层 space_id，不能回填", method, request)
	}
}

// 部署与归档 worker 的写操作只留给 moox-cli：控制台没有这些路由，一律 404。
func TestConsoleHasNoDeploymentWriteRoutes(t *testing.T) {
	for _, path := range []string{
		"/api/admin/sysdeploy/SyncTopology",
		"/api/admin/archive/StartArchiveImport",
		"/api/admin/archive/ClearArchiveUnrecoverable",
	} {
		store, current := setupSessionTest(t)
		gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
		rr := httptest.NewRecorder()
		router := newTestRouter(t, store, nil, gateway).buildControlRouter()
		router.ServeHTTP(rr, browserRequest(t, current, path, "", []byte(`{}`)))
		require.Equal(t, http.StatusNotFound, rr.Code, path)
	}
}
