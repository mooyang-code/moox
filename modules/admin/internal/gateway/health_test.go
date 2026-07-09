package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGatewayHealthzRoutesUseSharedPayload(t *testing.T) {
	router := NewHTTPRouter(NewGatewayHandle()).buildRouter()

	for _, path := range []string{"/api/admin/health", "/healthz"} {
		t.Run(path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
			}
			if got := rr.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q, want application/json", got)
			}

			var rsp struct {
				Module string `json:"module"`
				Ready  bool   `json:"ready"`
				Status string `json:"status"`
				Time   string `json:"time"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &rsp); err != nil {
				t.Fatalf("decode health response: %v", err)
			}
			if rsp.Module != "admin" {
				t.Fatalf("module = %q, want admin", rsp.Module)
			}
			if !rsp.Ready {
				t.Fatal("ready = false, want true")
			}
			if rsp.Status != "ok" {
				t.Fatalf("status = %q, want ok", rsp.Status)
			}
			if rsp.Time == "" {
				t.Fatal("time is empty")
			}
		})
	}
}
