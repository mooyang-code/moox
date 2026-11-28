package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPRequestHandler_ParseRequestParams_ValidPath_ShouldReturnServiceAndMethod(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login", nil)
	req = mux.SetURLVars(req, map[string]string{"service": "auth", "method": "login"})

	serviceID, method, err := h.parseRequestParams(req)
	require.NoError(t, err)
	assert.Equal(t, "auth", serviceID)
	assert.Equal(t, "login", method)
}

func TestHTTPRequestHandler_ParseRequestParams_MissingService_ShouldError(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/admin//login", nil)
	req = mux.SetURLVars(req, map[string]string{"method": "login"})

	_, _, err := h.parseRequestParams(req)
	require.Error(t, err)
}

func TestHTTPRequestHandler_ReadRequestBodyWithRaw_QueryDoesNotBecomeRPCBody(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/auth/login?foo=bar&baz=1", nil)

	raw, body, err := h.readRequestBodyWithRaw(req)
	require.NoError(t, err)
	assert.Empty(t, raw)

	assert.Empty(t, body)
}

func TestHTTPRequestHandler_ReadRequestBodyWithRaw_ReturnsExactBodyAndIgnoresQuery(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/login?foo=query", strings.NewReader(`{"foo":"body"}`))

	raw, body, err := h.readRequestBodyWithRaw(req)
	require.NoError(t, err)
	assert.JSONEq(t, `{"foo":"body"}`, string(raw))

	assert.Equal(t, raw, body)
}

// 透传给下游的请求头只有 trace、压缩与空间；用户、令牌、来源 IP 一律不从请求头取。
func TestHTTPRequestHandler_ExtractGatewayHeaders_ShouldCollectHeaders(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Access-Token", "token-1")
	req.Header.Set("X-Trace-Id", "trace-1")
	req.Header.Set("X-Client-Ip", "10.0.0.2")
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("X-Space-Id", "space-1")

	headers := h.extractGatewayHeaders(req)
	assert.Equal(t, map[string]string{"trace_id": "trace-1", "accept_encoding": "gzip", "space_id": "space-1"}, headers)
}

func TestHandleGatewayRequest_InvalidParams_ShouldReturnBadRequest(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/", nil)
	req = mux.SetURLVars(req, map[string]string{"service": "auth", "method": ""})
	_, _, err := h.parseRequestParams(req)
	require.Error(t, err)
}

func TestNewHandlerRequiresConfigAndDependencies(t *testing.T) {
	SetConfig(nil)
	_, err := NewHandler(nil, nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "控制台配置未初始化")
	SetConfig(&Config{})
	t.Cleanup(func() { SetConfig(nil) })
	_, err = NewHandler(nil, nil, nil)
	assert.Error(t, err)
}

type localActivityStub struct {
	pb.UnimplementedActivityLog
	calls int
	last  *pb.ListActivityLogsReq
	space string
}

func (s *localActivityStub) ListActivityLogs(ctx context.Context, req *pb.ListActivityLogsReq) (*pb.ListActivityLogsRsp, error) {
	s.calls++
	s.last = req
	s.space = gatewayclient.CallMetadataFromContext(ctx).SpaceID
	return &pb.ListActivityLogsRsp{RetInfo: &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}}, nil
}

