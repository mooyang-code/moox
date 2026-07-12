package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterAndLookupRawHandler_ShouldRoundTrip(t *testing.T) {
	h := RawHandler(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})
	RegisterRawHandler("svc", "Method", h)
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers, "svc")
		rawHandlersMutex.Unlock()
	})

	got, ok := LookupRawHandler("svc", "Method")
	require.True(t, ok)
	require.NotNil(t, got)
}

func TestRawAndServe_Miss_ShouldReturnFalse(t *testing.T) {
	assert.False(t, rawAndServe(context.Background(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil), "missing", "method", nil))
}

func TestRawAndServe_Hit_ShouldInvokeHandler(t *testing.T) {
	RegisterRawHandler("raw", "Echo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("echo"))
	})
	t.Cleanup(func() {
		rawHandlersMutex.Lock()
		delete(rawHandlers, "raw")
		rawHandlersMutex.Unlock()
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	ok := rawAndServe(context.Background(), rr, req, "raw", "Echo", map[string]string{
		"space_id": "space-1", "trace_id": "trace-1",
	})
	assert.True(t, ok)
	assert.Equal(t, "echo", rr.Body.String())
}
