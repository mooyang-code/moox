package console

import (
	"bytes"
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
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
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

func newTestRouter(t *testing.T, local *LocalDispatcher, gateway RPCForwarder, authorizers ...SpaceAuthorizer) *HTTPRouter {
	t.Helper()
	router, err := NewHTTPRouter(NewConsoleHandle(), local, gateway, authorizers...)
	require.NoError(t, err)
	return router
}

func browserRequest(t *testing.T, secret, key, sid, path, space string, body []byte) *http.Request {
	t.Helper()
	nonce := strings.Repeat("a", 64)
	timestamp := time.Now().Unix()
	r := signedAdminRequest(t, secret, key, sid, path, body, timestamp, nonce)
	r.Body = io.NopCloser(bytes.NewReader(body))
	if space != "" {
		r.Header.Set("X-Space-Id", space)
		signature, err := requestauth.Sign(key, requestauth.Material{
			Method: http.MethodPost, Path: path, Body: body, Timestamp: timestamp, Nonce: nonce,
			Headers: map[string]string{"X-Space-Id": space},
		})
		require.NoError(t, err)
		r.Header.Set("X-Moox-Signature", signature)
	}
	return r
}

func TestConsoleNamesResolveToCatalogAndPreserveExactJSON(t *testing.T) {
	for _, tc := range []struct{ name, method, path string }{
		{"storage", "ListFields", "trpc.moox.storage.Metadata"},
		{"storage", "UpsertFields", "trpc.moox.storage.PrimaryStore"},
		{"storage", "QueryTimeSeriesRows", "trpc.moox.storage.DataView"},
		{"collector", "GetTaskList", "trpc.moox.collector.CollectMgr"},
		{"factor-mgr", "ListFactors", "trpc.moox.factor.FactorMgr"},
		{"monitor", "GetHealthOverview", "trpc.moox.monitor.MonitorMgr"},
		{"trade", "ListOrders", "trpc.moox.trade.TradeConsoleService"},
	} {
		t.Run(tc.name+"/"+tc.method, func(t *testing.T) {
			_, secret, key, sid := setupRequestAuthTest(t)
			body := []byte(" {\n \"space_id\": \"space-1\", \"auth_info\": {\"app_id\": \"browser\"}, \"value\": 1e+03 }\n")
			response := []byte(" {\"ret_info\": {\"code\": 0}, \"data\": [ 1, 2 ]} \n")
			calls := 0
			gateway := forwardFunc(func(ctx context.Context, service, method string, serialization int, raw []byte) ([]byte, error) {
				calls++
				require.Equal(t, tc.path, service)
				require.Equal(t, tc.method, method)
				require.Equal(t, codec.SerializationTypeJSON, serialization)
				if tc.name == "storage" {
					var got map[string]any
					require.NoError(t, json.Unmarshal(raw, &got))
					auth := got["auth_info"].(map[string]any)
					require.Equal(t, "admin-gateway", auth["app_id"])
					secretEnv := "primary-secret"
					if tc.path == storageViewServicePath {
						secretEnv = "view-secret"
					}
					require.Equal(t, storageServiceAuthKey(secretEnv, "admin-gateway"), auth["app_key"])
					require.Equal(t, "space-1", got["space_id"])
				} else {
					require.Equal(t, body, raw)
				}
				require.Equal(t, gatewayclient.CallMetadata{SpaceID: "space-1", UserID: "u1", UserRole: "2", TraceID: "trace-1"}, gatewayclient.CallMetadataFromContext(ctx))
				return response, nil
			})
			authorizer := &fakeSpaceAuthorizer{}
			router := newTestRouter(t, nil, gateway, authorizer).buildControlRouter()
			r := browserRequest(t, secret, key, sid, "/api/admin/"+tc.name+"/"+tc.method, "space-1", body)
			r.Header.Set("X-Trace-Id", "trace-1")
			r.Header.Set("X-User-Id", "spoofed")
			r.Header.Set("X-User-Role", "999")
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, r)
			require.Equal(t, http.StatusOK, rr.Code)
			require.Equal(t, response, rr.Body.Bytes())
			require.Equal(t, 1, calls)
			if isSpaceScopedService(tc.name) {
				require.Equal(t, "u1", authorizer.userID)
				require.Equal(t, "space-1", authorizer.spaceID)
				require.Equal(t, !catalogReadOnly(t, tc.path, tc.method), authorizer.mutates)
				require.Equal(t, int32(2), authorizer.globalRole)
			}
		})
	}
}

