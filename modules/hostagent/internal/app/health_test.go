package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/hostagent/internal/eventpublisher"
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

func TestHealthHandler_BeforeFirstCollectionIsUnready(t *testing.T) {
	a := testAgent(t)
	rr := httptest.NewRecorder()
	healthHandler(a).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

// EventBus 不可达（连不上、发布失败）时，只要采集正常就必须就绪：
// 否则 EventBus 部署之前、或 EventBus 所在主机宕机时，部署流程会反复杀掉 HostAgent。
func TestHealthHandler_EventBusUnavailableDoesNotMakeAgentUnready(t *testing.T) {
	a := testAgent(t)
	a.collector = fakeSnapshotCollector{snapshot: testSnapshot()}
	a.newPublisher = func(context.Context) (eventpublisher.Publisher, error) {
		return nil, errors.New("eventbus unreachable")
	}
	_, err := a.RunOnce(context.Background(), nil)
	require.Error(t, err)

	status, statusErr := a.GetStatus(context.Background(), nil)
	require.NoError(t, statusErr)
	assert.False(t, status.GetEventbusConnected())
	assert.NotEmpty(t, status.GetLastError(), "发布错误仍然记在状态详情里")

	rr := httptest.NewRecorder()
	healthHandler(a).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestHealthHandler_PublishFailureDoesNotMakeAgentUnready(t *testing.T) {
	a := testAgent(t)
	a.collector = fakeSnapshotCollector{snapshot: testSnapshot()}
	a.publisher = &fakeEventPublisher{ready: true, err: errors.New("publish failed")}
	_, err := a.RunOnce(context.Background(), nil)
	require.Error(t, err)

	rr := httptest.NewRecorder()
	healthHandler(a).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestHealthHandler_CollectionRecoveryClearsUnready(t *testing.T) {
	a := testAgent(t)
	a.publisher = &fakeEventPublisher{ready: true}
	a.collector = fakeSnapshotCollector{err: errors.New("cpu")}
	_, err := a.RunOnce(context.Background(), nil)
	require.Error(t, err)
	a.collector = fakeSnapshotCollector{snapshot: testSnapshot()}
	_, err = a.RunOnce(context.Background(), nil)
	require.NoError(t, err)

	rr := httptest.NewRecorder()
	healthHandler(a).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	assert.Equal(t, http.StatusOK, rr.Code)
}

func TestRegisterHealth_NilService_ShouldReturnError(t *testing.T) {
	err := RegisterHealth(nil, testAgent(t))
	require.Error(t, err)
}
