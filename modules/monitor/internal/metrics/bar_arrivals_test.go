package metrics

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	"github.com/mooyang-code/moox/modules/monitor/schema"
	"github.com/mooyang-code/moox/packages/events/eventpb"
	metricspb "github.com/mooyang-code/moox/packages/metricspb"
	"github.com/stretchr/testify/require"
)

// arrivalSnapshot 是一份快照里某个数据集的输出水位与到达时间（同一标签身份）。
func arrivalSnapshot(service, metricPrefix, labelsJSON string, labels map[string]string, bar, arrived, observed time.Time) []Sample {
	watermark, arrival := metricPrefix+"_output_watermark_timestamp_seconds", metricPrefix+"_output_arrival_timestamp_seconds"
	return []Sample{
		{SeriesID: service + "|" + watermark + "|" + labelsJSON, ServiceName: service, InstanceID: service + "@node", MetricName: watermark, MetricType: "gauge",
			Labels: labels, LabelsJSON: labelsJSON, Value: float64(bar.Unix()), ObservedAt: observed},
		{SeriesID: service + "|" + arrival + "|" + labelsJSON, ServiceName: service, InstanceID: service + "@node", MetricName: arrival, MetricType: "gauge",
			Labels: labels, LabelsJSON: labelsJSON, Value: float64(arrived.Unix()), ObservedAt: observed},
	}
}

var (
	storageLabels     = map[string]string{"space_id": "crypto", "dataset_id": "dataset_spot_1m", "freq": "1m"}
	storageLabelsJSON = `{"dataset_id":"dataset_spot_1m","freq":"1m","space_id":"crypto"}`
	viewLabels        = map[string]string{"space_id": "crypto", "view_id": "view_spot_kline_1m", "dataset_id": "dataset_spot_1m", "freq": "1m"}
	viewLabelsJSON    = `{"dataset_id":"dataset_spot_1m","freq":"1m","space_id":"crypto","view_id":"view_spot_kline_1m"}`
)

func TestDetectBarArrivalsOnlyForAdvancedPipelineWatermarks(t *testing.T) {
	bar := time.Date(2026, 10, 16, 18, 54, 0, 0, time.UTC)
	observed := bar.Add(time.Minute + 2*time.Second)
	storage := arrivalSnapshot("storage-primary", "moox_storage_dataset", storageLabelsJSON, storageLabels, bar, bar.Add(time.Minute+time.Second), observed)
	view := arrivalSnapshot("storage-view", "moox_storage_view", viewLabelsJSON, viewLabels, bar, bar.Add(time.Minute+2*time.Second), observed)
	collector := arrivalSnapshot("collector", "moox_collector_dataset", storageLabelsJSON, storageLabels, bar, bar.Add(time.Minute), observed)
	system := arrivalSnapshot("storage-view", "moox_storage_view", `{"dataset_id":"dataset_mooxsys_host_resource","freq":"1m","space_id":"mooxsys","view_id":"view_mooxsys_host_resource"}`,
		map[string]string{"space_id": "mooxsys", "view_id": "view_mooxsys_host_resource", "dataset_id": "dataset_mooxsys_host_resource", "freq": "1m"}, bar, bar, observed)
	samples := append(append(append(append([]Sample(nil), storage...), view...), collector...), system...)

	// 第一次见到：入库与视图各记一根；采集不是数据链路的环节，mooxsys 系统数据没有交易日历，都不记。
	first := detectBarArrivals(samples, nil)
	require.Len(t, first, 2)
	require.Equal(t, "storage", first[0].Producer)
	require.Equal(t, "dataset_spot_1m", first[0].DatasetID)
	require.True(t, first[0].BarAt.Equal(bar))
	require.True(t, first[0].ArrivedAt.Equal(bar.Add(time.Minute+time.Second)))
	require.Nil(t, first[0].PreviousBarAt)
	require.Equal(t, "storage_view", first[1].Producer)
	require.Equal(t, "view_spot_kline_1m", first[1].DatasetID)
	require.Equal(t, "dataset_spot_1m", first[1].PrimaryDatasetID)

	// 水位没有前进：不记录。
	previous := map[string]float64{storage[0].SeriesID: float64(bar.Unix()), view[0].SeriesID: float64(bar.Unix())}
	require.Empty(t, detectBarArrivals(append(append([]Sample(nil), storage...), view...), previous))

	// 补数据时一批追了 3 根：只记最新那根，带上上一份快照的水位。
	caughtUp := arrivalSnapshot("storage-primary", "moox_storage_dataset", storageLabelsJSON, storageLabels, bar.Add(3*time.Minute), bar.Add(4*time.Minute+5*time.Second), observed.Add(3*time.Minute))
	got := detectBarArrivals(caughtUp, previous)
	require.Len(t, got, 1)
	require.True(t, got[0].BarAt.Equal(bar.Add(3*time.Minute)))
	require.NotNil(t, got[0].PreviousBarAt)
	require.True(t, got[0].PreviousBarAt.Equal(bar))

	// 缺到达时间的不推算。
	require.Empty(t, detectBarArrivals(storage[:1], nil))
}

