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

type fakeSpaceAuthorizer struct {
	err     error
	spaceID string
}

func (a *fakeSpaceAuthorizer) AuthorizeSpaceRequest(_ context.Context, spaceID string) error {
	a.spaceID = spaceID
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
