package health

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadinessFollowsState(t *testing.T) {
	state := New("callback-dispatcher", "test", "dev", "local")
	require.False(t, state.Ready())

	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	require.Equal(t, 503, rec.Code)

	state.SetReady(true)
	rec = httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	require.Equal(t, 200, rec.Code)

	rec = httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	require.Equal(t, 200, rec.Code)
}