func TestConsoleRejectsMachineMethodsAndNonCatalogAliasesBeforeDispatch(t *testing.T) {
	SetConfig(&Config{})
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return nil, nil })
	router := newTestRouter(t, nil, gateway).buildControlRouter()
	for _, suffix := range []string{
		"secret/GetSecretValue", "SecretMgr/getsecretvalue", "trpc.moox.ops.SecretMgr/GetSecretValue",
		"cloudfunction/CollectGarbage", "publishlease/ValidateCollectorPublishLease",
		"publishlease/BeginCollectorPublishOperation", "sysdeploy/SyncHostPlacements", "sysdeploy/DeleteHost",
		"trade/ReleaseLogicalAccountOwner", "trade/ClaimLogicalAccountOwner", "trade/RebindLogicalAccountOwner",
		"trade_owner/ReleaseLogicalAccountOwner", "trade_console/ListOrders", "collectmgr/GetTaskList",
		"factormgr/ListFactors", "moox_monitor/GetHealthOverview", "collector_market_runtime/ClaimTimerBatch",
		"storage-primary/ListFields", "storage-view/QueryTimeSeriesRows", "storage/ApplyReplica",
		"auth/getloginsalt", "auth/Unknown", "unknown/ListFields",
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/"+suffix, strings.NewReader(`{}`)))
		require.Equal(t, http.StatusNotFound, rr.Code, suffix)
	}
	require.Zero(t, calls)
}

type localAuth struct {
	pb.UnimplementedAuth
	username string
	calls    int
}

func (a *localAuth) GetLoginSalt(ctx context.Context, req *pb.GetLoginSaltReq) (*pb.GetLoginSaltRsp, error) {
	a.calls++
	a.username = req.Username
	return &pb.GetLoginSaltRsp{RetInfo: &pb.RetInfo{}, Salt: "in-process-salt"}, nil
}

func (a *localAuth) GetUserInfo(ctx context.Context, _ *pb.GetUserInfoReq) (*pb.GetUserInfoRsp, error) {
	a.calls++
	received := gatewayclient.CallMetadataFromContext(ctx)
	if received.UserID != "u1" || string(trpc.GetMetaData(ctx, authmodel.CtxUserID)) != "u1" || string(trpc.GetMetaData(ctx, authmodel.CtxSessionID)) != "sid-1" {
		return nil, errors.New("verified user/session was not injected")
	}
	return &pb.GetUserInfoRsp{RetInfo: &pb.RetInfo{}}, nil
}

func TestConsoleUsesGeneratedLocalHandlersAndVerifiedUserContext(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	local, err := NewLocalDispatcher()
	require.NoError(t, err)
	implementation := &localAuth{}
	require.NoError(t, local.Register(&pb.AuthServer_ServiceDesc, implementation))
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
		t.Fatal("Admin service went through gateway")
		return nil, nil
	})
	router := newTestRouter(t, local, gateway).buildControlRouter()
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/auth/GetLoginSalt", strings.NewReader(`{"username":"alice"}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Contains(t, rr.Body.String(), "in-process-salt")
	require.Equal(t, "alice", implementation.username)
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/auth/GetUserInfo", "", []byte(`{}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Empty(t, rr.Header().Get("trpc-ret"))
	require.Equal(t, 2, implementation.calls)
}

