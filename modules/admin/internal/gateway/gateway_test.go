package gateway

import (
	"bytes"
	"context"
	"github.com/gorilla/mux"
	authmodel "github.com/mooyang-code/moox/modules/admin/internal/service/auth/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"trpc.group/trpc-go/trpc-go"
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
	assert.Equal(t, "token-1", headers["access_token"])
	assert.Equal(t, "trace-1", headers["trace_id"])
	assert.Equal(t, "10.0.0.2", headers["client_ip"])
	assert.Equal(t, "https://app.example.com", headers["origin"])
	assert.Equal(t, "gzip", headers["accept_encoding"])
	assert.Equal(t, "space-1", headers["space_id"])
}

func TestHTTPRequestHandler_GetClientIP_FromXRealIP_ShouldReturnIP(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Real-IP", "203.0.113.1")
	assert.Equal(t, "203.0.113.1", h.getClientIP(req))
}

func TestHTTPRequestHandler_GetClientIP_FromXForwardedFor_ShouldReturnFirstIP(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
	assert.Equal(t, "198.51.100.1", h.getClientIP(req))
}

func TestHTTPRequestHandler_GetClientIP_FromRemoteAddr_ShouldStripPort(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "192.0.2.1:12345"
	assert.Equal(t, "192.0.2.1", h.getClientIP(req))
}

func TestHandleGatewayRequest_InvalidParams_ShouldReturnBadRequest(t *testing.T) {
	h := NewHTTPRequestHandler()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/", nil)
	req = mux.SetURLVars(req, map[string]string{"service": "auth", "method": ""})
	_, _, err := h.parseRequestParams(req)
	require.Error(t, err)
}

func TestHandleGatewayRequest_RawHandlerHit_ShouldServeWithoutForward(t *testing.T) {
	SetConfig(&Config{Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/demo/Ping"}}})
	called := false
	RegisterRawHandler("demo", "Ping", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("pong"))
	})
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers, "demo")
		rawHandlersMutex.Unlock()
	})

	router := NewHTTPRouter(NewGatewayHandle(), nil)
	muxRouter := router.buildRouter()
	rr := httptest.NewRecorder()
	muxRouter.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/demo/Ping", nil))

	assert.True(t, called)
	assert.Equal(t, http.StatusTeapot, rr.Code)
	assert.Equal(t, "pong", rr.Body.String())
}

func TestHandleGatewayRequest_ForwardMissingResolver_ShouldReturnForwardError(t *testing.T) {
	SetConfig(&Config{
		JWT:     JWTConfig{SecretKey: "secret"},
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/auth/GetUserInfo"}},
	})
	SetServiceDetailResolver(nil)

	router := NewHTTPRouter(NewGatewayHandle(), nil)
	muxRouter := router.buildRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/GetUserInfo", bytes.NewReader([]byte(`{}`)))
	muxRouter.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Contains(t, rr.Body.String(), "ret_info")
}

func TestGetGatewayHandleInstance_ShouldReturnSingleton(t *testing.T) {
	a := GetGatewayHandleInstance()
	b := GetGatewayHandleInstance()
	assert.Same(t, a, b)
}

func TestHandleGatewayRequest_UserIDFromContext_ShouldInjectHeader(t *testing.T) {
	SetConfig(&Config{
		JWT:     JWTConfig{SecretKey: "secret"},
		CORS:    CORSConfig{AllowedOrigins: []string{"*"}},
		Gateway: GatewayConfig{NoAuthMethods: []string{"/api/admin/auth/GetUserInfo"}},
	})
	SetServiceDetailResolver(func(ctx context.Context, serviceID string) (ServiceDetail, bool) {
		return ServiceDetail{}, false
	})

	router := NewHTTPRouter(NewGatewayHandle(), nil)
	muxRouter := router.buildRouter()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/GetUserInfo", bytes.NewReader([]byte(`{}`)))
	ctx := context.WithValue(req.Context(), authmodel.CtxUserID, "user-ctx-1")
	trpc.SetMetaData(ctx, authmodel.CtxUserRole, []byte("1"))
	req = req.WithContext(ctx)
	muxRouter.ServeHTTP(rr, req)
	assert.Equal(t, http.StatusOK, rr.Code)
}
