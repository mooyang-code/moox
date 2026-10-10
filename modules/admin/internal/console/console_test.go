package console

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeTradeSpaceAuthorizer struct {
	err        error
	userID     string
	spaceID    string
	method     string
	globalRole int32
}

func (a *fakeTradeSpaceAuthorizer) AuthorizeTradeRequest(_ context.Context, userID, spaceID, method string, globalRole int32) error {
	a.userID, a.spaceID, a.method, a.globalRole = userID, spaceID, method, globalRole
	return a.err
}

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

func TestGetConsoleHandleInstance_ShouldReturnSingleton(t *testing.T) {
	assert.Same(t, GetConsoleHandleInstance(), GetConsoleHandleInstance())
}