func TestConsoleRetainsSpaceAuthorizationAndRejectsNestedSpaceMismatch(t *testing.T) {
	for _, tc := range []struct {
		name, space, body string
		denied            bool
	}{
		{"missing space", "", `{}`, false},
		{"unauthorized space", "space-1", `{}`, true},
		{"nested snake space", "space-1", `{"request":{"space_id":"space-2"}}`, false},
		{"nested camel space", "space-1", `{"requests":[{"spaceId":"space-2"}]}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, secret, key, sid := setupRequestAuthTest(t)
			authorizer := &fakeSpaceAuthorizer{}
			if tc.denied {
				authorizer.err = errors.New("space membership denied")
			}
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) {
				t.Fatal("unauthorized space reached gateway")
				return nil, nil
			})
			rr := httptest.NewRecorder()
			newTestRouter(t, nil, gateway, authorizer).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/collector/GetTaskList", tc.space, []byte(tc.body)))
			if tc.space == "" || tc.denied {
				require.Equal(t, http.StatusForbidden, rr.Code)
			} else {
				require.Contains(t, rr.Body.String(), "does not match")
			}
		})
	}
	_, secret, key, sid := setupRequestAuthTest(t)
	rr := httptest.NewRecorder()
	newTestRouter(t, nil, nil).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/trade/ListOrders", "space-1", []byte(`{}`)))
	require.Equal(t, http.StatusForbidden, rr.Code, "missing authorizer fails closed")
}

func TestConsoleAuthenticationPrecedesRemoteDispatch(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
	router := newTestRouter(t, nil, gateway, &fakeSpaceAuthorizer{}).buildControlRouter()
	body := []byte(`{"space_id":"space-1","offset":1}`)
	r := browserRequest(t, secret, key, sid, "/api/admin/storage/ListFields", "", body)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, r)
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, 1, calls)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, r.URL.String(), bytes.NewReader(body)),
		browserRequest(t, secret, key, sid, r.URL.String(), "", body), // replay
	} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, request)
		require.Equal(t, http.StatusUnauthorized, rr.Code)
	}
	require.Equal(t, 1, calls)
}

func TestConsoleSSHStreamingRoutesRetainTicketOwnerAndExactBody(t *testing.T) {
	for method, operation := range rawRouteOperations {
		t.Run(method, func(t *testing.T) {
			store, _, _, sid := setupRequestAuthTest(t)
			SetRawSessionOwnerVerifier(func(session, user string) bool { return session == "ssh-1" && user == "u1" })
			t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
			store.tickets["ticket-1"] = authmodel.RawSessionTicket{TicketID: "ticket-1", SessionID: sid, ResourceSessionID: "ssh-1", UserID: "u1", Operation: operation, ExpiresAt: time.Now().Add(time.Minute)}
			parts := strings.Split(method, "/")
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
			router := newTestRouter(t, nil, nil).buildControlRouter()
			path := fmt.Sprintf("/api/admin/%s?ticket=ticket-1&session_id=ssh-1", method)
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
			require.Equal(t, http.StatusAccepted, rr.Code)
			rr = httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
			require.Equal(t, http.StatusUnauthorized, rr.Code)
			require.Equal(t, 1, calls)
		})
	}
}

func TestConsoleDoesNotExposeArbitraryRawHandlerRegistration(t *testing.T) {
	RegisterRawHandler("demo", "Ping", func(http.ResponseWriter, *http.Request) { t.Fatal("unlisted raw route exposed") })
	t.Cleanup(func() { rawHandlersMutex.Lock(); delete(rawHandlers, "demo"); rawHandlersMutex.Unlock() })
	rr := httptest.NewRecorder()
	newTestRouter(t, nil, nil).buildControlRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/demo/Ping", nil))
	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestConsoleManualViewRebuildStillRequiresAdministrator(t *testing.T) {
	for _, role := range []int32{1, 2} {
		t.Run(fmt.Sprint(role), func(t *testing.T) {
			_, secret, key, sid := setupRequestAuthTest(t)
			calls := 0
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
			r := browserRequest(t, secret, key, sid, "/api/admin/storage/RequestViewRebuild", "", []byte(`{"space_id":"space-1"}`))
			token, err := mooxsecurity.SignToken(map[string]any{"user_id": "u1", "username": "admin", "role": role, "token_type": "access", "sid": sid}, secret, "moox-admin", time.Hour)
			require.NoError(t, err)
			r.Header.Set("Authorization", token)
			rr := httptest.NewRecorder()
			newTestRouter(t, nil, gateway, &fakeSpaceAuthorizer{}).buildControlRouter().ServeHTTP(rr, r)
			if role < 2 {
				require.Equal(t, http.StatusForbidden, rr.Code)
				require.Zero(t, calls)
			} else {
				require.Equal(t, http.StatusOK, rr.Code)
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestConsoleRawRouteCannotBypassTicketThroughNoAuthConfiguration(t *testing.T) {
	setupRequestAuthTest(t)
	SetConfig(&Config{Console: ConsoleConfig{NoAuthMethods: []string{"/api/admin/ssh/WsConnect"}}})
	RegisterRawHandler("ssh", "WsConnect", func(http.ResponseWriter, *http.Request) { t.Fatal("unsigned raw route reached handler") })
	t.Cleanup(func() { rawHandlersMutex.Lock(); delete(rawHandlers["ssh"], "WsConnect"); rawHandlersMutex.Unlock() })
	rr := httptest.NewRecorder()
	newTestRouter(t, nil, nil).buildControlRouter().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/admin/ssh/WsConnect?session_id=ssh-1", nil))
	require.Equal(t, http.StatusUnauthorized, rr.Code)
}

// 普通用户不能改全局部署状态；管理员可以。
func TestConsoleGlobalWritesRequireAdministrator(t *testing.T) {
	for _, tc := range []struct {
		path   string
		role   int
		status int
	}{
		{"/api/admin/sysdeploy/SetPlacementStatus", 1, http.StatusForbidden},
		{"/api/admin/sysdeploy/SetHostStatus", 1, http.StatusForbidden},
		{"/api/admin/sysdeploy/SetPlacementStatus", 2, http.StatusOK},
		{"/api/admin/sysdeploy/ListPlacements", 1, http.StatusOK},
		// SSH 主机配置与会话等价于登录主机：普通用户连读都不行。
		{"/api/admin/ssh/ListHosts", 1, http.StatusForbidden},
		{"/api/admin/ssh/GetHost", 1, http.StatusForbidden},
		{"/api/admin/ssh/ListHosts", 2, http.StatusOK},
		// 因子定义是全局的，会被所有空间的因子集引用：只有全局管理员可以保存。
		{"/api/admin/factor-mgr/CreateFactor", 1, http.StatusForbidden},
		{"/api/admin/factor-mgr/UpdateFactor", 1, http.StatusForbidden},
		{"/api/admin/factor-mgr/UpsertFactorCategory", 1, http.StatusForbidden},
		{"/api/admin/factor-mgr/DeleteFactorCategory", 1, http.StatusForbidden},
		{"/api/admin/factor-mgr/SetFactorCategory", 1, http.StatusForbidden},
		// 策略定义是全局的：空间管理员不能改被其他空间实例引用的定义；读取仍按空间授权。
		{"/api/admin/strategy/CreateStrategy", 1, http.StatusForbidden},
		{"/api/admin/strategy/UpdateStrategy", 1, http.StatusForbidden},
		{"/api/admin/strategy/DeleteStrategy", 1, http.StatusForbidden},
		{"/api/admin/cloudfunction/CreateCloudAccount", 1, http.StatusForbidden},
		{"/api/admin/cloudfunction/UpdateCloudAccount", 1, http.StatusForbidden},
		{"/api/admin/cloudfunction/DeleteCloudAccount", 1, http.StatusForbidden},
		{"/api/admin/factor-mgr/ListFactors", 1, http.StatusForbidden}, // 无空间的 Factor 请求本来就要管理员
		// 推送渠道的完整 Webhook URL 是凭据：只有全局管理员能列出、增删；概览仍对所有登录用户开放。
		{"/api/admin/monitor/ListNotificationChannels", 1, http.StatusForbidden},
		{"/api/admin/monitor/ListNotificationChannels", 2, http.StatusOK},
		{"/api/admin/monitor/AddNotificationChannel", 1, http.StatusForbidden},
		{"/api/admin/monitor/DeleteNotificationChannel", 1, http.StatusForbidden},
		{"/api/admin/monitor/AddNotificationChannel", 2, http.StatusOK},
		{"/api/admin/monitor/GetHealthOverview", 1, http.StatusOK},
	} {
		_, secret, key, sid := setupRequestAuthTest(t)
		testTokenRole = tc.role
		gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
		rr := httptest.NewRecorder()
		router := newTestRouter(t, nil, gateway).buildControlRouter()
		router.ServeHTTP(rr, browserRequest(t, secret, key, sid, tc.path, "", []byte(`{}`)))
		if tc.status == http.StatusForbidden {
			require.Equal(t, tc.status, rr.Code, "%s role=%d", tc.path, tc.role)
		} else {
			require.NotEqual(t, http.StatusForbidden, rr.Code, "%s role=%d", tc.path, tc.role)
		}
	}
}

// Storage / Factor 请求同样受空间成员资格约束，无空间的请求只允许全局管理员。
func TestConsoleStorageAndFactorAreSpaceAuthorized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		space  string
		body   string
		role   int
		deny   bool
		status int
	}{
		{"member reads own space", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-1"}`, 1, false, http.StatusOK},
		{"member blocked by authorizer", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-1"}`, 1, true, http.StatusForbidden},
		{"body space without header is authorized", "/api/admin/storage/ListDatasets", "", `{"space_id":"space-2"}`, 1, true, http.StatusForbidden},
		{"header and body mismatch", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-2"}`, 1, false, http.StatusBadRequest},
		{"member without space", "/api/admin/storage/ListDatasets", "", `{}`, 1, false, http.StatusBadRequest},
		{"admin without space", "/api/admin/storage/ListDataNodes", "", `{}`, 2, false, http.StatusOK},
		{"factor member blocked", "/api/admin/factor-mgr/DeleteFactorSet", "space-1", `{}`, 1, true, http.StatusForbidden},
		{"factor member without space", "/api/admin/factor-mgr/ListFactors", "", `{}`, 1, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, secret, key, sid := setupRequestAuthTest(t)
			testTokenRole = tc.role
			authorizer := &fakeSpaceAuthorizer{}
			if tc.deny {
				authorizer.err = errors.New("space membership denied")
			}
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
			rr := httptest.NewRecorder()
			newTestRouter(t, nil, gateway, authorizer).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, tc.path, tc.space, []byte(tc.body)))
			require.Equal(t, tc.status, rr.Code, rr.Body.String())
		})
	}
}

