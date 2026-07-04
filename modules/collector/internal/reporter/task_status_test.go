package reporter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendRequestReturnsMalformedResponseError(t *testing.T) {
	t.Setenv("MOOX_SERVICE_AUTH_ACCESS_KEY", "test-ak")
	t.Setenv("MOOX_SERVICE_AUTH_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	err := sendRequest(context.Background(), server.URL, []byte(`{}`), server.Client())
	if err == nil || !strings.Contains(err.Error(), "解析服务端响应失败") {
		t.Fatalf("sendRequest() error = %v, want malformed response error", err)
	}
}

func TestSendRequestReturnsRetInfoError(t *testing.T) {
	t.Setenv("MOOX_SERVICE_AUTH_ACCESS_KEY", "test-ak")
	t.Setenv("MOOX_SERVICE_AUTH_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ret_info":{"code":1,"msg":"bad"}}`))
	}))
	defer server.Close()

	err := sendRequest(context.Background(), server.URL, []byte(`{}`), server.Client())
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("sendRequest() error = %v, want ret_info error", err)
	}
}
