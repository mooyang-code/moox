package observability

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/events/eventpb"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestViewMetricsRecordFixedOutcomeLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)

	metrics.IncDeriveInFlight()
	metrics.ObserveDerive("time_series", "error")
	metrics.ObserveBatch("duckdb", "error", 25*time.Millisecond)
	metrics.ObserveDelivery("nak", "success")
	metrics.IncRedelivery()
	metrics.DecDeriveInFlight()

	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.deriveTotal.WithLabelValues("time_series", "error")))
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.deliveryTotal.WithLabelValues("nak", "success")))
	assert.Equal(t, float64(1), testutil.ToFloat64(metrics.redeliveryTotal))
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.deriveInFlight))
}

func TestViewMetricsExposeAggregateRuntimeMetricsWithFixedLabels(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)
	_, err = NewViewMetrics(registry)
	require.NoError(t, err, "re-registering the same metric names must reuse collectors")

	raw, _ := proto.Marshal(&eventpb.EventMessage{OccurredAt: timestamppb.New(time.Now().Add(-2 * time.Second))})
	delivery := &jetstream.Delivery{RawData: raw}
	metrics.AddConsumerLagMessages(1)
	metrics.SetConsumerBound(true)
	metrics.ObservePendingDelivery(delivery, time.Now())
	metrics.ObserveLaneSubmit()
	metrics.IncLaneActive()
	metrics.ObserveDeliveryDuration(25 * time.Millisecond)
	metrics.IncAckError()
	metrics.IncInProgressError()
	metrics.IncOutboxPublishError()
	metrics.IncOutboxDuplicatePublish()
	metrics.SetOutboxSnapshotAt(3, time.Now().Add(-4*time.Second))
	metrics.ObserveDelivery("term", "success")
	metrics.ObservePeriodWaiting("prices-view", "1m", 1)
	metrics.ObserveReadyPublishRetry("prices-view", "source_period_ready")

	snapshot := metrics.Snapshot()
	assert.Equal(t, int64(1), snapshot.ConsumerLagMessages)
	assert.True(t, snapshot.ConsumerBound)
	assert.Equal(t, int64(1), snapshot.LaneActive)
	assert.Equal(t, int64(3), snapshot.OutboxPendingEntries)
	assert.GreaterOrEqual(t, snapshot.OutboxOldestAge, 4*time.Second)
	assert.Less(t, snapshot.OutboxOldestAge, 5*time.Second)
	assert.Greater(t, snapshot.OldestPendingAge, time.Second)
	assert.Equal(t, int64(1), snapshot.AckErrorsTotal)
	assert.Equal(t, int64(1), snapshot.InProgressErrorsTotal)
	assert.Equal(t, int64(1), snapshot.OutboxPublishErrorsTotal)
	assert.Equal(t, int64(1), snapshot.OutboxDuplicatePublishTotal)

	metrics.DecLaneActive()
	metrics.CompletePendingDelivery(delivery, time.Now())
	metrics.AddConsumerLagMessages(-1)
	metrics.SetConsumerBound(false)
	assert.Equal(t, int64(0), metrics.Snapshot().ConsumerLagMessages)
	assert.False(t, metrics.Snapshot().ConsumerBound)
	assert.Equal(t, int64(0), metrics.Snapshot().LaneActive)
	assert.Equal(t, time.Duration(0), metrics.Snapshot().OldestPendingAge)

	families, err := registry.Gather()
	require.NoError(t, err)
	wantNames := map[string]bool{
		"moox_storage_view_consumer_lag_messages":            true,
		"moox_storage_view_oldest_pending_event_age_seconds": true,
		"moox_storage_view_delivery_duration_seconds":        true,
		"moox_storage_view_ack_errors_total":                 true,
		"moox_storage_view_in_progress_errors_total":         true,
		"moox_storage_view_lane_active":                      true,
		"moox_storage_outbox_pending_entries":                true,
		"moox_storage_outbox_oldest_age_seconds":             true,
		"moox_storage_outbox_publish_errors_total":           true,
		"moox_storage_outbox_duplicate_publish_total":        true,
		"moox_storage_view_period_waiting_datasets":          true,
		"moox_storage_view_ready_publish_retry_total":        true,
	}
	for _, family := range families {
		delete(wantNames, family.GetName())
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				assert.NotContains(t, []string{"subject", "symbol", "message_id"}, label.GetName())
			}
		}
	}
	assert.Empty(t, wantNames)
}

func TestViewMetricsAgesOutboxFromOldestEventTimestamp(t *testing.T) {
	metrics, err := NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	metrics.SetOutboxSnapshotAt(1, time.Now().UTC().Add(-6*time.Minute))
	snapshot := metrics.Snapshot()
	assert.True(t, snapshot.OutboxObserved)
	assert.GreaterOrEqual(t, snapshot.OutboxOldestAge, 6*time.Minute)
}

func TestOldestPendingEventAgeGrowsOnScrapeWithoutNewEvents(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)
	raw, _ := proto.Marshal(&eventpb.EventMessage{OccurredAt: timestamppb.New(time.Now().Add(-50 * time.Millisecond))})
	delivery := &jetstream.Delivery{RawData: raw}
	metrics.ObservePendingDelivery(delivery, time.Now())
	first := gatherGauge(t, registry, "moox_storage_view_oldest_pending_event_age_seconds")
	time.Sleep(25 * time.Millisecond)
	second := gatherGauge(t, registry, "moox_storage_view_oldest_pending_event_age_seconds")
	assert.Greater(t, second, first)
}

func gatherGauge(t *testing.T, registry *prometheus.Registry, name string) float64 {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == name && len(family.GetMetric()) > 0 {
			return family.GetMetric()[0].GetGauge().GetValue()
		}
	}
	t.Fatalf("metric %q not found", name)
	return 0
}
