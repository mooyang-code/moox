package bootstrap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsHTTPURL(t *testing.T) {
	assert.True(t, isHTTPURL("http://example.com"))
	assert.True(t, isHTTPURL("HTTPS://example.com"))
	assert.False(t, isHTTPURL(""))
	assert.False(t, isHTTPURL("127.0.0.1:20100"))
	assert.False(t, isHTTPURL("/relative"))
}

func TestIsHTTPProtocol(t *testing.T) {
	assert.True(t, isHTTPProtocol("http"))
	assert.True(t, isHTTPProtocol("HTTPS"))
	assert.False(t, isHTTPProtocol("trpc"))
	assert.False(t, isHTTPProtocol(""))
}

func TestNormalizeBaseURL(t *testing.T) {
	assert.Equal(t, "", normalizeBaseURL(""))
	assert.Equal(t, "http://gw.example.com", normalizeBaseURL("http://gw.example.com/"))
	assert.Equal(t, "https://gw.example.com", normalizeBaseURL("https://gw.example.com"))
	assert.Equal(t, "http://gw.example.com:11000", normalizeBaseURL("gw.example.com:11000"))
}

func TestIsStorageTRPCTarget(t *testing.T) {
	assert.True(t, isStorageTRPCTarget("127.0.0.1:20100"))
	assert.False(t, isStorageTRPCTarget(""))
	assert.False(t, isStorageTRPCTarget("http://127.0.0.1:20100"))
	assert.False(t, isStorageTRPCTarget("https://127.0.0.1:20100"))
}
