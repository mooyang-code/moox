package metrics

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestMetricMessageStoreNilGuards(t *testing.T) {
	var store *MetricMessageStore
	_, err := store.IsDuplicate(context.Background(), "msg")
	require.Error(t, err)
	_, err = store.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: "m"}, &metricspb.MetricReport{}, nil)
	require.Error(t, err)

	empty := NewMetricMessageStore(nil)
	require.NotNil(t, empty)
	_, err = empty.IsDuplicate(context.Background(), "")
	require.Error(t, err)
	_, err = empty.IsDuplicate(context.Background(), "id")
	require.Error(t, err)
	_, err = empty.CommitIngest(context.Background(), nil, nil, nil)
	require.Error(t, err)
	_, err = empty.CommitIngest(context.Background(), &eventpb.EventMessage{}, &metricspb.MetricReport{}, nil)
	require.Error(t, err)
	assert.Equal(t, empty.DedupeRetention.Hours(), float64(7*24))
}

func TestMonotonicMetricRecognizesCanonicalModuleNames(t *testing.T) {
	for _, name := range []string{
		"moox_factor_last_success_timestamp_seconds",
		"moox_monitor_business_watermark_timestamp_seconds",
		"moox_strategy_input_watermark_timestamp_seconds",
		"moox_trade_metrics_errors_total",
		"moox_archive_metrics_last_error_timestamp_seconds",
		ViewDatasetOutputLastDataTimeMetric,
	} {
		require.True(t, monotonicMetric(name), name)
	}
	require.False(t, monotonicMetric("moox_factor_runs_total"))
}

func TestMetricMessageStoreViewDatasetTimestampsIgnoreOutOfOrderSnapshot(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	r := metricMessageStoreForTest(t, mgr)
	newer := time.Unix(200, 0).UTC()
	report := &metricspb.MetricReport{ServiceName: "storage", InstanceId: "storage@node-a", BootId: "boot-a"}
	for index, name := range []string{ViewDatasetOutputLastDataTimeMetric} {
		seriesID := fmt.Sprintf("kline-series-%d", index)
		_, err := r.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: fmt.Sprintf("new-%d", index)}, report, []Sample{{
			SeriesID: seriesID, ServiceName: report.ServiceName, InstanceID: report.InstanceId, MetricName: name,
			MetricType: "gauge", Value: 200, ObservedAt: newer, MessageID: fmt.Sprintf("new-%d", index),
		}})
		require.NoError(t, err)
		_, err = r.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: fmt.Sprintf("old-%d", index)}, report, []Sample{{
			SeriesID: seriesID, ServiceName: report.ServiceName, InstanceID: report.InstanceId, MetricName: name,
			MetricType: "gauge", Value: 100, ObservedAt: newer.Add(time.Second), MessageID: fmt.Sprintf("old-%d", index),
		}})
		require.NoError(t, err)
		latest, err := r.GetLatest(context.Background(), seriesID)
		require.NoError(t, err)
		require.Equal(t, float64(200), latest.Value, name)
	}
}

func TestMetricMessageStoreListLatestByMetricNamesIsBoundedAndStable(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	_, err = store.WithDatabase(mgr, func(db *gorm.DB) error {
		rows := make([]MetricLatest, 0, 5003)
		for index := 0; index < 5000; index++ {
			rows = append(rows, MetricLatest{
				SeriesID: fmt.Sprintf("view-%05d", index), MetricName: ViewDatasetOutputLastDataTimeMetric,
				LabelsJSON: fmt.Sprintf(`{"space_id":"crypto","view_id":"view","dataset_id":"dataset","freq":"1m","series_tag":"default","subject_id":"%05d"}`, index),
				Value:      float64(index + 1), ObservedAt: time.Unix(int64(index+1), 0).UTC(),
			})
		}
		rows = append(rows,
			MetricLatest{SeriesID: "unrelated", MetricName: "moox_storage_dataset_output_watermark_timestamp_seconds", LabelsJSON: `{}`, Value: 1},
		)
		return db.CreateInBatches(&rows, 500).Error
	})
	require.NoError(t, err)
	r := metricMessageStoreForTest(t, mgr)
	rows, err := r.ListLatestByMetricNames(context.Background(), []string{
		ViewDatasetOutputLastDataTimeMetric,
	}, 0)
	require.NoError(t, err)
	require.Len(t, rows, 5000)
	for index := 1; index < len(rows); index++ {
		previous := rows[index-1]
		current := rows[index]
		require.LessOrEqual(t, previous.MetricName, current.MetricName)
		if previous.MetricName == current.MetricName {
			require.LessOrEqual(t, previous.LabelsJSON, current.LabelsJSON)
			if previous.LabelsJSON == current.LabelsJSON {
				require.Less(t, previous.SeriesID, current.SeriesID)
			}
		}
	}
	_, err = r.ListLatestByMetricNames(context.Background(), []string{"not_a_view_dataset_metric"}, 1)
	require.Error(t, err)
	_, err = r.ListLatestByMetricNames(context.Background(), []string{ViewDatasetOutputLastDataTimeMetric}, 500001)
	require.Error(t, err)

	query := NewQueryService(r, nil)
	rows, err = query.ListLatestByMetricNames(context.Background(), []string{ViewDatasetOutputLastDataTimeMetric}, 1)
	require.Error(t, err)
}

func TestMetricMessageStoreListLatestByViewScopesFiltersBeforeRead(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	_, err = store.WithDatabase(mgr, func(db *gorm.DB) error {
		return db.Create([]MetricLatest{
			{SeriesID: "configured", MetricName: ViewDatasetOutputLastDataTimeMetric, LabelsJSON: `{"space_id":"crypto","view_id":"kline","dataset_id":"binance","subject_id":"BTC","freq":"1m","series_tag":"default"}`, Value: 10, ObservedAt: time.Unix(10, 0).UTC()},
			{SeriesID: "unconfigured", MetricName: ViewDatasetOutputLastDataTimeMetric, LabelsJSON: `{"space_id":"mooxsys","view_id":"service","dataset_id":"metrics","subject_id":"cpu","freq":"30s","series_tag":"default"}`, Value: 10, ObservedAt: time.Unix(10, 0).UTC()},
		}).Error
	})
	require.NoError(t, err)
	r := metricMessageStoreForTest(t, mgr)
	rows, err := r.ListLatestByViewScopes(context.Background(), ViewDatasetOutputLastDataTimeMetric, []ViewMetricScope{{
		SpaceID: "crypto", ViewID: "kline", DatasetID: "binance", Frequency: "1m",
	}}, 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "configured", rows[0].SeriesID)
}

func metricMessageStoreForTest(t *testing.T, db *store.Store) *MetricMessageStore {
	t.Helper()
	result, err := store.WithDatabase(db, NewMetricMessageStore)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
