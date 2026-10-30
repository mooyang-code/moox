package observability

import (
	"fmt"
	"strings"
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

func TestViewMetricsViewFileOverLimit(t *testing.T) {
	metrics, err := NewViewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	metrics.SetViewFileOverLimitCount(2)
	assert.Equal(t, float64(2), testutil.ToFloat64(metrics.viewFileOverLimit))
	metrics.SetViewFileOverLimitCount(-1)
	assert.Equal(t, float64(0), testutil.ToFloat64(metrics.viewFileOverLimit))
}

func TestViewMetricsSummarizeSubjectsPerView(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)
	latest := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	observe := func(subject string, at time.Time) {
		require.NoError(t, metrics.ObserveViewDatasetOutput(ViewDatasetObservation{
			SpaceID: "crypto", ViewID: "prices-view", DatasetID: "market-prices", SubjectID: subject, Frequency: "1m", DataTime: at,
		}))
	}
	observe("BTC-USDT", latest)
	// One bar plus the lag tolerance behind is still current.
	observe("ETH-USDT", latest.Add(-time.Minute-viewSubjectLagTolerance))
	for i := 0; i < ViewLaggingSubjectsReported+5; i++ {
		observe(fmt.Sprintf("L%02d-USDT", i), latest.Add(-time.Hour+time.Duration(i)*time.Second))
	}
	scope := map[string]string{"space_id": "crypto", "view_id": "prices-view", "dataset_id": "market-prices", "freq": "1m"}
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_latest_data_time_seconds", scope, float64(latest.Unix()))
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_tracked_since_data_time_seconds", scope, float64(latest.Unix()))
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_subjects", scope, float64(2+ViewLaggingSubjectsReported+5))
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_lagging_subjects", scope, float64(ViewLaggingSubjectsReported+5))
	oldest := map[string]string{"space_id": "crypto", "view_id": "prices-view", "dataset_id": "market-prices", "freq": "1m", "subject_id": "L00-USDT"}
	assertViewDatasetMetric(t, registry, "moox_storage_view_output_lagging_subject_data_time_seconds", oldest, float64(latest.Add(-time.Hour).Unix()))

	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == "moox_storage_view_output_lagging_subject_data_time_seconds" {
			require.Len(t, family.GetMetric(), ViewLaggingSubjectsReported, "only the most lagging subjects are named")
		}
	}
	assertNoMetricFamily(t, registry, "moox_storage_view_dataset_output_last_data_time_seconds")
}

func assertNoMetricFamily(t *testing.T, registry *prometheus.Registry, name string) {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() == name {
			t.Fatalf("metric family %q should not be exposed", name)
		}
	}
}

func assertViewDatasetMetric(t *testing.T, registry *prometheus.Registry, name string, wantLabels map[string]string, wantValue float64) {
	t.Helper()
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := make(map[string]string, len(metric.GetLabel()))
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if assert.ObjectsAreEqual(labels, wantLabels) {
				assert.Equal(t, wantValue, metric.GetGauge().GetValue())
				return
			}
		}
	}
	t.Fatalf("metric %q labels=%v not found", name, wantLabels)
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
	arrived := time.Date(2026, 8, 20, 14, 1, 3, 0, time.UTC)
	metrics.now = func() time.Time { return arrived }
	metrics.ObserveViewOutputWatermark("crypto", "view_factor_binance_kline_1m", "dataset_factor_binance_kline_1m", "1m", time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC))
	metrics.now = func() time.Time { return arrived.Add(time.Minute) }
	metrics.ObserveViewOutputWatermark("crypto", "view_factor_binance_kline_1m", "dataset_factor_binance_kline_1m", "1m", time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC))
	assert.Equal(t, float64(time.Date(2026, 8, 20, 14, 0, 0, 0, time.UTC).Unix()), testutil.ToFloat64(metrics.viewOutputWatermark.WithLabelValues("crypto", "view_factor_binance_kline_1m", "dataset_factor_binance_kline_1m", "1m")))
	// 到达时间只在水位前进时记录：较旧的水位不改动它。
	assert.Equal(t, float64(arrived.Unix()), testutil.ToFloat64(metrics.viewOutputArrival.WithLabelValues("crypto", "view_factor_binance_kline_1m", "dataset_factor_binance_kline_1m", "1m")))
	metrics.ObserveReadyPublishRetry("prices-view", "source_period_ready")
	metrics.ObserveRestore(true, 250*time.Millisecond)

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
	assert.True(t, snapshot.RestoreReady)
	assert.Equal(t, 250*time.Millisecond, snapshot.RestoreDuration)

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
		"moox_storage_view_consumer_lag_messages":              true,
		"moox_storage_view_oldest_pending_event_age_seconds":   true,
		"moox_storage_view_delivery_duration_seconds":          true,
		"moox_storage_view_ack_errors_total":                   true,
		"moox_storage_view_in_progress_errors_total":           true,
		"moox_storage_view_lane_active":                        true,
		"moox_storage_outbox_pending_entries":                  true,
		"moox_storage_outbox_oldest_age_seconds":               true,
		"moox_storage_outbox_publish_errors_total":             true,
		"moox_storage_outbox_duplicate_publish_total":          true,
		"moox_storage_view_period_waiting_datasets":            true,
		"moox_storage_view_output_watermark_timestamp_seconds": true,
		"moox_storage_view_ready_publish_retry_total":          true,
		"moox_storage_view_restore_duration_seconds":           true,
		"moox_storage_view_restore_ready":                      true,
		"moox_storage_view_restore_failures_total":             true,
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