// Collector 的只读方法对空间普通成员是只读授权，写方法才要求 owner/admin。
func TestConsoleCatalogReadOnlyDrivesSpaceMutationClass(t *testing.T) {
	for _, tc := range []struct {
		path    string
		mutates bool
	}{
		{"/api/admin/collector/GetTaskList", false},
		{"/api/admin/cloudfunction/ListFunctionInstances", false},
		{"/api/admin/collector/CreateTask", true},
	} {
		_, secret, key, sid := setupRequestAuthTest(t)
		testTokenRole = 1
		authorizer := &fakeSpaceAuthorizer{}
		gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
		rr := httptest.NewRecorder()
		newTestRouter(t, nil, gateway, authorizer).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, tc.path, "space-1", []byte(`{}`)))
		require.Equal(t, http.StatusOK, rr.Code, tc.path)
		require.Equal(t, tc.mutates, authorizer.mutates, tc.path)
	}
}

// 限流接入真实 HTTP 路由：登录接口超过突发额度后返回 429，且不到达转发层。
func TestConsoleRateLimitsConfiguredMethodsOnRealRoute(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	resetRateLimitManagerForTest(t)
	calls := 0
	gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { calls++; return []byte(`{}`), nil })
	router := newTestRouter(t, nil, gateway).buildControlRouter()
	codes := map[int]int{}
	for i := 0; i < 8; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/auth/GetLoginSalt", "", []byte(`{}`)))
		codes[rr.Code]++
	}
	require.NotZero(t, codes[http.StatusTooManyRequests], "超额请求必须被限流: %v", codes)
	// 未配置接口级限额的方法不被限流。
	for i := 0; i < 40; i++ {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/monitor/GetHealthOverview", "", []byte(`{}`)))
		require.NotEqual(t, http.StatusTooManyRequests, rr.Code)
	}
}

