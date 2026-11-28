package catalog

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata/sqlite"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

// newDatasetFixture 建一个带 close、bias_20 两列的时序数据集和对应的元数据服务。
func newDatasetFixture(t *testing.T) (*sqlite.Store, *Service, *pb.Dataset) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, sqlite.Options{
		Path:       filepath.Join(t.TempDir(), "metadata.db"),
		SchemaPath: filepath.Join("..", "..", "..", "schema", "metadata.sql"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.InitSchema(ctx))
	_, err = store.UpsertSpace(ctx, &pb.Space{SpaceId: "factor-space", Name: "因子空间"})
	require.NoError(t, err)
	_, err = store.UpsertDataSource(ctx, &pb.DataSource{SpaceId: "factor-space", DataSourceId: "factor", Name: "因子计算"})
	require.NoError(t, err)
	dataset, err := store.CreateDataset(ctx, &pb.Dataset{
		SpaceId: "factor-space", DatasetId: "dataset_factor_btc_1m", DataSourceId: "factor",
		Name: "BTC 因子", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m",
	})
	require.NoError(t, err)
	for _, column := range []struct{ name, display string }{{"close", "收盘"}, {"bias_20", "偏离"}} {
		_, err := store.UpsertDatasetColumn(ctx, newDatasetColumn(dataset, column.name, column.display))
		require.NoError(t, err)
	}
	service, err := NewMetadataService(store, nil, Options{Rows: noopRows{}})
	require.NoError(t, err)
	return store, service, dataset
}

// noopRows 是不存行的行存储：只关心元数据的测试用它。
type noopRows struct{}

func (noopRows) DeleteDatasetRows(context.Context, string, string) (uint64, error) { return 0, nil }

func newDatasetColumn(dataset *pb.Dataset, name, displayName string) *pb.DatasetColumn {
	return &pb.DatasetColumn{
		SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId(), ColumnName: name,
		ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, DisplayName: displayName,
	}
}

func columnNames(columns []*pb.DatasetColumn) []string {
	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.GetColumnName())
	}
	return names
}

// 列自描述：显示名必填，按建列顺序列出；缓存与底层一致。
func TestDatasetColumnsCarryTheirOwnDisplayName(t *testing.T) {
	ctx := context.Background()
	store, service, dataset := newDatasetFixture(t)

	missing := newDatasetColumn(dataset, "momentum_5", "")
	rsp, err := service.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: missing})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "列必须带显示名")

	added := newDatasetColumn(dataset, "momentum_5", "momentum_5")
	added.Group = "动量"
	rsp, err = service.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: added})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())

	listed, err := service.ListDatasetColumns(ctx, &pb.ListDatasetColumnsReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, []string{"close", "bias_20", "momentum_5"}, columnNames(listed.GetColumns()))
	require.Equal(t, "动量", listed.GetColumns()[2].GetGroup())

	cached, err := metacache.NewDatasetCache(ctx, store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cached.Close() })
	columns, _, err := cached.ListDatasetColumns(ctx, dataset.GetSpaceId(), dataset.GetDatasetId(), nil)
	require.NoError(t, err)
	require.Equal(t, []string{"close", "bias_20", "momentum_5"}, columnNames(columns), "缓存保留建列顺序")
}

// 标的/标签/数据源等非数据集写入不触发 DatasetCache 刷新；数据集/列/删空间变更必须同步刷新。
func TestDatasetCacheRefreshOnlyTriggeredByDatasetMutations(t *testing.T) {
	ctx := context.Background()
	store, service, dataset := newDatasetFixture(t)
	cached, err := metacache.NewDatasetCache(ctx, store)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cached.Close() })

	var refreshes int
	service.datasetCache = cached
	service.refreshOverride = func(ctx context.Context) error {
		refreshes++
		return cached.Refresh(ctx)
	}

	_, err = service.UpsertSubject(ctx, &pb.UpsertSubjectReq{Subject: &pb.Subject{SpaceId: dataset.GetSpaceId(), SubjectId: "BTC-USDT", Name: "BTC"}})
	require.NoError(t, err)
	_, err = service.UpsertTag(ctx, &pb.UpsertTagReq{Tag: &pb.Tag{SpaceId: dataset.GetSpaceId(), TagId: "hot", TagName: "热门"}})
	require.NoError(t, err)
	_, err = service.ApplyTagSnapshot(ctx, &pb.ApplyTagSnapshotReq{
		SpaceId: dataset.GetSpaceId(), TagId: "hot",
		Items: []*pb.TagSnapshotItem{{SubjectId: "BTC-USDT", Name: "BTC"}},
	})
	require.NoError(t, err)
	_, err = service.UpdateSubjectAttributes(ctx, &pb.UpdateSubjectAttributesReq{
		SpaceId: dataset.GetSpaceId(),
		Items:   []*pb.SubjectAttributes{{SubjectId: "BTC-USDT", Attributes: map[string]string{"sector": "layer1"}}},
	})
	require.NoError(t, err)
	_, err = service.CreateDataSource(ctx, &pb.CreateDataSourceReq{DataSource: &pb.DataSource{SpaceId: dataset.GetSpaceId(), DataSourceId: "ds_extra", Name: "扩展源"}})
	require.NoError(t, err)
	require.Equal(t, 0, refreshes, "非数据集变更不应触发 DatasetCache 刷新")

	rspCol, err := service.UpsertDatasetColumn(ctx, &pb.UpsertDatasetColumnReq{Column: newDatasetColumn(dataset, "rsi_14", "RSI")})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rspCol.GetRetInfo().GetCode())
	require.Equal(t, 1, refreshes)
	cols, _, err := cached.ListDatasetColumns(ctx, dataset.GetSpaceId(), dataset.GetDatasetId(), nil)
	require.NoError(t, err)
	require.True(t, slices.ContainsFunc(cols, func(col *pb.DatasetColumn) bool { return col.GetColumnName() == "rsi_14" }))

	rspDel, err := service.DeleteDataset(ctx, &pb.DeleteDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rspDel.GetRetInfo().GetCode())
	require.Equal(t, 2, refreshes)
	require.False(t, cached.HasDataset(dataset.GetSpaceId(), dataset.GetDatasetId()))
}