// 控制台网关三档空间契约（global / space_optional / space_required）端到端 HTTP 路由矩阵验证。
func TestConsoleThreeTierSpacePolicyRouting(t *testing.T) {
	cases := []struct {
		name          string
		path          string
		headerSpace   string
		body          string
		wantStatus    int
		wantForwarded bool
		wantMetaSpace string
	}{
		// 1. 纯全局方法：无 X-Space-Id 直接放行且不注入 space_id
		{"strategy ListStrategies no space", "/api/admin/strategy/ListStrategies", "", `{}`, http.StatusOK, true, ""},
		{"strategy CreateStrategy no space", "/api/admin/strategy/CreateStrategy", "", `{"strategy":{"dsl_yaml":"name: a"}}`, http.StatusOK, true, ""},
		{"strategy UpdateStrategy no space", "/api/admin/strategy/UpdateStrategy", "", `{"strategy_id":"s1","dsl_yaml":"name: a"}`, http.StatusOK, true, ""},
		{"strategy DeleteStrategy no space", "/api/admin/strategy/DeleteStrategy", "", `{"strategy_id":"s1"}`, http.StatusOK, true, ""},
		{"strategy GetLifecycle no space", "/api/admin/strategy/GetLifecycle", "", `{}`, http.StatusOK, true, ""},
		{"trade InspectExchangeCredential no space", "/api/admin/trade/InspectExchangeCredential", "", `{"exchange":"binance","environment":"ACCOUNT_ENVIRONMENT_PRODUCTION","credential_secret_id":"s1"}`, http.StatusOK, true, ""},
		{"trade ListBlockedInstruments no space", "/api/admin/trade/ListBlockedInstruments", "", `{}`, http.StatusOK, true, ""},
		{"trade UnblockInstrument no space", "/api/admin/trade/UnblockInstrument", "", `{"exchange":"binance","market_type":"spot","exchange_symbol":"BTCUSDT"}`, http.StatusOK, true, ""},
		{"trade ListInstrumentHolders no space", "/api/admin/trade/ListInstrumentHolders", "", `{"exchange":"binance","market_type":"spot","exchange_symbol":"BTCUSDT"}`, http.StatusOK, true, ""},
		{"trade ListDelists no space", "/api/admin/trade/ListDelists", "", `{}`, http.StatusOK, true, ""},
		{"trade GetLifecycle no space", "/api/admin/trade/GetLifecycle", "", `{}`, http.StatusOK, true, ""},
		{"collector ListCloudAccounts no space", "/api/admin/collector/ListCloudAccounts", "", `{}`, http.StatusOK, true, ""},
		{"collector ListCloudRegions no space", "/api/admin/collector/ListCloudRegions", "", `{}`, http.StatusOK, true, ""},
		{"collector CreateCloudAccount no space", "/api/admin/collector/CreateCloudAccount", "", `{"account":{"account_id":"a1"}}`, http.StatusOK, true, ""},
		{"collector DeleteCloudAccount no space", "/api/admin/collector/DeleteCloudAccount", "", `{"account_id":"a1"}`, http.StatusOK, true, ""},
		{"collector GetLifecycle no space", "/api/admin/collector/GetLifecycle", "", `{}`, http.StatusOK, true, ""},
		{"archive GetLifecycle no space", "/api/admin/archive/GetLifecycle", "", `{}`, http.StatusOK, true, ""},
		{"storage ListSpaces no space", "/api/admin/storage/ListSpaces", "", `{}`, http.StatusOK, true, ""},
		{"storage CreateSpace strips stray header space", "/api/admin/storage/CreateSpace", "crypto", `{"space":{"space_id":"stockcn"}}`, http.StatusOK, true, ""},
		{"factor CreateFactor no space", "/api/admin/factor-mgr/CreateFactor", "", `{}`, http.StatusOK, true, ""},
		{"factor ListFactorCategories no space", "/api/admin/factor-mgr/ListFactorCategories", "", `{}`, http.StatusOK, true, ""},
		{"factor ListFactorFunctions no space", "/api/admin/factor-mgr/ListFactorFunctions", "", `{}`, http.StatusOK, true, ""},
		{"factor ListUdfs no space", "/api/admin/factor-mgr/ListUdfs", "", `{}`, http.StatusOK, true, ""},

		// 2. space_optional：无空间放行；有空间校验一致性并透传
		{"strategy GetStrategy no space", "/api/admin/strategy/GetStrategy", "", `{"strategy_id":"s1"}`, http.StatusOK, true, ""},
		{"strategy GetStrategy with space", "/api/admin/strategy/GetStrategy", "crypto", `{"strategy_id":"s1"}`, http.StatusOK, true, "crypto"},
		{"strategy ValidateStrategy no space", "/api/admin/strategy/ValidateStrategy", "", `{"dsl_yaml":"name: a"}`, http.StatusOK, true, ""},
		{"strategy ValidateStrategy with space", "/api/admin/strategy/ValidateStrategy", "crypto", `{"dsl_yaml":"name: a"}`, http.StatusOK, true, "crypto"},
		{"strategy ValidateStrategy mismatched body space", "/api/admin/strategy/ValidateStrategy", "crypto", `{"space_id":"stockcn"}`, http.StatusBadRequest, false, ""},
		{"factor ListFactorTasks no space", "/api/admin/factor-mgr/ListFactorTasks", "", `{}`, http.StatusOK, true, ""},
		{"factor ListFactorTasks blank header trimmed", "/api/admin/factor-mgr/ListFactorTasks", "   ", `{}`, http.StatusOK, true, ""},
		{"factor ListFactorTasks with space header", "/api/admin/factor-mgr/ListFactorTasks", "crypto", `{}`, http.StatusOK, true, "crypto"},
		{"factor ListFactorTasks with body space only", "/api/admin/factor-mgr/ListFactorTasks", "", `{"space_id":"stockcn"}`, http.StatusOK, true, "stockcn"},
		{"factor ListFactorTasks mismatched space", "/api/admin/factor-mgr/ListFactorTasks", "crypto", `{"space_id":"stockcn"}`, http.StatusBadRequest, false, ""},

		// 3. space_required：无空间仍严格拒绝（403/400）；带错配 space_id 返回 400
		{"strategy ListStrategyInstances no space", "/api/admin/strategy/ListStrategyInstances", "", `{}`, http.StatusForbidden, false, ""},
		{"strategy ListStrategyInstances with space", "/api/admin/strategy/ListStrategyInstances", "crypto", `{}`, http.StatusOK, true, "crypto"},
		{"strategy ListStrategyInstances mismatched space", "/api/admin/strategy/ListStrategyInstances", "crypto", `{"space_id":"stockcn"}`, http.StatusBadRequest, false, ""},
		{"trade ListLogicalAccounts no space", "/api/admin/trade/ListLogicalAccounts", "", `{}`, http.StatusForbidden, false, ""},
		{"trade ListLogicalAccounts with space", "/api/admin/trade/ListLogicalAccounts", "crypto", `{}`, http.StatusOK, true, "crypto"},
		{"trade ListLogicalAccounts mismatched space", "/api/admin/trade/ListLogicalAccounts", "crypto", `{"space_id":"stockcn"}`, http.StatusBadRequest, false, ""},
		{"collector GetTaskList no space", "/api/admin/collector/GetTaskList", "", `{}`, http.StatusForbidden, false, ""},
		{"archive ListArchiveTasks no space", "/api/admin/archive/ListArchiveTasks", "", `{}`, http.StatusForbidden, false, ""},
		{"factor DeleteFactorTask no space", "/api/admin/factor-mgr/DeleteFactorTask", "", `{"task_id":"t1"}`, http.StatusForbidden, false, ""},
		{"factor PreviewFactor no space", "/api/admin/factor-mgr/PreviewFactor", "", `{"task_id":"t1"}`, http.StatusForbidden, false, ""},
		{"storage ListDatasets no space", "/api/admin/storage/ListDatasets", "", `{}`, http.StatusBadRequest, false, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, current := setupSessionTest(t)
			calls := 0
			var gotSpace string
			gateway := forwardFunc(func(ctx context.Context, _, _ string, _ int, _ []byte) ([]byte, error) {
				calls++
				gotSpace = gatewayclient.CallMetadataFromContext(ctx).SpaceID
				return []byte(`{"ret_info":{"code":0}}`), nil
			})
			rr := httptest.NewRecorder()
			newTestRouter(t, store, nil, gateway).buildControlRouter().ServeHTTP(
				rr, browserRequest(t, current, tc.path, tc.headerSpace, []byte(tc.body)),
			)
			require.Equal(t, tc.wantStatus, rr.Code, rr.Body.String())
			if tc.wantForwarded {
				require.Equal(t, 1, calls)
				require.Equal(t, tc.wantMetaSpace, gotSpace)
			} else {
				require.Zero(t, calls, "被拒绝的请求不得转发")
			}
		})
	}
}