// Storage 方法的授权空间必须由请求体声明，防止请求头授权 S 而后端按空 space_id 查询全部空间；
// 节点等全局方法只允许全局管理员，不能借空间管理权限执行。
func TestConsoleStorageBindsAuthorizedSpaceAndProtectsGlobalMethods(t *testing.T) {
	for _, tc := range []struct {
		name, path, space, body string
		role                    int
		status                  int
	}{
		{"header space injected when body has none", "/api/admin/storage/ListDatasets", "space-1", `{}`, 1, http.StatusOK},
		{"empty space id is replaced by header space", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":""}`, 1, http.StatusOK},
		{"body space ok", "/api/admin/storage/ListDatasets", "space-1", `{"space_id":"space-1"}`, 1, http.StatusOK},
		{"node write needs admin even with space header", "/api/admin/storage/UpdateDataNode", "space-1", `{"space_id":"space-1","node_id":"n1"}`, 1, http.StatusForbidden},
		{"node read needs admin", "/api/admin/storage/ListDataNodes", "", `{}`, 1, http.StatusForbidden},
		{"admin node write", "/api/admin/storage/UpdateDataNode", "", `{"node_id":"n1"}`, 2, http.StatusOK},
		{"space delete denied for member", "/api/admin/space/DeleteSpace", "", `{"space_id":"space-2"}`, 1, http.StatusForbidden},
		{"space update denied for member", "/api/admin/space/UpdateSpace", "space-1", `{"space":{"space_id":"space-1"}}`, 1, http.StatusForbidden},
		{"members need membership", "/api/admin/space/ListSpaceMembers", "", `{"space_id":"space-2"}`, 1, http.StatusForbidden},
		{"null body", "/api/admin/storage/ListDatasets", "", `null`, 2, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, secret, key, sid := setupRequestAuthTest(t)
			testTokenRole = tc.role
			authorizer := &fakeSpaceAuthorizer{}
			if tc.status == http.StatusForbidden && strings.Contains(tc.name, "members need") {
				authorizer.err = errors.New("not a member")
			}
			gateway := forwardFunc(func(context.Context, string, string, int, []byte) ([]byte, error) { return []byte(`{}`), nil })
			rr := httptest.NewRecorder()
			newTestRouter(t, nil, gateway, authorizer).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, tc.path, tc.space, []byte(tc.body)))
			if strings.HasPrefix(tc.path, "/api/admin/space/") && tc.status == http.StatusOK {
				return
			}
			require.Equal(t, tc.status, rr.Code, rr.Body.String())
		})
	}
}

