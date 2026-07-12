package httpclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPClient_Get_ShouldDelegateToGetWithIP(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := NewHTTPClient()
	client.httpClient = server.Client()

	var result map[string]string
	err = client.Get(context.Background(), parsed.Host, parsed.Path, nil, &result)
	require.NoError(t, err)
	assert.Equal(t, "ok", result["status"])
}

func TestHTTPClient_Get_ShouldDecodeJSONResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := NewHTTPClient()
	client.httpClient = server.Client()

	var result map[string]string
	err = client.GetWithIP(context.Background(), parsed.Host, parsed.Path, nil, &result, "")
	require.NoError(t, err)
	assert.Equal(t, "ok", result["status"])
}

func TestHTTPClient_Get_ShouldRejectNonOKStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)

	client := NewHTTPClient()
	client.httpClient = server.Client()
	var result map[string]string
	err = client.GetWithIP(context.Background(), parsed.Host, parsed.Path, nil, &result, "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "HTTP 错误")
}
