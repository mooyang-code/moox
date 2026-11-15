package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 健康端口只有 /healthz、/readyz、/metrics：生命周期、本地写入都改成了 RPC（批 4 T6），健康端口上不再有这些路径。
func TestHealthPortServesOnlyStandardRoutes(t *testing.T) {
	handler := Handler(New("m", "i", "v", "c"))
	for _, path := range []string{"/healthz", "/readyz", "/metrics"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusNotFound {
			t.Fatalf("标准健康路由 %s 丢失", path)
		}
	}
	for _, path := range []string{"/v1/lifecycle/status", "/v1/lifecycle/command", "/v1/series/write"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}")))
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s 不应再挂在健康端口上：%d", path, rec.Code)
		}
	}
}
