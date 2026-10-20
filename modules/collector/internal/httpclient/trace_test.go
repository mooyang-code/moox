package httpclient

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTraceRecordsPhasesAcrossRequestsAndReusesConnection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(30 * time.Millisecond)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	client := NewHTTPClient(base)
	trace := &Trace{}
	ctx := WithTrace(context.Background(), trace)
	domain := strings.TrimPrefix(server.URL, "https://")

	var out map[string]any
	require.NoError(t, client.Get(ctx, domain, "/a", url.Values{}, &out))
	require.NoError(t, client.Get(ctx, domain, "/b", url.Values{}, &out))

	snap := trace.Snapshot()
	require.Equal(t, 2, snap.Requests)
	require.Equal(t, 1, snap.NewConns, "第一次请求新建连接")
	require.Equal(t, 1, snap.ReusedConns, "第二次请求复用连接")
	require.GreaterOrEqual(t, snap.TTFBMs, int64(20), "首字节耗时包含服务端的 30ms 处理")
	require.GreaterOrEqual(t, snap.RequestMs, int64(60))
	require.Equal(t, http.StatusOK, snap.LastStatus)
	require.Equal(t, domain, snap.LastDomain)
	require.NotEmpty(t, snap.RemoteIP)
}

func TestTraceRecordsFailedRequestWithoutStatus(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	client := NewHTTPClient(base)
	trace := &Trace{}
	err := client.Get(WithTrace(context.Background(), trace), strings.TrimPrefix(server.URL, "https://"), "/", nil, nil)
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, trace.Snapshot().LastStatus)

	// 没有挂 Trace 时行为不变（仍返回 429 错误）。
	require.Error(t, client.Get(context.Background(), strings.TrimPrefix(server.URL, "https://"), "/x", nil, nil))
}

func TestTraceRecordsWhereAndWhyARequestFailed(t *testing.T) {
	// 服务端收到请求后不响应：失败应停在 wait_response，原因是超时。
	block := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-block
	}))
	defer server.Close()
	defer close(block)
	base := &http.Client{Timeout: 200 * time.Millisecond, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	client := NewHTTPClient(base)
	trace := &Trace{}
	domain := strings.TrimPrefix(server.URL, "https://")
	require.Error(t, client.Get(WithTrace(context.Background(), trace), domain, "/", nil, nil))

	attempts := trace.Snapshot().Attempts
	require.Len(t, attempts, 1)
	require.Equal(t, "wait_response", attempts[0].Phase)
	require.Contains(t, []string{"timeout", "deadline"}, attempts[0].Kind, "客户端超时可能表现为上下文截止")
	require.False(t, attempts[0].Reused)
	require.GreaterOrEqual(t, attempts[0].Ms, int64(150))
	require.Contains(t, AttemptsText(attempts), domain+":wait_response:"+attempts[0].Kind+":")

	// 连不上的端口：失败停在建连阶段。
	trace = &Trace{}
	require.Error(t, client.Get(WithTrace(context.Background(), trace), "127.0.0.1:1", "/", nil, nil))
	attempts = trace.Snapshot().Attempts
	require.Len(t, attempts, 1)
	require.Contains(t, []string{"connect", "get_conn"}, attempts[0].Phase)
	require.Equal(t, "dial", attempts[0].Kind)
}

func TestTraceMarksReusedConnectionAndIdleTime(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	client := NewHTTPClient(base)
	domain := strings.TrimPrefix(server.URL, "https://")
	var out map[string]any
	require.NoError(t, client.Get(context.Background(), domain, "/", nil, &out))
	time.Sleep(60 * time.Millisecond)
	trace := &Trace{}
	require.NoError(t, client.Get(WithTrace(context.Background(), trace), domain, "/", nil, &out))
	attempts := trace.Snapshot().Attempts
	require.Len(t, attempts, 1)
	require.True(t, attempts[0].Reused)
	require.GreaterOrEqual(t, attempts[0].IdleMs, int64(50))
	require.Equal(t, "done", attempts[0].Phase)
	require.Contains(t, AttemptsText(attempts), ":200:")
}

func TestErrorKindClassification(t *testing.T) {
	require.Equal(t, "status_429", errorKind(&StatusError{StatusCode: 429}, 429))
	require.Equal(t, "deadline", errorKind(fmt.Errorf("x: %w", context.DeadlineExceeded), 0))
	require.Equal(t, "canceled", errorKind(context.Canceled, 0))
	require.Equal(t, "reset", errorKind(fmt.Errorf("read: %w", syscall.ECONNRESET), 0))
	require.Equal(t, "eof", errorKind(fmt.Errorf("x: %w", io.EOF), 0))
	require.Equal(t, "decode", errorKind(errors.New("JSON 解析失败"), 200))
	require.Equal(t, "other", errorKind(errors.New("boom"), 0))
}

func TestTraceRecordsResponseHintsAndBodyOfNon200(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Amz-Cf-Pop", "NRT57-P1")
		w.Header().Set("X-Cache", "Miss from cloudfront")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("{\"msg\":\"queued\"}\n\x01"))
	}))
	defer server.Close()
	base := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	client := NewHTTPClient(base)
	trace := &Trace{}
	require.Error(t, client.Get(WithTrace(context.Background(), trace), strings.TrimPrefix(server.URL, "https://"), "/", nil, nil))

	attempts := trace.Snapshot().Attempts
	require.Len(t, attempts, 1)
	a := attempts[0]
	require.Equal(t, "status_202", a.Kind)
	require.Equal(t, "NRT57-P1", a.Pop)
	require.Equal(t, "Miss from cloudfront", a.Cache)
	require.Contains(t, a.BodyHint, "queued")
	require.NotContains(t, a.BodyHint, "\n", "不可打印字符被替换")
	require.False(t, a.StartedAt.IsZero())
	require.GreaterOrEqual(t, a.ConnectMs+a.TLSMs, int64(0))
	require.GreaterOrEqual(t, a.WroteMs, int64(0))
	require.GreaterOrEqual(t, a.TTFBMs, int64(0))
	text := AttemptsText(attempts)
	require.Contains(t, text, "status_202")
	require.Contains(t, text, "pop=NRT57-P1")
	require.Contains(t, text, "cache=Miss from cloudfront")
	require.Contains(t, text, "body=")
	require.Contains(t, text, "skew=")

	// 没收到首字节的失败：f=-1。
	block := make(chan struct{})
	slow := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-block }))
	defer slow.Close()
	defer close(block)
	short := NewHTTPClient(&http.Client{Timeout: 150 * time.Millisecond, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}})
	trace = &Trace{}
	require.Error(t, short.Get(WithTrace(context.Background(), trace), strings.TrimPrefix(slow.URL, "https://"), "/", nil, nil))
	require.Equal(t, int64(-1), trace.Snapshot().Attempts[0].TTFBMs)
	require.Contains(t, AttemptsText(trace.Snapshot().Attempts), "f=-1")
}
