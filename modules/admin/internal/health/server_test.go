package health

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandler_LivenessEndpoint_ShouldReturnOK(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(time.Now().UTC()).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	assert.Equal(t, 200, rec.Code)
}

func TestHandler_ReadinessEndpoint_ShouldReturnOK(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(time.Now().UTC()).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	assert.Equal(t, 200, rec.Code)
}

func TestHandler_MetricsEndpoint_ShouldExposePrometheusMetrics(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(time.Now().UTC()).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	require.Equal(t, 200, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "# HELP") || strings.Contains(rec.Body.String(), "# TYPE"))
}
