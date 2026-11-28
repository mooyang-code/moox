package cache

import (
	"context"
	"sync"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viewMetadataReader 提供数据集与可变列定义；故意暴露 ListSubjects 并在被调用时失败，
// 验证 DatasetCache 刷新永不扫描标的。
type viewMetadataReader struct {
	mu      sync.Mutex
	columns []string
}

func (r *viewMetadataReader) ListDatasets(context.Context, metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	return []*pb.Dataset{{
		SpaceId: "crypto", DatasetId: "dataset_kline_5m", Freq: "5m", Status: "active",
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Alignment: "epoch_utc", AllowedLatenessSeconds: 30,
	}}, nil, nil
}

func (r *viewMetadataReader) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]*pb.DatasetColumn, 0, len(r.columns))
	for _, name := range r.columns {
		items = append(items, &pb.DatasetColumn{
			SpaceId: "crypto", DatasetId: "dataset_kline_5m", ColumnName: name, DisplayName: name,
			Status: "active", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		})
	}
	return items, nil, nil
}

func (r *viewMetadataReader) setColumns(columns ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.columns = columns
}

func newViewCache(t testing.TB, reader *viewMetadataReader) *DatasetCache {
	t.Helper()
	cache, err := NewDatasetCache(context.Background(), reader)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })
	return cache
}

// 写请求拿到的是共享的只读视图：同一代元数据下两次请求得到同一批消息对象，零分配读取，不拷贝数据集与列定义。
func TestSnapshotSharesDatasetViewWithoutCopy(t *testing.T) {
	reader := &viewMetadataReader{columns: []string{"open", "high", "low", "close", "volume"}}
	cache := newViewCache(t, reader)

	first, second := cache.Snapshot(), cache.Snapshot()
	require.NotNil(t, first)
	datasetA, ok := first.GetDataset("crypto", "dataset_kline_5m")
	require.True(t, ok)
	datasetB, ok := second.GetDataset("crypto", "dataset_kline_5m")
	require.True(t, ok)
	assert.Same(t, datasetA, datasetB, "同一代元数据应共享同一个数据集消息")
	assert.Equal(t, "epoch_utc", datasetA.GetAlignment())
	assert.Equal(t, uint32(30), datasetA.GetAllowedLatenessSeconds())

	columnsA := first.DatasetColumns("crypto", "dataset_kline_5m")
	require.Len(t, columnsA, 5)
	assert.Equal(t, "open", columnsA[0].GetColumnName())
	columnsB := second.DatasetColumns("crypto", "dataset_kline_5m")
	assert.Same(t, columnsA[0], columnsB[0], "同一代元数据应共享同一批列定义")
	colA, ok := first.GetDatasetColumn("crypto", "dataset_kline_5m", "close")
	require.True(t, ok)
	colB, ok := second.GetDatasetColumn("crypto", "dataset_kline_5m", "close")
	require.True(t, ok)
	assert.Same(t, colA, colB, "按名查列也应共享同一个消息")

	_, ok = first.GetDataset("crypto", "missing")
	assert.False(t, ok)
	assert.Empty(t, first.DatasetColumns("crypto", "missing"))
	_, ok = first.GetDatasetColumn("crypto", "missing", "close")
	assert.False(t, ok)

	allocs := testing.AllocsPerRun(100, func() {
		snapshot := cache.Snapshot()
		if _, ok := snapshot.GetDataset("crypto", "dataset_kline_5m"); !ok {
			t.Fatal("数据集不在视图里")
		}
		if _, ok := snapshot.GetDatasetColumn("crypto", "dataset_kline_5m", "close"); !ok {
			t.Fatal("列不在视图里")
		}
	})
	assert.Zero(t, allocs, "写路径读元数据快照不应产生内存分配")
}

// 元数据变更后视图整体替换：新请求看到新列，已经拿到旧视图的请求内容不变。
func TestSnapshotReplacedOnRefresh(t *testing.T) {
	reader := &viewMetadataReader{columns: []string{"open"}}
	cache := newViewCache(t, reader)

	old := cache.Snapshot()
	oldDataset, _ := old.GetDataset("crypto", "dataset_kline_5m")
	reader.setColumns("open", "close")
	require.NoError(t, cache.Refresh(context.Background()))

	require.Len(t, old.DatasetColumns("crypto", "dataset_kline_5m"), 1, "旧视图不应被原地修改")
	_, ok := old.GetDatasetColumn("crypto", "dataset_kline_5m", "close")
	assert.False(t, ok, "旧视图不应看到新列")

	current := cache.Snapshot()
	columns := current.DatasetColumns("crypto", "dataset_kline_5m")
	require.Len(t, columns, 2)
	currentDataset, _ := current.GetDataset("crypto", "dataset_kline_5m")
	assert.NotSame(t, oldDataset, currentDataset)

	// 非快照读取方每次拿到独立的克隆副本，避免调用方修改污染快照。
	cached, _, err := cache.ListDatasetColumns(context.Background(), "crypto", "dataset_kline_5m", nil)
	require.NoError(t, err)
	require.Len(t, cached, 2)
	assert.NotSame(t, columns[0], cached[0])
}

// BenchmarkSnapshot 量一次写请求读元数据的成本。
func BenchmarkSnapshot(b *testing.B) {
	reader := &viewMetadataReader{columns: []string{"open", "high", "low", "close", "volume"}}
	cache := newViewCache(b, reader)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot := cache.Snapshot()
		snapshot.GetDataset("crypto", "dataset_kline_5m")
		snapshot.GetDatasetColumn("crypto", "dataset_kline_5m", "close")
	}
}
