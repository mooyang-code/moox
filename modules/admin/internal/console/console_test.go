package console

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "trpc.group/trpc-go/trpc-filter/masking"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/filter"
)

type forwardedCall struct {
	path, method  string
	serialization int
	body          []byte
}

type fakeForwarder struct {
	calls    []forwardedCall
	response []byte
	err      error
}

func (f *fakeForwarder) Forward(_ context.Context, servicePath, method string, serialization int, body []byte, _ ...gatewayclient.CallOption) ([]byte, error) {
	f.calls = append(f.calls, forwardedCall{path: servicePath, method: method, serialization: serialization, body: append([]byte(nil), body...)})
	if f.err != nil {
		return nil, f.err
	}
	if f.response != nil {
		return f.response, nil
	}
	return body, nil
}

type fakeSecretMgr struct {
	pb.UnimplementedSecretMgr
	userID string
}

func (f *fakeSecretMgr) ListSecrets(ctx context.Context, req *pb.ListSecretsReq) (*pb.ListSecretsRsp, error) {
	f.userID = string(trpc.GetMetaData(ctx, authmodel.CtxUserID))
	return &pb.ListSecretsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS}, Secrets: []*pb.Secret{{Name: "binance", SecretValue: "plain-text-secret-value"}}}, nil
}

func (f *fakeSecretMgr) GetSecretValue(context.Context, *pb.GetSecretValueReq) (*pb.GetSecretValueRsp, error) {
	return nil, errors.New("浏览器不应该调到 GetSecretValue")
}

type consoleFixture struct {
	router  http.Handler
	remote  *fakeForwarder
	secrets *fakeSecretMgr
	auth    *fakeTradeSpaceAuthorizer
	secret  string
	key     string
	sid     string
	nonce   int
}

type fakeTradeSpaceAuthorizer struct {
	err                     error
	userID, spaceID, method string
	globalRole              int32
}

func (a *fakeTradeSpaceAuthorizer) AuthorizeTradeRequest(_ context.Context, userID, spaceID, method string, globalRole int32) error {
	a.userID, a.spaceID, a.method, a.globalRole = userID, spaceID, method, globalRole
	return a.err
}

func newConsoleFixture(t *testing.T) *consoleFixture {
	t.Helper()
	_, secret, key, sid := setupRequestAuthTest(t)
	t.Setenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET", "primary-secret")
	t.Setenv("MOOX_STORAGE_VIEW_AUTH_SECRET", "view-secret")
	local := NewLocalServices(filter.GetServer("masking"))
	secrets := &fakeSecretMgr{}
	require.NoError(t, local.Register(&pb.SecretMgrServer_ServiceDesc, secrets))
	remote := &fakeForwarder{}
	auth := &fakeTradeSpaceAuthorizer{}
	router := NewRouter(Options{Local: local, Remote: remote, Authorizer: auth}).Handler()
	return &consoleFixture{router: router, remote: remote, secrets: secrets, auth: auth, secret: secret, key: key, sid: sid}
}

// call 发出一个带登录态和请求签名的浏览器请求。
func (f *consoleFixture) call(t *testing.T, path string, body []byte, spaceID string) *httptest.ResponseRecorder {
	t.Helper()
	f.nonce++
	timestamp := time.Now().Unix()
	nonce := fmt.Sprintf("%064x", f.nonce)
	headers := map[string]string{}
	if spaceID != "" {
		headers[requestauth.HeaderSpaceID] = spaceID
	}
	token, err := mooxsecurity.SignToken(map[string]any{"user_id": "u1", "username": "admin", "role": 2, "token_type": "access", "sid": f.sid}, f.secret, "moox-admin", time.Hour)
	require.NoError(t, err)
	signature, err := requestauth.Sign(f.key, requestauth.Material{Method: http.MethodPost, Path: path, Body: body, Headers: headers, Timestamp: timestamp, Nonce: nonce})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Authorization", token)
	req.Header.Set("X-Moox-Timestamp", fmt.Sprint(timestamp))
	req.Header.Set("X-Moox-Nonce", nonce)
	req.Header.Set("X-Moox-Signature", signature)
	if spaceID != "" {
		req.Header.Set(requestauth.HeaderSpaceID, spaceID)
	}
	ctx, _ := codec.WithNewMessage(req.Context())
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req.WithContext(ctx))
	return rr
}

