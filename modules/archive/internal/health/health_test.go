package health

import (
	"net/http/httptest"
	"testing"
)

func TestReadinessFollowsTheState(t *testing.T) {
	state := New("archive", "test", "dev", "local")
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 503 {
		t.Fatalf("未就绪应返回 503: %d", rec.Code)
	}
	state.SetReady(true)
	rec = httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	if rec.Code != 200 {
		t.Fatalf("就绪应返回 200: %d", rec.Code)
	}
}
