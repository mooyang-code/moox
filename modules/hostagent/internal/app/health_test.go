package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestHealthHandler_ReadyAgent_ShouldReturnOK(t *testing.T) {
	a := testAgent(t)
	a.publisher = &fakeEventPublisher{ready: true}
	a.lastCollect = time.Now().UTC()
	handler := healthHandler(a)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestHealthHandler_RequiredCollectionFailureMakesAgentUnready(t *testing.T) {
	a := testAgent(t)
	a.publisher = &fakeEventPublisher{ready: true}
	a.collector = fakeSnapshotCollector{err: errors.New("required host collectors failed: cpu")}
	_, err := a.RunOnce(context.Background(), nil)
	require.Error(t, err)

	rr := httptest.NewRecorder()
	healthHandler(a).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestRegisterHealth_NilService_ShouldReturnError(t *testing.T) {
	err := RegisterHealth(nil, testAgent(t))
	require.Error(t, err)
}
