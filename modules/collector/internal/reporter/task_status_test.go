package reporter

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-go/codec"
)

type asyncContextKey struct{}

func TestCloneAsyncContextDetachesCancellationAndCopiesTRPCMessage(t *testing.T) {
	ctx, msg := codec.WithNewMessage(context.Background())
	msg.WithCalleeMethod("collector.ReportTaskStatus")
	ctx = context.WithValue(ctx, asyncContextKey{}, "trace-value")
	ctx, cancel := context.WithTimeout(ctx, time.Hour)

	asyncCtx := cloneAsyncContext(ctx)
	cancel()

	if err := ctx.Err(); err != context.Canceled {
		t.Fatalf("source context error = %v, want canceled", err)
	}
	if err := asyncCtx.Err(); err != nil {
		t.Fatalf("async context error = %v, want nil", err)
	}
	if _, ok := asyncCtx.Deadline(); ok {
		t.Fatal("async context unexpectedly retained the upstream deadline")
	}
	if got := asyncCtx.Value(asyncContextKey{}); got != "trace-value" {
		t.Fatalf("async context value = %v, want trace-value", got)
	}
	if got := codec.Message(asyncCtx).CalleeMethod(); got != "collector.ReportTaskStatus" {
		t.Fatalf("async context callee method = %q", got)
	}
}

func TestCloneAsyncContextAcceptsNil(t *testing.T) {
	ctx := cloneAsyncContext(nil)
	if ctx == nil || codec.Message(ctx) == nil {
		t.Fatal("cloneAsyncContext(nil) must return an initialized tRPC context")
	}
}

func TestSendRequestReturnsMalformedResponseError(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("target node = %q", got)
		}
		_, _ = w.Write([]byte("not-json"))
	}))
	defer server.Close()

	err := sendRequest(context.Background(), server.URL, []byte(`{}`), server.Client())
	if err == nil || !strings.Contains(err.Error(), "解析服务端响应失败") {
		t.Fatalf("sendRequest() error = %v, want malformed response error", err)
	}
}

func TestSendRequestReturnsRetInfoError(t *testing.T) {
	t.Setenv("MOOX_GATEWAY_NODE_ID", "gateway-gz-122")
	t.Setenv("MOOX_GATEWAY_SERVICE_KEY_ID", "test-ak")
	t.Setenv("MOOX_GATEWAY_SERVICE_SECRET_KEY", "test-sk")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Moox-Target-Node"); got != "gateway-gz-122" {
			t.Fatalf("target node = %q", got)
		}
		_, _ = w.Write([]byte(`{"ret_info":{"code":1,"msg":"bad"}}`))
	}))
	defer server.Close()

	err := sendRequest(context.Background(), server.URL, []byte(`{}`), server.Client())
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("sendRequest() error = %v, want ret_info error", err)
	}
}