func TestConsoleForwardsJSONUnchangedByConsoleName(t *testing.T) {
	f := newConsoleFixture(t)
	body := []byte(`{"space_id":"s1","page":{"page":1,"size":20}}`)
	rr := f.call(t, "/api/admin/collector/GetTaskList", body, "s1")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Len(t, f.remote.calls, 1)
	call := f.remote.calls[0]
	assert.Equal(t, "trpc.moox.collector.CollectMgr", call.path)
	assert.Equal(t, "GetTaskList", call.method)
	assert.Equal(t, codec.SerializationTypeJSON, call.serialization)
	assert.Equal(t, body, call.body, "JSON 透传后字节不变")
	assert.Equal(t, string(body), rr.Body.String())
	assert.Equal(t, "u1", f.auth.userID, "空间作用域请求经过空间授权")
	assert.Equal(t, "s1", f.auth.spaceID)
}

func TestConsoleRejectsMethodsNotOpenToConsole(t *testing.T) {
	f := newConsoleFixture(t)
	for _, path := range []string{
		"/api/admin/cloudnode/CollectGarbage",
		"/api/admin/secret/GetSecretValue",
		"/api/admin/publishlease/ValidateCollectorPublishLease",
		"/api/admin/publishlease/BeginCollectorPublishOperation",
		"/api/admin/trade/ClaimLogicalAccountOwner",
		"/api/admin/storage/ApplyTagSnapshot",
		"/api/admin/storage-primary/ListDatasets",
		"/api/admin/collectmgr/GetTaskList",
		"/api/admin/sysdeploy/SyncHostPlacements",
		"/api/admin/gatewaycontrol/PullSnapshot",
	} {
		rr := f.call(t, path, []byte(`{}`), "s1")
		assert.Equal(t, http.StatusNotFound, rr.Code, path)
	}
	assert.Empty(t, f.remote.calls)
}

