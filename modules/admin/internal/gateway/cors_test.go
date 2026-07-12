package gateway

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
