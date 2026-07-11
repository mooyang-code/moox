package health

import (
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerReturnsNotReadyUntilStateReady(t *testing.T) {
	state := New("archive", "test", "dev", "local")
	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, req)
	if rec.Code != 503 {
		t.Fatalf("status=%d", rec.Code)
	}
	state.ReadyFlag.Store(true)
	rec = httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("ready status=%d", rec.Code)
	}
}
