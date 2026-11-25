package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// viewMetadataReader 提供大量标的、一个数据集和可变的列定义，用来确认写路径的元数据读取与标的数量无关。
type viewMetadataReader struct {
	stubMetadataReader
	mu       sync.Mutex
	subjects int
	columns  []string
}

func (r *viewMetadataReader) ListSubjects(_ context.Context, _ string, _ []string, _ string, page *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	items := make([]*pb.Subject, 0, r.subjects)
	for i := 0; i < r.subjects; i++ {
		items = append(items, &pb.Subject{SpaceId: "crypto", SubjectId: fmt.Sprintf("S%05d-USDT", i), Name: "x", Status: "active"})
	}
	return pageItems(items, page)
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

func newViewStore(t testing.TB, reader *viewMetadataReader) *Store {
	t.Helper()
	store, err := New(context.Background(), reader, Options{RefreshInterval: RefreshDisabled, InitialLoadTimeout: 30 * time.Second})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return store
}

// 写请求拿到的是共享的只读视图：同一代元数据下两次请求得到同一批消息对象，读取的分配次数是常数，
// 不随标的数量增长（原来每个请求把全部条目连同 payload 深拷贝一遍）。
func TestRequestSnapshotSharesDatasetViewWithoutCopy(t *testing.T) {
	reader := &viewMetadataReader{subjects: 10000, columns: []string{"open", "high", "low", "close", "volume"}}
	store := newViewStore(t, reader)

	first, second := store.RequestSnapshot(), store.RequestSnapshot()
	require.NotNil(t, first)
	datasetA, ok := first.GetDataset("crypto", "dataset_kline_5m")
	require.True(t, ok)
	datasetB, ok := second.GetDataset("crypto", "dataset_kline_5m")
	require.True(t, ok)
	assert.Same(t, datasetA, datasetB, "同一代元数据应共享同一个数据集消息")
	assert.Equal(t, "epoch_utc", datasetA.GetAlignment())
	assert.Equal(t, uint32(30), uint32(datasetA.GetAllowedLatenessSeconds()))

	columnsA, page, err := first.ListDatasetColumns("crypto", "dataset_kline_5m", &pb.Page{Page: 1, Size: 1000})
	require.NoError(t, err)
	require.Len(t, columnsA, 5)
	assert.False(t, page.GetHasMore())
	assert.Equal(t, "open", columnsA[0].GetColumnName())
	columnsB, _, err := second.ListDatasetColumns("crypto", "dataset_kline_5m", &pb.Page{Page: 1, Size: 1000})
	require.NoError(t, err)
	assert.Same(t, columnsA[0], columnsB[0], "同一代元数据应共享同一批列定义")

	_, ok = first.GetDataset("crypto", "missing")
	assert.False(t, ok)
	missing, _, err := first.ListDatasetColumns("crypto", "missing", nil)
	require.NoError(t, err)
	assert.Empty(t, missing)

	query := &pb.Page{Page: 1, Size: 1000}
	allocs := testing.AllocsPerRun(100, func() {
		snapshot := store.RequestSnapshot()
		if _, ok := snapshot.GetDataset("crypto", "dataset_kline_5m"); !ok {
			t.Fatal("数据集不在视图里")
		}
		if _, _, err := snapshot.ListDatasetColumns("crypto", "dataset_kline_5m", query); err != nil {
			t.Fatal(err)
		}
	})
	assert.LessOrEqual(t, allocs, float64(2), "写路径读元数据不应随标的数量分配内存")
}

// 元数据变更后视图整体替换：新请求看到新列，已经拿到旧视图的请求内容不变。
func TestRequestSnapshotReplacedOnRefresh(t *testing.T) {
	reader := &viewMetadataReader{subjects: 3, columns: []string{"open"}}
	store := newViewStore(t, reader)

	old := store.RequestSnapshot()
	oldDataset, _ := old.GetDataset("crypto", "dataset_kline_5m")
	reader.setColumns("open", "close")
	require.NoError(t, store.Refresh(context.Background()))

	oldColumns, _, err := old.ListDatasetColumns("crypto", "dataset_kline_5m", nil)
	require.NoError(t, err)
	require.Len(t, oldColumns, 1, "旧视图不应被原地修改")

	current := store.RequestSnapshot()
	columns, _, err := current.ListDatasetColumns("crypto", "dataset_kline_5m", nil)
	require.NoError(t, err)
	require.Len(t, columns, 2)
	currentDataset, _ := current.GetDataset("crypto", "dataset_kline_5m")
	assert.NotSame(t, oldDataset, currentDataset)

	// 其他读取方仍走原来的缓存索引，每次返回各自解码的副本。
	cached, _, err := store.ListDatasetColumns(context.Background(), "crypto", "dataset_kline_5m", nil)
	require.NoError(t, err)
	require.Len(t, cached, 2)
	assert.NotSame(t, columns[0], cached[0])
}

// BenchmarkRequestSnapshot 量一次写请求读元数据的成本（1 万个标的）。
func BenchmarkRequestSnapshot(b *testing.B) {
	reader := &viewMetadataReader{subjects: 10000, columns: []string{"open", "high", "low", "close", "volume"}}
	store := newViewStore(b, reader)
	query := &pb.Page{Page: 1, Size: 1000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snapshot := store.RequestSnapshot()
		snapshot.GetDataset("crypto", "dataset_kline_5m")
		snapshot.ListDatasetColumns("crypto", "dataset_kline_5m", query)
	}
}
