package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	trpc "trpc.group/trpc-go/trpc-go"
)

// DatasetSource 提供数据集与列定义的底层只读查询能力。
type DatasetSource interface {
	ListDatasets(ctx context.Context, query metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error)
	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
}

// DatasetCache 轻量缓存全量数据集定义及其列定义，供写路径热校验与存在性检查无锁读取。
// 标的、标签、空间、数据源等不进入此缓存，避免无关写入触发全表扫描。
type DatasetCache struct {
	base      DatasetSource
	refreshMu sync.Mutex
	views     atomic.Pointer[datasetViews]
}

// datasetKey 是数据集在只读视图里的键。
type datasetKey struct{ spaceID, datasetID string }

// datasetView 保存单个数据集的只读视图及其列定义（保持底层返回顺序）。
type datasetView struct {
	dataset       *pb.Dataset
	columns       []*pb.DatasetColumn
	columnsByName map[string]*pb.DatasetColumn
}

// datasetViews 是不可变的数据集与列定义快照；发布后只读，调用方共享其中的消息，不得修改。
type datasetViews struct {
	datasets []*pb.Dataset
	columns  []*pb.DatasetColumn
	items    map[datasetKey]*datasetView
}

var _ metadata.RequestSnapshot = (*datasetViews)(nil)

// NewDatasetCache 创建并完成首轮数据集与列定义加载。
func NewDatasetCache(ctx context.Context, base DatasetSource) (*DatasetCache, error) {
	if base == nil {
		return nil, errors.New("metadata base store is required")
	}
	cache := &DatasetCache{base: base}
	if err := cache.Refresh(ctx); err != nil {
		return nil, err
	}
	return cache, nil
}

// Close 保留生命周期接口；DatasetCache 不持有后台协程，直接返回 nil。
func (c *DatasetCache) Close() error {
	return nil
}

// Refresh 串行从底层存储加载最新数据集与列定义，构造不可变视图后原子替换。
func (c *DatasetCache) Refresh(ctx context.Context) error {
	if c == nil || c.base == nil {
		return errors.New("dataset cache is not open")
	}
	if ctx == nil {
		ctx = trpc.BackgroundContext()
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	datasets, columns, err := c.fetch(ctx)
	if err != nil {
		return err
	}
	c.views.Store(buildDatasetViews(datasets, columns))
	return nil
}

// RequestSnapshot 返回当前不可变数据集只读视图，供单次写请求全程零拷贝复用。
func (c *DatasetCache) RequestSnapshot() metadata.RequestSnapshot {
	if c == nil {
		return nil
	}
	if views := c.views.Load(); views != nil {
		return views
	}
	return nil
}

// HasDataset 无锁判断数据集是否存在于当前缓存快照中。
func (c *DatasetCache) HasDataset(spaceID string, datasetID string) bool {
	if c == nil {
		return false
	}
	_, ok := c.views.Load().GetDataset(spaceID, datasetID)
	return ok
}

// GetDataset 返回单个数据集的独立副本。
func (c *DatasetCache) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	views, err := c.currentViews()
	if err != nil {
		return nil, err
	}
	item, ok := views.GetDataset(spaceID, datasetID)
	if !ok {
		return nil, notFound("dataset", spaceID, datasetID)
	}
	return proto.Clone(item).(*pb.Dataset), nil
}

// ListDatasets 按条件过滤并分页返回数据集独立副本。
func (c *DatasetCache) ListDatasets(ctx context.Context, query metadata.DatasetQuery) ([]*pb.Dataset, *pb.PageResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	views, err := c.currentViews()
	if err != nil {
		return nil, nil, err
	}
	matched := make([]*pb.Dataset, 0, len(views.datasets))
	for _, item := range views.datasets {
		if query.SpaceID != "" && item.GetSpaceId() != query.SpaceID {
			continue
		}
		if query.DataSourceID != "" && item.GetDataSourceId() != query.DataSourceID {
			continue
		}
		if query.DataKind != pb.DataKind_DATA_KIND_UNSPECIFIED && item.GetDataKind() != query.DataKind {
			continue
		}
		if query.Freq != "" && item.GetFreq() != query.Freq {
			continue
		}
		if query.Status != "" && item.GetStatus() != query.Status {
			continue
		}
		matched = append(matched, item)
	}
	paged, pageResult, err := pageItems(matched, query.Page)
	if err != nil {
		return nil, nil, err
	}
	return cloneSlice(paged), pageResult, nil
}

// ListDatasetColumns 按条件过滤并分页返回列定义独立副本，保持建列顺序。
func (c *DatasetCache) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
	}
	views, err := c.currentViews()
	if err != nil {
		return nil, nil, err
	}
	var matched []*pb.DatasetColumn
	if spaceID != "" && datasetID != "" {
		if item := views.items[datasetKey{spaceID, datasetID}]; item != nil {
			matched = item.columns
		}
	} else {
		matched = make([]*pb.DatasetColumn, 0, len(views.columns))
		for _, column := range views.columns {
			if spaceID != "" && column.GetSpaceId() != spaceID {
				continue
			}
			if datasetID != "" && column.GetDatasetId() != datasetID {
				continue
			}
			matched = append(matched, column)
		}
	}
	paged, pageResult, err := pageItems(matched, page)
	if err != nil {
		return nil, nil, err
	}
	return cloneSlice(paged), pageResult, nil
}

