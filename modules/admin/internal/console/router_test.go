package console

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/requestauth"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
)

type forwardFunc func(context.Context, string, string, int, []byte) ([]byte, error)

func (f forwardFunc) Forward(ctx context.Context, service, method string, serialization int, body []byte) ([]byte, error) {
	return f(ctx, service, method, serialization, body)
}

func newTestRouter(t *testing.T, local *LocalDispatcher, gateway RPCForwarder, authorizers ...TradeSpaceAuthorizer) *HTTPRouter {
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
				require.Equal(t, body, raw)
				require.Equal(t, gatewayclient.CallMetadata{SpaceID: "space-1", UserID: "u1", UserRole: "2", TraceID: "trace-1"}, gatewayclient.CallMetadataFromContext(ctx))
				return response, nil
			})
			authorizer := &fakeTradeSpaceAuthorizer{}
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
				require.Equal(t, tc.method, authorizer.method)
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
		"cloudnode/CollectGarbage", "publishlease/ValidateCollectorPublishLease",
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
			authorizer := &fakeTradeSpaceAuthorizer{}
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
	router := newTestRouter(t, nil, gateway).buildControlRouter()
	body := []byte(`{"offset":1}`)
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
			r := browserRequest(t, secret, key, sid, "/api/admin/storage/RequestViewRebuild", "", []byte(`{}`))
			token, err := mooxsecurity.SignToken(map[string]any{"user_id": "u1", "username": "admin", "role": role, "token_type": "access", "sid": sid}, secret, "moox-admin", time.Hour)
			require.NoError(t, err)
			r.Header.Set("Authorization", token)
			rr := httptest.NewRecorder()
			newTestRouter(t, nil, gateway).buildControlRouter().ServeHTTP(rr, r)
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
