package httpclient

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
