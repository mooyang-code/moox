package health

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 额外路由要能接受 POST（本地写入、生命周期命令、回放）：healthz.Mux 的 exact 路由只接受 GET，所以走前缀路由。
func TestExtraRoutesAcceptPost(t *testing.T) {
	state := New("m", "i", "v", "c")
	called := ""
	handler := Handler(state, Route{Path: "/v1/x/command", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = r.Method
		w.WriteHeader(http.StatusNoContent)
	})})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/x/command", strings.NewReader("{}")))
	if rec.Code != http.StatusNoContent || called != http.MethodPost {
		t.Fatalf("POST 没有到达额外路由：%d %q", rec.Code, called)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code == http.StatusNotFound {
		t.Fatal("标准健康路由丢失")
	}
}
