package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCORSMiddleware_AllowedPreflight_ShouldStopBeforeHandler(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	called := false
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/admin/auth/Login", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set(
		"Access-Control-Request-Headers",
		"Authorization, X-Space-Id, X-Moox-Timestamp, X-Moox-Nonce, X-Moox-Signature",
	)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNoContent, rr.Code)
	assert.False(t, called)
	assert.Equal(t, "https://app.example.com", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Values("Vary"), "Origin")
	assert.Contains(t, rr.Header().Values("Vary"), "Access-Control-Request-Method")
	assert.Contains(t, rr.Header().Values("Vary"), "Access-Control-Request-Headers")
}

func TestCORSMiddleware_DisallowedOrigin_ShouldRejectRequest(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	called := false
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	req := httptest.NewRequest(http.MethodPost, "/api/admin/auth/Login", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
	assert.False(t, called)
	assert.Contains(t, rr.Header().Values("Vary"), "Origin")
}

func TestCORSMiddleware_UnsupportedPreflightHeader_ShouldRejectRequest(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	handler := corsMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("preflight must not reach the application handler")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/admin/auth/Login", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "X-Not-Allowed")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusForbidden, rr.Code)
}

func TestCORSMiddleware_NoOrigin_ShouldPassThrough(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	handler := corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/auth/Login", nil))

	assert.Equal(t, http.StatusCreated, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestControlRouter_UnknownPreflightPath_ShouldReturnNotFound(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	router := NewHTTPRouter(NewGatewayHandle()).buildControlRouter()
	req := httptest.NewRequest(http.MethodOptions, "/not-an-admin-route", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestControlRouter_NonPreflightOptions_ShouldReturnMethodNotAllowed(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	router := NewHTTPRouter(NewGatewayHandle()).buildControlRouter()
	req := httptest.NewRequest(http.MethodOptions, "/api/admin/auth/Login", nil)
	req.Header.Set("Origin", "https://app.example.com")
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
}

func TestControlRouter_OptionsWithoutOrigin_ShouldReturnMethodNotAllowed(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	router := NewHTTPRouter(NewGatewayHandle()).buildControlRouter()
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/api/admin/auth/Login", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Equal(t, "GET, POST, PUT, DELETE", rr.Header().Get("Allow"))
}

func TestServiceRouter_Preflight_ShouldNotEnableBrowserCORS(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	router := NewHTTPRouter(NewGatewayHandle()).buildServiceRouter()
	req := httptest.NewRequest(http.MethodOptions, "/api/service/cloudnode/PollJobItems", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestApplyCORSHeaders_AllowedOrigin_ShouldSetHeaders(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	rr := httptest.NewRecorder()

	applyCORSHeaders(rr, "https://app.example.com")

	assert.Equal(t, "https://app.example.com", rr.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Methods"), "GET")
	assert.Contains(t, rr.Header().Get("Access-Control-Allow-Headers"), "Authorization")
}

func TestApplyCORSHeaders_WildcardOrigin_ShouldAllowAny(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"*"}}})
	rr := httptest.NewRecorder()

	applyCORSHeaders(rr, "https://other.example.com")

	assert.Equal(t, "https://other.example.com", rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestApplyCORSHeaders_DisallowedOrigin_ShouldSkipHeaders(t *testing.T) {
	SetConfig(&Config{CORS: CORSConfig{AllowedOrigins: []string{"https://app.example.com"}}})
	rr := httptest.NewRecorder()

	applyCORSHeaders(rr, "https://evil.example.com")

	assert.Empty(t, rr.Header().Get("Access-Control-Allow-Origin"))
}

func TestIsOriginAllowed_EmptyList_ShouldReturnFalse(t *testing.T) {
	assert.False(t, isOriginAllowed("https://app.example.com", nil))
}

func TestContainsWildcard_WithStar_ShouldReturnTrue(t *testing.T) {
	assert.True(t, containsWildcard([]string{" https://a.com ", "*"}))
	assert.False(t, containsWildcard([]string{"https://a.com"}))
}
