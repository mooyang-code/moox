package observability

import (
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestIsKlineDatasetID(t *testing.T) {
	if !IsKlineDatasetID("dataset_stockcn_equity_kline") || !IsKlineDatasetID("market_kline") {
		t.Fatal("kline datasets must be recognized")
	}
	if IsKlineDatasetID("dataset_mooxsys_service_metrics") {
		t.Fatal("service metrics must not be recognized as kline")
	}
}

func TestKlineMetricsExposePrimaryAndViewContracts(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewKlineMetrics(registry)
	require.NoError(t, err)
	_, err = NewKlineMetrics(registry)
	require.NoError(t, err, "re-registering the contract must reuse collectors")

	dataTime := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	commitTime := dataTime.Add(2 * time.Second)
	require.NoError(t, metrics.ObservePrimary(KlineObservation{
		SpaceID: " crypto ", DatasetID: " dataset_kline ", SubjectID: " BTC-USDT ", Frequency: " 1m ",
		DataTime: dataTime, CommittedAt: commitTime,
	}))
	require.NoError(t, metrics.ObserveView(KlineObservation{
		SpaceID: "crypto", ViewID: "view_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		DataTime: dataTime, CommittedAt: commitTime, SeriesTag: "venue:binance",
	}))

	assertKlineMetricContract(t, registry, "moox_storage_kline_last_data_time_seconds", map[string]string{
		"space_id": "crypto", "dataset_id": "dataset_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "default",
	}, float64(dataTime.Unix()))
	assertKlineMetricContract(t, registry, "moox_storage_kline_last_commit_timestamp_seconds", map[string]string{
		"space_id": "crypto", "dataset_id": "dataset_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "default",
	}, float64(commitTime.Unix()))
	assertKlineMetricContract(t, registry, "moox_storage_view_kline_last_data_time_seconds", map[string]string{
		"space_id": "crypto", "view_id": "view_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "venue:binance",
	}, float64(dataTime.Unix()))
	assertKlineMetricContract(t, registry, "moox_storage_view_kline_last_commit_timestamp_seconds", map[string]string{
		"space_id": "crypto", "view_id": "view_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "venue:binance",
	}, float64(commitTime.Unix()))
}

func TestKlineMetricsRejectInvalidObservations(t *testing.T) {
	metrics, err := NewKlineMetrics(prometheus.NewRegistry())
	require.NoError(t, err)
	valid := KlineObservation{
		SpaceID: "crypto", DatasetID: "dataset_kline", SubjectID: "BTC-USDT", Frequency: "1m",
		DataTime: time.Unix(100, 0).UTC(), CommittedAt: time.Unix(101, 0).UTC(),
	}

	tests := []struct {
		name   string
		mutate func(*KlineObservation)
	}{
		{name: "space", mutate: func(o *KlineObservation) { o.SpaceID = " " }},
		{name: "dataset", mutate: func(o *KlineObservation) { o.DatasetID = "" }},
		{name: "subject", mutate: func(o *KlineObservation) { o.SubjectID = "" }},
		{name: "frequency", mutate: func(o *KlineObservation) { o.Frequency = "\t" }},
		{name: "data time", mutate: func(o *KlineObservation) { o.DataTime = time.Time{} }},
		{name: "commit time", mutate: func(o *KlineObservation) { o.CommittedAt = time.Time{} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			observation := valid
			tt.mutate(&observation)
			require.Error(t, metrics.ObservePrimary(observation))
		})
	}

	view := valid
	view.DatasetID = ""
	view.ViewID = ""
	require.Error(t, metrics.ObserveView(view))
	primary := valid
	primary.DatasetID = ""
	primary.ViewID = "view_kline"
	require.Error(t, metrics.ObservePrimary(primary))
}

func TestKlineMetricsKeepEachSubjectMonotonicAndIsolated(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewKlineMetrics(registry)
	require.NoError(t, err)

	newObservation := func(subject string, dataTime, commitTime time.Time) KlineObservation {
		return KlineObservation{SpaceID: "crypto", DatasetID: "dataset_kline", SubjectID: subject, Frequency: "1m", DataTime: dataTime, CommittedAt: commitTime}
	}
	newerData := time.Unix(200, 0).UTC()
	newerCommit := time.Unix(300, 0).UTC()
	require.NoError(t, metrics.ObservePrimary(newObservation("BTC-USDT", newerData, newerCommit)))
	require.NoError(t, metrics.ObservePrimary(newObservation("ETH-USDT", time.Unix(150, 0).UTC(), time.Unix(250, 0).UTC())))
	require.NoError(t, metrics.ObservePrimary(newObservation("BTC-USDT", time.Unix(100, 0).UTC(), time.Unix(100, 0).UTC())))
	viewObservation := func(dataTime, commitTime time.Time) KlineObservation {
		return KlineObservation{SpaceID: "crypto", ViewID: "view_kline", SubjectID: "BTC-USDT", Frequency: "1m", SeriesTag: "venue:binance", DataTime: dataTime, CommittedAt: commitTime}
	}
	require.NoError(t, metrics.ObserveView(viewObservation(newerData, newerCommit)))
	require.NoError(t, metrics.ObserveView(viewObservation(time.Unix(100, 0).UTC(), time.Unix(100, 0).UTC())))

	assertKlineMetricContract(t, registry, "moox_storage_kline_last_data_time_seconds", map[string]string{
		"space_id": "crypto", "dataset_id": "dataset_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "default",
	}, float64(newerData.Unix()))
	assertKlineMetricContract(t, registry, "moox_storage_kline_last_commit_timestamp_seconds", map[string]string{
		"space_id": "crypto", "dataset_id": "dataset_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "default",
	}, float64(newerCommit.Unix()))
	assertKlineMetricContract(t, registry, "moox_storage_kline_last_data_time_seconds", map[string]string{
		"space_id": "crypto", "dataset_id": "dataset_kline", "subject_id": "ETH-USDT", "freq": "1m", "series_tag": "default",
	}, 150)
	assertKlineMetricContract(t, registry, "moox_storage_view_kline_last_data_time_seconds", map[string]string{
		"space_id": "crypto", "view_id": "view_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "venue:binance",
	}, float64(newerData.Unix()))
	assertKlineMetricContract(t, registry, "moox_storage_view_kline_last_commit_timestamp_seconds", map[string]string{
		"space_id": "crypto", "view_id": "view_kline", "subject_id": "BTC-USDT", "freq": "1m", "series_tag": "venue:binance",
	}, float64(newerCommit.Unix()))
}

func TestKlineMetricsAccept5000Subjects(t *testing.T) {
	registry := prometheus.NewRegistry()
	metrics, err := NewKlineMetrics(registry)
	require.NoError(t, err)
	for i := 0; i < 5000; i++ {
		require.NoError(t, metrics.ObservePrimary(KlineObservation{
			SpaceID: "crypto", DatasetID: "dataset_kline", SubjectID: "subject-" + strconv.Itoa(i), Frequency: "1m",
			DataTime: time.Unix(int64(i+1), 0).UTC(), CommittedAt: time.Unix(int64(i+2), 0).UTC(),
		}))
	}

	families, err := registry.Gather()
	require.NoError(t, err)
	counts := map[string]int{}
	for _, family := range families {
		if family.GetName() == "moox_storage_kline_last_data_time_seconds" || family.GetName() == "moox_storage_kline_last_commit_timestamp_seconds" {
			counts[family.GetName()] = len(family.GetMetric())
		}
	}
	require.Equal(t, 5000, counts["moox_storage_kline_last_data_time_seconds"])
	require.Equal(t, 5000, counts["moox_storage_kline_last_commit_timestamp_seconds"])
}

func assertKlineMetricContract(t *testing.T, registry *prometheus.Registry, name string, wantLabels map[string]string, wantValue float64) {
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
			if labelsMatch(labels, wantLabels) {
				require.Equal(t, wantValue, metric.GetGauge().GetValue())
				return
			}
		}
	}
	require.Fail(t, "metric sample not found", "%s labels=%v", name, wantLabels)
}

func labelsMatch(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