func TestCommitIngestRecordsEachAdvanceOnce(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	r := metricMessageStoreForTest(t, mgr)
	report := &metricspb.MetricReport{ServiceName: "storage-primary", InstanceId: "storage-primary@node", BootId: "boot-a"}
	bar := time.Date(2026, 10, 16, 18, 54, 0, 0, time.UTC)
	commit := func(id string, samples []Sample) {
		t.Helper()
		_, err := r.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: id}, report, samples)
		require.NoError(t, err)
	}
	snapshot := func(barAt time.Time, observed time.Time) []Sample {
		return arrivalSnapshot("storage-primary", "moox_storage_dataset", storageLabelsJSON, storageLabels, barAt, barAt.Add(time.Minute+time.Second), observed)
	}

	commit("m1", snapshot(bar, bar.Add(time.Minute+2*time.Second)))
	// 重复投递同一份快照、以及下一份水位没变的快照，都不再记录。
	commit("m1", snapshot(bar, bar.Add(time.Minute+2*time.Second)))
	commit("m2", snapshot(bar, bar.Add(time.Minute+32*time.Second)))
	commit("m3", snapshot(bar.Add(time.Minute), bar.Add(2*time.Minute+2*time.Second)))

	chain := BarArrivalChain{SpaceID: "crypto", Freq: "1m", SourceDatasetID: "dataset_spot_1m", FactorDatasetID: "dataset_factor_spot_1m"}
	rows, truncated, err := r.ListBarArrivals(context.Background(), chain, bar.Add(-time.Hour), bar.Add(time.Hour))
	require.NoError(t, err)
	require.False(t, truncated)
	require.Len(t, rows, 2)
	require.True(t, rows[0].BarAt.Equal(bar))
	require.True(t, rows[1].BarAt.Equal(bar.Add(time.Minute)))
	require.NotNil(t, rows[1].PreviousBarAt)
	require.True(t, rows[1].PreviousBarAt.Equal(bar))
	require.True(t, rows[1].ArrivedAt.Equal(bar.Add(2*time.Minute+time.Second)))

	// 查询区间按开盘时间、左闭右开；别的链路、别的周期不返回。
	rows, _, err = r.ListBarArrivals(context.Background(), chain, bar.Add(time.Minute), bar.Add(2*time.Minute))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	other := chain
	other.SourceDatasetID, other.FactorDatasetID = "dataset_swap_1m", "dataset_factor_swap_1m"
	rows, _, err = r.ListBarArrivals(context.Background(), other, bar.Add(-time.Hour), bar.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, rows)
	hourly := chain
	hourly.Freq = "1h"
	rows, _, err = r.ListBarArrivals(context.Background(), hourly, bar.Add(-time.Hour), bar.Add(time.Hour))
	require.NoError(t, err)
	require.Empty(t, rows)

	// 超过上限时只返回前面的部分并标出截断。
	defer func(limit int) { maxBarArrivalRows = limit }(maxBarArrivalRows)
	maxBarArrivalRows = 1
	rows, truncated, err = r.ListBarArrivals(context.Background(), chain, bar.Add(-time.Hour), bar.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, truncated)
	require.Len(t, rows, 1)
	maxBarArrivalRows = 50000

	// 按到达时间清理：第一根 18:55:01 到达，第二根 18:56:01 到达。
	deleted, err := r.PruneBarArrivals(context.Background(), bar.Add(90*time.Second))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	rows, _, err = r.ListBarArrivals(context.Background(), chain, bar.Add(-time.Hour), bar.Add(time.Hour))
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestPruneBarArrivalsKeepsRecentlyArrivedWeeklyBars(t *testing.T) {
	mgr, err := store.Open(filepath.Join(t.TempDir(), "monitor.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close() })
	require.NoError(t, mgr.ApplySchema(schema.SQL()))
	r := metricMessageStoreForTest(t, mgr)
	report := &metricspb.MetricReport{ServiceName: "storage-primary", InstanceId: "storage-primary@node", BootId: "boot-a"}
	// 周线 10-12 开盘、10-19 收线、10-19 00:00:05 到达：开盘早于 7 天，但刚到，不能被清理。
	weekOpen := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	weekly := map[string]string{"space_id": "crypto", "dataset_id": "dataset_spot_1w", "freq": "1w"}
	samples := arrivalSnapshot("storage-primary", "moox_storage_dataset", `{"dataset_id":"dataset_spot_1w","freq":"1w","space_id":"crypto"}`, weekly,
		weekOpen, weekOpen.AddDate(0, 0, 7).Add(5*time.Second), weekOpen.AddDate(0, 0, 7).Add(30*time.Second))
	_, err = r.CommitIngest(context.Background(), &eventpb.EventMessage{EventId: "w1"}, report, samples)
	require.NoError(t, err)
	now := weekOpen.AddDate(0, 0, 7).Add(time.Hour)
	deleted, err := r.PruneBarArrivals(context.Background(), now.Add(-BarArrivalRetention))
	require.NoError(t, err)
	require.Zero(t, deleted)
}
