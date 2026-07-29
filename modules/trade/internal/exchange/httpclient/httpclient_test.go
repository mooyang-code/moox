package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClient_New_ShouldTrimTrailingSlash(t *testing.T) {
	c := New("https://api.example.com/")
	assert.Equal(t, "https://api.example.com", c.BaseURL)
}

func TestClient_Do_ClassifiesHTTPAndTransportErrors(t *testing.T) {
	tests := []struct {
		status int
		kind   exchange.ErrorKind
	}{
		{http.StatusUnauthorized, exchange.ErrorAuthentication},
		{http.StatusNotFound, exchange.ErrorOrderNotFound},
		{http.StatusTooManyRequests, exchange.ErrorRateLimited},
		{http.StatusBadRequest, exchange.ErrorRejected},
		{http.StatusServiceUnavailable, exchange.ErrorTransportUnknown},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			_, err := New(server.URL).Do(context.Background(), &Request{Path: "/"})
			require.True(t, exchange.IsKind(err, test.kind), "error = %v", err)
		})
	}

	client := New("http://127.0.0.1:1")
	client.HTTP.Timeout = 50 * time.Millisecond
	_, err := client.Do(context.Background(), &Request{Path: "/", Timeout: 50 * time.Millisecond})
	require.True(t, exchange.IsKind(err, exchange.ErrorTransportUnknown), "error = %v", err)
}

func TestClient_Do_SuccessResponse_ShouldReturnBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/ping", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	c := New(server.URL)
	raw, err := c.Do(context.Background(), &Request{
		Method: http.MethodPost,
		Path:   "/v1/ping",
		Body:   []byte(`{"ping":1}`),
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"ok":true}`, string(raw))
}

func TestClient_Do_Non2xxResponse_ShouldReturnError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	c := New(server.URL)
	_, err := c.Do(context.Background(), &Request{Path: "/bad"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "http 400")
}

func TestClient_Do_DefaultMethodAndTimeout_ShouldUseGetAndDefaultTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := New(server.URL)
	_, err := c.Do(context.Background(), &Request{Path: "/health", Timeout: 2 * time.Second})
	require.NoError(t, err)
}

func TestTruncate_LongBody_ShouldAppendEllipsis(t *testing.T) {
	assert.Equal(t, "abcd...", truncate([]byte("abcdef"), 4))
	assert.Equal(t, "abc", truncate([]byte("abc"), 4))
}

func TestClient_Do_WithQuery_ShouldEncodeQueryString(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "BTCUSDT", r.URL.Query().Get("symbol"))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	c := New(server.URL)
	q := url.Values{}
	q.Set("symbol", "BTCUSDT")
	_, err := c.Do(context.Background(), &Request{Path: "/klines", Query: q})
	require.NoError(t, err)
}
