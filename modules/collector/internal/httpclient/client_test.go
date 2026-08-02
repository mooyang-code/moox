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

func TestHTTPClientGetUsesDomainDirectly(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	var result map[string]string
	require.NoError(t, NewHTTPClient(server.Client()).Get(context.Background(), parsed.Host, parsed.Path, nil, &result))
	assert.Equal(t, "ok", result["status"])
}

func TestHTTPClientGetRejectsNonOK(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	defer server.Close()
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	err = NewHTTPClient(server.Client()).Get(context.Background(), parsed.Host, parsed.Path, nil, &map[string]string{})
	statusErr := &StatusError{}
	require.ErrorAs(t, err, &statusErr)
	assert.Equal(t, http.StatusBadGateway, statusErr.StatusCode)
}