func (c *DatasetCache) currentViews() (*datasetViews, error) {
	if c == nil {
		return nil, errors.New("dataset cache is not open")
	}
	views := c.views.Load()
	if views == nil {
		return nil, errors.New("dataset cache is not initialized")
	}
	return views, nil
}

func (c *DatasetCache) fetch(ctx context.Context) ([]*pb.Dataset, []*pb.DatasetColumn, error) {
	if snapshotter, ok := c.base.(interface {
		WithReadSnapshot(context.Context, func(context.Context) error) error
	}); ok {
		var datasets []*pb.Dataset
		var columns []*pb.DatasetColumn
		err := snapshotter.WithReadSnapshot(ctx, func(snapshotCtx context.Context) error {
			var err error
			datasets, columns, err = c.fetchFrom(snapshotCtx)
			return err
		})
		return datasets, columns, err
	}
	return c.fetchFrom(ctx)
}

func (c *DatasetCache) fetchFrom(ctx context.Context) ([]*pb.Dataset, []*pb.DatasetColumn, error) {
	datasets, err := collectPages(ctx, func(page *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
		return c.base.ListDatasets(ctx, metadata.DatasetQuery{Page: page})
	})
	if err != nil {
		return nil, nil, err
	}
	columns, err := collectPages(ctx, func(page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
		return c.base.ListDatasetColumns(ctx, "", "", page)
	})
	if err != nil {
		return nil, nil, err
	}
	return datasets, columns, nil
}

// GetDataset 返回只读视图里共享的数据集消息，调用方不得修改。
func (v *datasetViews) GetDataset(spaceID string, datasetID string) (*pb.Dataset, bool) {
	if v == nil {
		return nil, false
	}
	item := v.items[datasetKey{spaceID, datasetID}]
	if item == nil || item.dataset == nil {
		return nil, false
	}
	return item.dataset, true
}

// ListDatasetColumns 返回只读视图里共享的列定义（切片与消息均只读）。
func (v *datasetViews) ListDatasetColumns(spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	if v == nil {
		return nil, nil, errors.New("元数据缓存尚未发布")
	}
	var columns []*pb.DatasetColumn
	if item := v.items[datasetKey{spaceID, datasetID}]; item != nil {
		columns = item.columns
	}
	return pageItems(columns, page)
}

// buildDatasetViews 用一次取回的数据集与列定义构造不可变只读视图；每条消息独立克隆，不与底层对象共享内存。
func buildDatasetViews(datasets []*pb.Dataset, columns []*pb.DatasetColumn) *datasetViews {
	clonedDatasets := make([]*pb.Dataset, 0, len(datasets))
	items := make(map[datasetKey]*datasetView, len(datasets))
	for _, dataset := range datasets {
		if dataset == nil {
			continue
		}
		cloned := proto.Clone(dataset).(*pb.Dataset)
		clonedDatasets = append(clonedDatasets, cloned)
		key := datasetKey{cloned.GetSpaceId(), cloned.GetDatasetId()}
		items[key] = &datasetView{dataset: cloned}
	}
	clonedColumns := make([]*pb.DatasetColumn, 0, len(columns))
	for _, column := range columns {
		if column == nil {
			continue
		}
		cloned := proto.Clone(column).(*pb.DatasetColumn)
		clonedColumns = append(clonedColumns, cloned)
		key := datasetKey{cloned.GetSpaceId(), cloned.GetDatasetId()}
		item := items[key]
		if item == nil {
			item = &datasetView{}
			items[key] = item
		}
		if item.columnsByName == nil {
			item.columnsByName = make(map[string]*pb.DatasetColumn)
		}
		item.columns = append(item.columns, cloned)
		item.columnsByName[cloned.GetColumnName()] = cloned
	}
	return &datasetViews{
		datasets: clonedDatasets,
		columns:  clonedColumns,
		items:    items,
	}
}

func cloneSlice[T proto.Message](items []T) []T {
	out := make([]T, len(items))
	for i, item := range items {
		out[i] = proto.Clone(item).(T)
	}
	return out
}

func collectPages[T any](ctx context.Context, list func(page *pb.Page) ([]T, *pb.PageResult, error)) ([]T, error) {
	const pageSize uint32 = 1000
	var out []T
	for pageNo := uint32(1); ; pageNo++ {
		items, result, err := list(&pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if result == nil || !result.GetHasMore() {
			return out, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

func pageItems[T any](items []T, page *pb.Page) ([]T, *pb.PageResult, error) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	offset64 := uint64(pageNo-1) * uint64(size)
	maxInt := int(^uint(0) >> 1)
	start := int(offset64)
	if offset64 > uint64(maxInt) || start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	// 容量截到长度：返回的切片可能来自各请求共享的只读视图，调用方 append 时必须另行分配，不能写进共享的底层数组。
	return items[start:end:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(items)), HasMore: end < len(items)}, nil
}

func notFound(kind string, spaceID string, id string) error {
	return fmt.Errorf("metadata row not found: %s/%s/%s: %w", kind, spaceID, id, sql.ErrNoRows)
}