// 每个数据集恰好一个查询索引，不单独登记：删数据集一次调用即可。
func TestDeleteDatasetNeedsNoViewStep(t *testing.T) {
	ctx := context.Background()
	_, service, dataset := newDatasetFixture(t)
	rsp, err := service.DeleteDataset(ctx, &pb.DeleteDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
}

// maintenance_priority 建数据集时可指定，之后可改；按状态过滤只列出启用的数据集。
func TestDatasetMaintenancePriorityAndStatusFilter(t *testing.T) {
	ctx := context.Background()
	_, service, dataset := newDatasetFixture(t)
	created, err := service.CreateDataset(ctx, &pb.CreateDatasetReq{Dataset: &pb.Dataset{
		SpaceId: dataset.GetSpaceId(), DatasetId: "dataset_factor_eth_1m", DataSourceId: "factor",
		Name: "ETH 因子", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m", MaintenancePriority: 10,
	}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, created.GetRetInfo().GetCode(), created.GetRetInfo().GetMsg())
	require.Equal(t, uint32(10), created.GetDataset().GetMaintenancePriority())

	update := created.GetDataset()
	update.MaintenancePriority, update.Status = 3, "disabled"
	updated, err := service.UpdateDataset(ctx, &pb.UpdateDatasetReq{Dataset: update})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, updated.GetRetInfo().GetCode(), updated.GetRetInfo().GetMsg())
	require.Equal(t, uint32(3), updated.GetDataset().GetMaintenancePriority())

	active, err := service.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: dataset.GetSpaceId(), Status: "active"})
	require.NoError(t, err)
	require.Len(t, active.GetDatasets(), 1)
	require.Equal(t, dataset.GetDatasetId(), active.GetDatasets()[0].GetDatasetId())
}

func TestMetadataReturnsDatasetRetentionFromPolicy(t *testing.T) {
	_, service, dataset := newDatasetFixture(t)
	ctx := context.Background()
	got, err := service.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, got.GetRetInfo().GetCode(), got.GetRetInfo().GetMsg())
	require.Equal(t, "840h", got.GetDataset().GetRetention(), "a 1m Dataset in a space without overrides keeps 35 days")
	require.Equal(t, "default", got.GetDataset().GetRetentionSource())

	listed, err := service.ListDatasets(ctx, &pb.ListDatasetsReq{SpaceId: dataset.GetSpaceId()})
	require.NoError(t, err)
	require.Len(t, listed.GetDatasets(), 1)
	require.Equal(t, "840h", listed.GetDatasets()[0].GetRetention())

	retention, source := service.datasetRetention(&pb.Dataset{DataKind: pb.DataKind_DATA_KIND_RECORD})
	require.Equal(t, "forever", retention)
	require.Equal(t, "record", source)
	service.retention.Spaces = map[string]map[string]string{"stockcn": {"1m": "60d"}}
	retention, source = service.datasetRetention(&pb.Dataset{SpaceId: "stockcn", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Freq: "1m"})
	require.Equal(t, "1440h", retention)
	require.Equal(t, "space", source)
}

func TestGetDatasetReturnsLiveSubjectsRevision(t *testing.T) {
	store, service, dataset := newDatasetFixture(t)
	ctx := context.Background()
	first, err := service.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, first.GetRetInfo().GetCode(), first.GetRetInfo().GetMsg())

	_, err = store.UpsertSubject(ctx, &pb.Subject{SpaceId: dataset.GetSpaceId(), SubjectId: "REV-USDT", Name: "REV"})
	require.NoError(t, err)
	second, err := service.GetDataset(ctx, &pb.GetDatasetReq{SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	require.NoError(t, err)
	require.Greater(t, second.GetSubjectsRevision(), first.GetSubjectsRevision(), "新增标的后 GetDataset 返回的修订号必须递增")
}