// ActivityLog.ListActivityLogs 为 space_optional：支持无空间 global=true、按空间查询，并拒绝请求头与请求体空间错配。
func TestConsoleActivityLogsSpaceOptionalRouting(t *testing.T) {
	store, current := setupSessionTest(t)
	local, err := NewLocalDispatcher()
	require.NoError(t, err)
	stub := &localActivityStub{}
	require.NoError(t, local.Register(&pb.ActivityLogServer_ServiceDesc, stub))
	router := newTestRouter(t, store, local, nil).buildControlRouter()

	// 无空间 + global=true 放行
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/activity/ListActivityLogs", "", []byte(`{"global":true}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.True(t, stub.last.GetGlobal())
	require.Empty(t, stub.space)

	// 有请求头 X-Space-Id 但 body 查 global=true 放行
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/activity/ListActivityLogs", "crypto", []byte(`{"global":true}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.True(t, stub.last.GetGlobal())

	// 无请求头但 body 指定 space_id，透传到 CallMetadata
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/activity/ListActivityLogs", "", []byte(`{"space_id":"crypto"}`)))
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	require.Equal(t, "crypto", stub.last.GetSpaceId())
	require.Equal(t, "crypto", stub.space)

	// 请求头与请求体 space_id 错配返回 400
	before := stub.calls
	rr = httptest.NewRecorder()
	router.ServeHTTP(rr, browserRequest(t, current, "/api/admin/activity/ListActivityLogs", "crypto", []byte(`{"space_id":"stockcn"}`)))
	require.Equal(t, http.StatusBadRequest, rr.Code, rr.Body.String())
	require.Equal(t, before, stub.calls)
}
