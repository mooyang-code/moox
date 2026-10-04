package health

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsEndpoint(t *testing.T) {
	s := &Server{}
	r := httptest.NewRecorder()
	s.metrics(r, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(r.Body.String(), "moox_eventbus_connections") {
		t.Fatalf("metrics missing connection gauge: %s", r.Body.String())
	}
}

func TestRegister_NilService_ShouldReturnError(t *testing.T) {
	s := &Server{}
	err := s.Register(nil)
	require.Error(t, err)
}

func TestRegister_NilServer_ShouldReturnError(t *testing.T) {
	var s *Server
	err := s.Register(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

func TestHandlerRoutesLivenessAndReadiness(t *testing.T) {
	s := &Server{}
	handler := s.Handler()

	for _, tc := range []struct {
		path string
		want int
	}{
		{path: "/healthz", want: http.StatusOK},
		{path: "/readyz", want: http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		assert.Equal(t, tc.want, rec.Code)
	}
}

func TestShutdownNilServer_ShouldNoop(t *testing.T) {
	var s *Server
	assert.NoError(t, s.Shutdown(nil))
}