func TestViewMetricsTracksOutboxPublisherLifecycle(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)

	metrics.SetOutboxPublisherReady(false)
	first := metrics.Snapshot()
	assert.True(t, first.OutboxPublisherObserved)
	assert.False(t, first.OutboxPublisherReady)
	assert.GreaterOrEqual(t, first.OutboxPublisherUnavailableAge, time.Duration(0))

	metrics.ObserveOutboxReconnect(false)
	assert.Equal(t, int64(1), metrics.Snapshot().OutboxReconnectFailures)

	metrics.ObserveOutboxReconnect(true)
	metrics.ObserveOutboxPublishSuccess()
	ready := metrics.Snapshot()
	assert.True(t, ready.OutboxPublisherReady)
	assert.Equal(t, "success", ready.OutboxReconnectStatus)
	assert.Equal(t, int64(1), ready.OutboxReconnectSuccesses)
	assert.False(t, ready.OutboxLastPublishSuccess.IsZero())
	assert.False(t, ready.OutboxLastReconnectAt.IsZero())

	expectedNames := map[string]bool{
		"moox_storage_outbox_publisher_ready":                        true,
		"moox_storage_outbox_publisher_unavailable_age_seconds":      true,
		"moox_storage_outbox_last_publish_success_timestamp_seconds": true,
		"moox_storage_outbox_last_reconnect_timestamp_seconds":       true,
		"moox_storage_outbox_reconnect_attempts_total":               true,
	}
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		delete(expectedNames, family.GetName())
	}
	assert.Empty(t, expectedNames)
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

// 删除或停用的 View 不再导出输出水位等序列：RetainViews 只保留活动 View，已被删除的 View 的序列消失，
// 之后同一 View 重新出现并产出时序列恢复（水位记录也已清掉，不会被旧的更大水位挡住）。
func TestRetainViewsDropsSeriesOfInactiveViews(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewViewMetrics(registry)
	require.NoError(t, err)
	at := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)
	for _, view := range []string{"view_keep", "view_gone"} {
		metrics.ObserveViewOutputWatermark("crypto", view, "dataset_"+view, "1m", at)
		require.NoError(t, metrics.ObserveViewDatasetOutput(ViewDatasetObservation{
			SpaceID: "crypto", ViewID: view, DatasetID: "dataset_" + view, SubjectID: "BTC", Frequency: "1m", SeriesTag: "t", DataTime: at}))
	}
	viewsReported := func() map[string]bool {
		families, err := registry.Gather()
		require.NoError(t, err)
		seen := map[string]bool{}
		for _, family := range families {
			if !strings.HasPrefix(family.GetName(), "moox_storage_view_output_") {
				continue
			}
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "view_id" {
						seen[label.GetValue()] = true
					}
				}
			}
		}
		return seen
	}
	require.Equal(t, map[string]bool{"view_keep": true, "view_gone": true}, viewsReported())

	metrics.RetainViews(map[ViewKey]struct{}{{SpaceID: "crypto", ViewID: "view_keep"}: {}})
	require.Equal(t, map[string]bool{"view_keep": true}, viewsReported())

	// 重新出现的 View 照常产出（旧水位记录已清除）。
	metrics.ObserveViewOutputWatermark("crypto", "view_gone", "dataset_view_gone", "1m", at.Add(-time.Hour))
	require.True(t, viewsReported()["view_gone"])
}
