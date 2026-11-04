package storageio

import (
	"context"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/stretchr/testify/require"
)

// axisBackend 记下换算边界发给 Storage 的参数，按真实时间返回数据。
type axisBackend struct {
	Backend
	read        ReadRequest
	changes     Changes
	existsAt    int64
	datasetGets int
}

func (b *axisBackend) GetDataset(_ context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	b.datasetGets++
	return DatasetInfo{SpaceID: spaceID, DatasetID: datasetID, Freq: "5m"}, nil
}

func (b *axisBackend) ReadWindow(_ context.Context, req ReadRequest) (map[string]*Frame, error) {
	b.read = req
	return map[string]*Frame{"A": {SubjectID: "A", Columns: []string{"data_time", "series_tag"}, Rows: [][]any{{req.Start, ""}}}}, nil
}

func (b *axisBackend) ReadDatasetChanges(context.Context, string, string, *ChangeCursor, bool, int) (Changes, error) {
	return b.changes, nil
}

func (b *axisBackend) ComputedExists(_ context.Context, _, _, _ string, periodTime int64) (bool, error) {
	b.existsAt = periodTime
	return true, nil
}

func cn(value string) time.Time {
	parsed, _ := time.ParseInLocation("2006-01-02 15:04", value, time.FixedZone("CST", 8*3600))
	return parsed.UTC()
}

func TestAxisConvertsStockCNBoundaries(t *testing.T) {
	axis, err := periodclock.AxisFor("stockcn", "5m")
	require.NoError(t, err)
	toAxis := func(value string) time.Time {
		x, err := axis.ToAxis(cn(value))
		require.NoError(t, err)
		return x
	}
	inner := &axisBackend{changes: Changes{Changes: []Change{{Ranges: []ChangeRange{{Subject: "A", Freq: "5m", Start: cn("2026-10-09 14:55"), End: cn("2026-10-12 09:30")}}}}}}
	store := NewAxis(inner)
	ctx := context.Background()

	// 读取区间 [11:25, 13:05) 在轴上是两根：真实的终点是 13:00 那根的收盘时刻。
	frames, err := store.ReadWindow(ctx, ReadRequest{SpaceID: "stockcn", DatasetID: "bars", Freq: "5m", Subjects: []string{"A"},
		Start: toAxis("2026-10-08 11:25"), End: toAxis("2026-10-08 13:05")})
	require.NoError(t, err)
	require.Equal(t, cn("2026-10-08 11:25"), inner.read.Start)
	require.Equal(t, cn("2026-10-08 13:05"), inner.read.End)
	require.Equal(t, toAxis("2026-10-08 11:25"), frames["A"].Rows[0][0], "读回的 data_time 换到轴上")

	// 变更区间两端都是闭区间：周五最后一根到周一第一根，在轴上相邻。
	changes, err := store.ReadDatasetChanges(ctx, "stockcn", "bars", nil, false, 0)
	require.NoError(t, err)
	r := changes.Changes[0].Ranges[0]
	require.Equal(t, 5*time.Minute, r.End.Sub(r.Start))

	// 不带频率的接口按数据集频率换算，频率只查一次。
	for range 2 {
		exists, err := store.ComputedExists(ctx, "stockcn", "factor", "evt", toAxis("2026-10-08 13:00").Unix())
		require.NoError(t, err)
		require.True(t, exists)
		require.Equal(t, cn("2026-10-08 13:00").Unix(), inner.existsAt)
	}
	require.Equal(t, 1, inner.datasetGets)
}

// 7×24 市场原样透传，不查数据集。
func TestAxisPassesCryptoThrough(t *testing.T) {
	inner := &axisBackend{}
	store := NewAxis(inner)
	period := time.Date(2026, 10, 8, 3, 25, 0, 0, time.UTC)
	_, err := store.ComputedExists(context.Background(), "crypto", "factor", "evt", period.Unix())
	require.NoError(t, err)
	require.Equal(t, period.Unix(), inner.existsAt)
	require.Zero(t, inner.datasetGets)
	_, err = store.ReadWindow(context.Background(), ReadRequest{SpaceID: "crypto", DatasetID: "bars", Freq: "5m", Subjects: []string{"A"}, Start: period, End: period.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, period.Add(time.Hour), inner.read.End)
}
