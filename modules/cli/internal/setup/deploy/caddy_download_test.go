package deploy

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// downloadServer 模拟只在第一次请求时中途断开（或卡住）的下载服务，之后按 Range 续传。
type downloadServer struct {
	payload []byte
	mode    string // "abort"：第一次发送一部分后断开；"stall"：第一次发送一部分后卡住；"range-invalid"：对 Range 返回 416

	mu       sync.Mutex
	ranges   []string
	requests int
	release  chan struct{}
}

func (s *downloadServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.requests++
	first := s.requests == 1
	s.ranges = append(s.ranges, r.Header.Get("Range"))
	s.mu.Unlock()

	var start int
	if header := r.Header.Get("Range"); header != "" {
		if s.mode == "range-invalid" {
			http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
			return
		}
		_, _ = fmt.Sscanf(header, "bytes=%d-", &start)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, len(s.payload)-1, len(s.payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(s.payload)-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(s.payload[start:])
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(s.payload)))
	w.WriteHeader(http.StatusOK)
	if !first || s.mode == "" {
		_, _ = w.Write(s.payload)
		return
	}
	half := len(s.payload) / 3
	_, _ = w.Write(s.payload[:half])
	w.(http.Flusher).Flush()
	switch s.mode {
	case "abort":
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	case "stall":
		select {
		case <-s.release:
		case <-r.Context().Done():
		}
	}
}

func withFastDownload(t *testing.T) {
	t.Helper()
	attempts, idle := downloadAttempts, downloadIdleTimeout
	downloadAttempts, downloadIdleTimeout = 4, 300*time.Millisecond
	t.Cleanup(func() { downloadAttempts, downloadIdleTimeout = attempts, idle })
}

func TestDownloadResumesAfterAbortedConnection(t *testing.T) {
	withFastDownload(t)
	handler := &downloadServer{payload: bytes.Repeat([]byte("moox-caddy-archive-"), 20000), mode: "abort"}
	server := httptest.NewServer(handler)
	defer server.Close()

	target := filepath.Join(t.TempDir(), "caddy.tar.gz")
	require.NoError(t, download(context.Background(), server.URL, target))

	raw, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, handler.payload, raw, "断点续传之后内容必须完整一致")
	require.Len(t, handler.ranges, 2)
	assert.Empty(t, handler.ranges[0], "第一次从头下载")
	assert.True(t, strings.HasPrefix(handler.ranges[1], "bytes="), "第二次用 Range 续传：%q", handler.ranges[1])
	_, err = os.Stat(target + ".download")
	assert.True(t, os.IsNotExist(err), "下载完成后不能留下 .download 文件")
}

func TestDownloadGivesUpOnStalledConnectionAndResumes(t *testing.T) {
	withFastDownload(t)
	handler := &downloadServer{payload: bytes.Repeat([]byte("0123456789"), 30000), mode: "stall", release: make(chan struct{})}
	server := httptest.NewServer(handler)
	defer func() {
		close(handler.release)
		server.Close()
	}()

	target := filepath.Join(t.TempDir(), "caddy.tar.gz")
	started := time.Now()
	require.NoError(t, download(context.Background(), server.URL, target))
	assert.Less(t, time.Since(started), 10*time.Second, "空闲超时应该很快触发，而不是等总时限")

	raw, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, handler.payload, raw)
	require.GreaterOrEqual(t, len(handler.ranges), 2)
	assert.NotEmpty(t, handler.ranges[1], "卡住之后从已收到的位置续传")
}

func TestDownloadDiscardsPartialWhenRangeIsRejected(t *testing.T) {
	withFastDownload(t)
	handler := &downloadServer{payload: []byte("complete-archive-content"), mode: "range-invalid"}
	server := httptest.NewServer(handler)
	defer server.Close()

	target := filepath.Join(t.TempDir(), "caddy.tar.gz")
	// 残留的未完成文件和服务端对不上：第一次得到 416，丢弃后第二次从头下载。
	require.NoError(t, os.WriteFile(target+".download", []byte("stale-partial"), 0o644))
	require.NoError(t, download(context.Background(), server.URL, target))

	raw, err := os.ReadFile(target)
	require.NoError(t, err)
	assert.Equal(t, handler.payload, raw)
}

func TestDownloadFailsAfterAttemptsAreExhausted(t *testing.T) {
	withFastDownload(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := download(context.Background(), server.URL, filepath.Join(t.TempDir(), "caddy.tar.gz"))
	require.ErrorContains(t, err, "HTTP 503")
}

func TestContentRangeStart(t *testing.T) {
	assert.Equal(t, int64(100), contentRangeStart("bytes 100-199/200"))
	assert.Equal(t, int64(-1), contentRangeStart(""))
	assert.Equal(t, int64(-1), contentRangeStart("bytes */200"))
}
