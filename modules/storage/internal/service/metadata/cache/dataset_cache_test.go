package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubDatasetSource struct {
	mu          sync.Mutex
	datasets    []*pb.Dataset
	columns     []*pb.DatasetColumn
	datasetCall atomic.Int64
	columnCall  atomic.Int64
	snapshotHit atomic.Int64
	err         error
}

func (s *stubDatasetSource) WithReadSnapshot(ctx context.Context, fn func(context.Context) error) error {
	s.snapshotHit.Add(1)
	return fn(ctx)
}

func (s *stubDatasetSource) ListDatasets(_ context.Context, query metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	s.datasetCall.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, nil, s.err
	}
	return pageItems(s.datasets, query.Page)
}

func (s *stubDatasetSource) ListDatasetColumns(_ context.Context, _ string, _ string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	s.columnCall.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, nil, s.err
	}
	return pageItems(s.columns, page)
}

func TestNewDatasetCache_NilBase_ReturnsError(t *testing.T) {
	_, err := NewDatasetCache(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata base store is required")
}

func TestDatasetCache_GetListAndFilters(t *testing.T) {
	ctx := context.Background()
	src := &stubDatasetSource{
		datasets: []*pb.Dataset{
			{
				SpaceId: "crypto", DatasetId: "dataset_kline_5m", DataSourceId: "binance",
				Name: "K线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "5m", Status: "active", Revision: 7,
			},
			{
				SpaceId: "crypto", DatasetId: "dataset_record_a", DataSourceId: "internal",
				Name: "记录", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "disabled", Revision: 2,
			},
			{
				SpaceId: "stockcn", DatasetId: "dataset_kline_1d", DataSourceId: "baostock",
				Name: "A股日线", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1d", Status: "active", Revision: 3,
			},
		},
		columns: []*pb.DatasetColumn{
			{SpaceId: "crypto", DatasetId: "dataset_kline_5m", ColumnName: "open", DisplayName: "开盘", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
			{SpaceId: "crypto", DatasetId: "dataset_kline_5m", ColumnName: "close", DisplayName: "收盘", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
			{SpaceId: "stockcn", DatasetId: "dataset_kline_1d", ColumnName: "close", DisplayName: "收盘", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		},
	}

	cache, err := NewDatasetCache(ctx, src)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cache.Close()) })

	assert.Equal(t, int64(1), src.snapshotHit.Load(), "应通过 WithReadSnapshot 保证数据集与列的一致性读取")
	assert.Equal(t, int64(1), src.datasetCall.Load())
	assert.Equal(t, int64(1), src.columnCall.Load())

	assert.True(t, cache.HasDataset("crypto", "dataset_kline_5m"))
	assert.False(t, cache.HasDataset("crypto", "missing"))

	ds, err := cache.GetDataset(ctx, "crypto", "dataset_kline_5m")
	require.NoError(t, err)
	assert.Equal(t, "active", ds.GetStatus())
	assert.Equal(t, uint64(7), ds.GetRevision())
	// 修改 GetDataset 返回值不得污染缓存快照。
	ds.Status = "disabled"
	dsAgain, err := cache.GetDataset(ctx, "crypto", "dataset_kline_5m")
	require.NoError(t, err)
	assert.Equal(t, "active", dsAgain.GetStatus())

	_, err = cache.GetDataset(ctx, "crypto", "missing")
	require.ErrorIs(t, err, sql.ErrNoRows)

	// 过滤查询
	tsActive, page, err := cache.ListDatasets(ctx, metadata.DatasetQuery{
		DataKind: pb.DataKind_DATA_KIND_TIME_SERIES,
		Status:   "active",
	})
	require.NoError(t, err)
	require.Len(t, tsActive, 2)
	assert.Equal(t, uint32(2), page.GetTotal())

	bySpaceAndSource, _, err := cache.ListDatasets(ctx, metadata.DatasetQuery{
		SpaceID:      "crypto",
		DataSourceID: "binance",
		Freq:         "5m",
	})
	require.NoError(t, err)
	require.Len(t, bySpaceAndSource, 1)
	assert.Equal(t, "dataset_kline_5m", bySpaceAndSource[0].GetDatasetId())

	cols, _, err := cache.ListDatasetColumns(ctx, "crypto", "dataset_kline_5m", nil)
	require.NoError(t, err)
	require.Len(t, cols, 2)
	assert.Equal(t, "open", cols[0].GetColumnName())
	assert.Equal(t, "close", cols[1].GetColumnName())

	allCols, _, err := cache.ListDatasetColumns(ctx, "", "", nil)
	require.NoError(t, err)
	require.Len(t, allCols, 3)

	// 读操作不得再次访问底层存储。
	assert.Equal(t, int64(1), src.datasetCall.Load())
	assert.Equal(t, int64(1), src.columnCall.Load())
}

func TestDatasetCache_RefreshFailurePreservesPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	src := &stubDatasetSource{
		datasets: []*pb.Dataset{{SpaceId: "crypto", DatasetId: "dataset_a", Status: "active"}},
	}
	cache, err := NewDatasetCache(ctx, src)
	require.NoError(t, err)

	src.mu.Lock()
	src.err = errors.New("sqlite busy")
	src.mu.Unlock()

	err = cache.Refresh(ctx)
	require.Error(t, err)
	assert.True(t, cache.HasDataset("crypto", "dataset_a"), "刷新失败时应保留上一次成功发布的快照")
}

func TestDatasetCache_ConcurrentRefreshAndReads(t *testing.T) {
	ctx := context.Background()
	src := &stubDatasetSource{
		datasets: []*pb.Dataset{{SpaceId: "crypto", DatasetId: "dataset_kline_5m", Status: "active", Revision: 1}},
		columns:  []*pb.DatasetColumn{{SpaceId: "crypto", DatasetId: "dataset_kline_5m", ColumnName: "c0", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}},
	}
	cache, err := NewDatasetCache(ctx, src)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				src.mu.Lock()
				src.datasets = []*pb.Dataset{{SpaceId: "crypto", DatasetId: "dataset_kline_5m", Status: "active", Revision: uint64(worker*100 + i + 1)}}
				src.columns = []*pb.DatasetColumn{{SpaceId: "crypto", DatasetId: "dataset_kline_5m", ColumnName: fmt.Sprintf("c_%d_%d", worker, i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"}}
				src.mu.Unlock()
				require.NoError(t, cache.Refresh(ctx))
			}
		}(w)
	}
	for r := 0; r < 8; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				snap := cache.Snapshot()
				require.NotNil(t, snap)
				ds, ok := snap.GetDataset("crypto", "dataset_kline_5m")
				require.True(t, ok)
				require.NotNil(t, ds)
				cols := snap.DatasetColumns("crypto", "dataset_kline_5m")
				require.Len(t, cols, 1)
				col, ok := snap.GetDatasetColumn("crypto", "dataset_kline_5m", cols[0].GetColumnName())
				require.True(t, ok)
				require.Same(t, cols[0], col)
				require.True(t, cache.HasDataset("crypto", "dataset_kline_5m"))
				_, err = cache.GetDataset(ctx, "crypto", "dataset_kline_5m")
				require.NoError(t, err)
			}
		}()
	}
	wg.Wait()
}
