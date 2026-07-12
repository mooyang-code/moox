package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerSeparatesLivenessAndReadiness(t *testing.T) {
	handler := healthHandler(nil)
	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/healthz", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
	} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rr.Code != tc.want {
			t.Fatalf("%s status = %d, want %d", tc.path, rr.Code, tc.want)
		}
	}
}