// 仅由请求体声明的空间，授权通过后必须传给下游元数据。
func TestConsolePropagatesBodyDeclaredSpaceToDownstream(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	var got gatewayclient.CallMetadata
	gateway := forwardFunc(func(ctx context.Context, _, _ string, _ int, _ []byte) ([]byte, error) {
		got = gatewayclient.CallMetadataFromContext(ctx)
		return []byte(`{}`), nil
	})
	rr := httptest.NewRecorder()
	newTestRouter(t, nil, gateway, &fakeSpaceAuthorizer{}).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/strategy/ListStrategyInstances", "", []byte(`{"space_id":"space-9"}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	require.Equal(t, "space-9", got.SpaceID)
}

// Storage 鉴权注入对 camelCase 别名只输出一份凭据。
func TestSignStorageBodyNormalizesProtoJSONAliases(t *testing.T) {
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	for _, body := range []string{
		`{"authInfo":{"appId":"browser","appKey":"x","operator":"op"},"spaceId":"s"}`,
		`{"auth_info":{"appId":"browser","appKey":"x","operator":"op"},"spaceId":"s"}`,
		`{"space_id":"s"}`,
	} {
		out, err := signStorageBody("trpc.moox.storage.Metadata", []byte(body), "")
		require.NoError(t, err)
		var got map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(out, &got))
		require.NotContains(t, got, "authInfo")
		var auth map[string]any
		require.NoError(t, json.Unmarshal(got["auth_info"], &auth))
		require.NotContains(t, auth, "appId")
		require.NotContains(t, auth, "appKey")
		require.Equal(t, "admin-gateway", auth["app_id"])
		require.Equal(t, storageServiceAuthKey("primary-secret", "admin-gateway"), auth["app_key"])
	}
	_, err := signStorageBody("trpc.moox.storage.Metadata", []byte(`null`), "")
	require.ErrorIs(t, err, errInvalidStorageBody)
}

func TestConsoleInjectsHeaderSpaceIntoStorageBodyWhenAbsent(t *testing.T) {
	_, secret, key, sid := setupRequestAuthTest(t)
	var raw []byte
	gateway := forwardFunc(func(_ context.Context, _, _ string, _ int, body []byte) ([]byte, error) {
		raw = body
		return []byte(`{}`), nil
	})
	rr := httptest.NewRecorder()
	newTestRouter(t, nil, gateway, &fakeSpaceAuthorizer{}).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/storage/ListDatasets", "space-1", []byte(`{"page":{"page":1}}`)))
	require.Equal(t, http.StatusOK, rr.Code)
	// 空间在嵌套消息里的方法（view.space_id）不能回填顶层字段，缺省时直接 400。
	_, secret, key, sid = setupRequestAuthTest(t) // 新的 nonce 存储，避免被当作重放
	rr = httptest.NewRecorder()
	newTestRouter(t, nil, gateway, &fakeSpaceAuthorizer{}).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/storage/UpdateView", "space-1", []byte(`{"view":{"view_id":"v1"}}`)))
	require.Equal(t, http.StatusBadRequest, rr.Code)
	// ListSpaceMembers 同样要求请求体声明空间。
	_, secret, key, sid = setupRequestAuthTest(t)
	rr = httptest.NewRecorder()
	newTestRouter(t, nil, gateway, &fakeSpaceAuthorizer{}).buildControlRouter().ServeHTTP(rr, browserRequest(t, secret, key, sid, "/api/admin/space/ListSpaceMembers", "space-1", []byte(`{}`)))
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
