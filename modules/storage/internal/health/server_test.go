package health

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/prometheus/client_golang/prometheus"
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
	observability.DefaultViewMetrics.SetOutboxSnapshotAt(2, time.Now().Add(-3*time.Second))
	rec := httptest.NewRecorder()
	Handler(state).ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	require.Equal(t, 200, rec.Code)
	assert.True(t, strings.Contains(rec.Body.String(), "# HELP") || strings.Contains(rec.Body.String(), "# TYPE"))
	assert.Contains(t, rec.Body.String(), "moox_storage_outbox_pending_entries 2")
}

// View 恢复完活动索引并开始跟随变更日志才就绪，两者缺一不可。
func TestSnapshotForRole_ViewReadyAfterRestoreAndFollowing(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageView, "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRole(InstanceStorageView, metrics)

	assert.False(t, state.Snapshot(context.Background()).Ready)
	metrics.ObserveRestore(true, time.Second)
	assert.False(t, state.Snapshot(context.Background()).Ready, "恢复完但还没开始跟随")
	metrics.SetFollowing(true)
	assert.True(t, state.Snapshot(context.Background()).Ready)
	metrics.SetFollowing(false)
	assert.False(t, state.Snapshot(context.Background()).Ready)
}

// 主服务的 Outbox 积压与 EventBus 断连只上报、不影响就绪：EventBus 故障时 primary 照常服务。
func TestSnapshotForRole_PrimaryStaysReadyWithOutboxBacklog(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", "storage-primary", "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRole("storage-primary", metrics)
	metrics.SetOutboxPublisherReady(false)
	metrics.SetOutboxSnapshotAt(1, time.Now().Add(-6*time.Minute))

	rsp := state.Snapshot(context.Background())
	assert.True(t, rsp.Ready)
	assert.Equal(t, false, rsp.Details["outbox_publisher_ready"])
	assert.EqualValues(t, 1, rsp.Details["outbox_pending_entries"])
}
