package health

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/observability"
	"github.com/mooyang-code/moox/packages/jetstream"
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

func TestSnapshotForRole_ViewRequiresBoundConsumer(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", "storage-view", "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRole("storage-view", metrics)

	metrics.SetConsumerBound(false)
	assert.False(t, state.Snapshot(context.Background()).Ready)
	metrics.SetConsumerBound(true)
	assert.True(t, state.Snapshot(context.Background()).Ready)
}

func TestSnapshotForRole_ViewBacklogDoesNotBlockReadiness(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageView, "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRoleWithOptions(InstanceStorageView, metrics, RoleOptions{OldestPendingThreshold: time.Minute})
	metrics.SetConsumerBound(true)

	delivery := &jetstream.Delivery{}
	metrics.ObservePendingDelivery(delivery, time.Now().Add(-2*time.Minute))
	rsp := state.Snapshot(context.Background())

	assert.True(t, rsp.Ready)
	assert.Equal(t, true, rsp.Details["consumer_draining"])
	metrics.CompletePendingDelivery(delivery, time.Now())
	assert.True(t, state.Snapshot(context.Background()).Ready)
}

func TestSnapshotForRole_NodeRejectsOldOutbox(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", "storage-node", "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRole("storage-node", metrics)
	metrics.SetOutboxPublisherReady(true)

	metrics.SetOutboxSnapshotAt(1, time.Now().Add(-6*time.Minute))
	rsp := state.Snapshot(context.Background())
	assert.False(t, rsp.Ready)
	assert.Equal(t, false, rsp.Details["outbox_draining"])
	metrics.SetOutboxSnapshotAt(0, time.Time{})
	assert.True(t, state.Snapshot(context.Background()).Ready)
}

func TestSnapshotForRole_NodeTreatsUnobservedOutboxAsInitializing(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageNode, "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRole(InstanceStorageNode, metrics)

	rsp := state.Snapshot(context.Background())
	assert.True(t, rsp.Ready)
	assert.Equal(t, true, rsp.Details["outbox_draining"])
	assert.Equal(t, true, rsp.Details["outbox_publisher_healthy"])
}

func TestSnapshotForRole_NodeRejectsUnobservedOutboxAfterInitializationDeadline(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageNode, "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRoleWithOptions(InstanceStorageNode, metrics, RoleOptions{
		InitializationThreshold: time.Millisecond,
	})
	time.Sleep(2 * time.Millisecond)

	rsp := state.Snapshot(context.Background())
	assert.False(t, rsp.Ready)
	assert.Equal(t, false, rsp.Details["outbox_initializing"])
}

func TestSnapshotForRoleWithOptionsUsesConfiguredThreshold(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageNode, "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRoleWithOptions(InstanceStorageNode, metrics, RoleOptions{OldestPendingThreshold: time.Second})
	metrics.SetOutboxSnapshotAt(1, time.Now().Add(-2*time.Second))
	assert.False(t, state.Snapshot(context.Background()).Ready)
}

func TestSnapshotForRole_NodeToleratesBriefPublisherDisconnect(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageNode, "", "")
	state.SetReady(true)
	state.SnapshotFunc = SnapshotForRoleWithOptions(InstanceStorageNode, metrics, RoleOptions{
		OldestPendingThreshold:        time.Minute,
		PublisherUnavailableThreshold: time.Minute,
	})
	metrics.SetOutboxSnapshotAt(0, time.Time{})
	metrics.SetOutboxPublisherReady(false)

	rsp := state.Snapshot(context.Background())
	assert.True(t, rsp.Ready)
	assert.Equal(t, false, rsp.Details["outbox_publisher_ready"])
	assert.Equal(t, true, rsp.Details["outbox_publisher_healthy"])
}

func TestSnapshotForRole_NodeRejectsLongPublisherDisconnect(t *testing.T) {
	metrics, err := observability.NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	state := New("storage", InstanceStorageNode, "", "")
	state.SnapshotFunc = SnapshotForRoleWithOptions(InstanceStorageNode, metrics, RoleOptions{
		OldestPendingThreshold:        time.Minute,
		PublisherUnavailableThreshold: 10 * time.Millisecond,
	})
	metrics.SetOutboxSnapshotAt(0, time.Time{})
	metrics.SetOutboxPublisherReady(false)
	time.Sleep(15 * time.Millisecond)

	rsp := state.Snapshot(context.Background())
	assert.False(t, rsp.Ready)
	assert.Equal(t, false, rsp.Details["outbox_publisher_healthy"])
}
