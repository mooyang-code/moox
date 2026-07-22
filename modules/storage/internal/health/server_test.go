package health

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestState_New_ShouldInitializeModuleMetadata(t *testing.T) {
	state := New("storage", "instance-1", "v1", "abc")
	require.NotNil(t, state)
	assert.Equal(t, "storage", state.Module)
	assert.False(t, state.Ready())
}

func TestHandler_ReadinessEndpoint_NotReady_ShouldReturn503(t *testing.T) {
	state := New("storage", "test", "dev", "local")
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	assert.Equal(t, 503, rec.Code)
}

func TestHandler_ReadinessEndpoint_Ready_ShouldReturn200(t *testing.T) {
	state := New("storage", "test", "dev", "local")
	state.SetReady(true)
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	assert.Equal(t, 200, rec.Code)
}

func TestHandler_MetricsEndpoint_ShouldExposePrometheusMetrics(t *testing.T) {
	state := New("storage", "test", "dev", "local")
	observability.DefaultViewMetrics.SetOutboxSnapshot(2, 3*time.Second)
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	require.Equal(t, 200, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "# HELP") || strings.Contains(rec.Body.String(), "# TYPE"))
	assert.Contains(t, rec.Body.String(), "moox_storage_outbox_pending_entries 2")
}