func TestConsoleCallsAdminServicesInProcessWithMasking(t *testing.T) {
	f := newConsoleFixture(t)
	rr := f.call(t, "/api/admin/secret/ListSecrets", []byte(`{}`), "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Empty(t, f.remote.calls, "管理后台自己的服务不经过主机网关")
	assert.Equal(t, "u1", f.secrets.userID, "进程内调用带上登录用户")
	var rsp struct {
		Secrets []struct {
			SecretValue string `json:"secret_value"`
		} `json:"secrets"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &rsp))
	require.Len(t, rsp.Secrets, 1)
	assert.NotContains(t, rsp.Secrets[0].SecretValue, "plain-text-secret", "进程内调用同样经过脱敏")
}

func TestConsoleStorageInjectsConsoleAppAuth(t *testing.T) {
	f := newConsoleFixture(t)
	rr := f.call(t, "/api/admin/storage/QueryTimeSeriesRows", []byte(`{"view_id":"v1"}`), "")
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Len(t, f.remote.calls, 1)
	assert.Equal(t, "trpc.moox.storage.DataView", f.remote.calls[0].path)
	var payload struct {
		ViewID   string `json:"view_id"`
		AuthInfo struct {
			AppID  string `json:"app_id"`
			AppKey string `json:"app_key"`
		} `json:"auth_info"`
	}
	require.NoError(t, json.Unmarshal(f.remote.calls[0].body, &payload))
	assert.Equal(t, "v1", payload.ViewID)
	assert.Equal(t, "console", payload.AuthInfo.AppID)
	assert.Equal(t, storageServiceAuthKey("view-secret", "console"), payload.AuthInfo.AppKey)

	rr = f.call(t, "/api/admin/storage/UpsertFields", []byte(`{}`), "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "trpc.moox.storage.PrimaryStore", f.remote.calls[1].path)
	require.NoError(t, json.Unmarshal(f.remote.calls[1].body, &payload))
	assert.Equal(t, storageServiceAuthKey("primary-secret", "console"), payload.AuthInfo.AppKey)
}

func TestConsoleSpaceScopedRequestsAreAuthorized(t *testing.T) {
	f := newConsoleFixture(t)
	rr := f.call(t, "/api/admin/trade/ListOrders", []byte(`{}`), "")
	assert.Equal(t, http.StatusForbidden, rr.Code, "缺少空间")
	f.auth.err = errors.New("不是空间成员")
	rr = f.call(t, "/api/admin/trade/ListOrders", []byte(`{}`), "s1")
	assert.Equal(t, http.StatusForbidden, rr.Code)
	f.auth.err = nil
	rr = f.call(t, "/api/admin/strategy/ListStrategies", []byte(`{"space_id":"s2"}`), "s1")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "space_id")
	assert.Equal(t, "1", rr.Header().Get("trpc-ret"), "请求体里嵌套其他空间被拒绝")
	assert.Empty(t, f.remote.calls)
}

func TestConsoleSurfacesRemoteErrors(t *testing.T) {
	f := newConsoleFixture(t)
	f.remote.err = errors.New("服务 trpc.moox.monitor.MonitorMgr 没有已启用的部署")
	rr := f.call(t, "/api/admin/monitor/GetHealthOverview", []byte(`{}`), "")
	require.Equal(t, http.StatusOK, rr.Code)
	assert.NotEqual(t, "0", rr.Header().Get("trpc-ret"))
	assert.Contains(t, rr.Body.String(), "没有已启用的部署")
}

func TestConsoleRequiresAuthentication(t *testing.T) {
	f := newConsoleFixture(t)
	req := httptest.NewRequest(http.MethodPost, "/api/admin/collector/GetTaskList", strings.NewReader(`{}`))
	ctx, _ := codec.WithNewMessage(req.Context())
	rr := httptest.NewRecorder()
	f.router.ServeHTTP(rr, req.WithContext(ctx))
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
	assert.Empty(t, f.remote.calls)
}

func TestConsoleRawHandlersStayInProcess(t *testing.T) {
	store, _, _, sid := setupRequestAuthTest(t)
	SetRawSessionOwnerVerifier(func(sessionID, userID string) bool { return sessionID == "ssh-1" && userID == "u1" })
	t.Cleanup(func() { SetRawSessionOwnerVerifier(nil) })
	served := ""
	RegisterRawHandler("ssh", "SftpDownload", func(w http.ResponseWriter, r *http.Request) {
		served = r.URL.Query().Get("path")
		_, _ = w.Write([]byte("file-bytes"))
	})
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers["ssh"], "SftpDownload")
		rawHandlersMutex.Unlock()
	})
	store.tickets["ticket-1"] = authmodel.RawSessionTicket{TicketID: "ticket-1", SessionID: sid, ResourceSessionID: "ssh-1", UserID: "u1", Operation: "sftp_download", ExpiresAt: time.Now().Add(time.Minute)}
	remote := &fakeForwarder{}
	router := NewRouter(Options{Remote: remote}).Handler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/ssh/SftpDownload?ticket=ticket-1&session_id=ssh-1&path=/tmp/a.txt", nil)
	ctx, _ := codec.WithNewMessage(req.Context())
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req.WithContext(ctx))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	assert.Equal(t, "file-bytes", rr.Body.String())
	assert.Equal(t, "/tmp/a.txt", served)
	assert.Empty(t, remote.calls)
}

func TestValidateSpaceScopedBody(t *testing.T) {
	assert.NoError(t, validateSpaceScopedBody("s1", []byte(`{"space_id":"s1","items":[{"space_id":"s1"}]}`)))
	assert.Error(t, validateSpaceScopedBody("s1", []byte(`{"items":[{"spaceId":"s2"}]}`)))
	assert.NoError(t, validateSpaceScopedBody("", []byte(`{"space_id":"s2"}`)))
	assert.Error(t, validateSpaceScopedBody("s1", []byte(`{bad`)))
}
