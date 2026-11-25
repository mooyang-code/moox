package resample

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/client"
)

type fakeViewState struct {
	states []*storagepb.GetViewStateRsp
	index  int
}

func (f *fakeViewState) GetViewState(context.Context, *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	state := f.states[f.index]
	if f.index < len(f.states)-1 {
		f.index++
	}
	return state, nil
}

type catalogMetadataFake struct {
	datasets    map[string]*storagepb.Dataset
	failAt      string
	failCleanup bool
	deleteOrder []string
	// createColumns 是随 CreateDataset 一起写入的列；upsertColumns 是单独补列的次数。
	createColumns []string
	upsertColumns int
	// datasetColumns 是各数据集的列，查询索引的列按它推出（和 Storage 一样）。
	datasetColumns map[string][]*storagepb.DatasetColumn
	// buildingPolls 是 GetViewState 先回答"索引还在建"的次数。
	buildingPolls int
}

func newCatalogMetadataFake(failAt string) *catalogMetadataFake {
	return &catalogMetadataFake{
		datasets:       make(map[string]*storagepb.Dataset),
		datasetColumns: make(map[string][]*storagepb.DatasetColumn),
		failAt:         failAt,
	}
}

func (f *catalogMetadataFake) fail(action string) error {
	if f.failAt == action {
		return errors.New(action + " failed")
	}
	return nil
}

func (f *catalogMetadataFake) GetDataset(_ context.Context, req *storagepb.GetDatasetReq, _ ...client.Option) (*storagepb.GetDatasetRsp, error) {
	if err := f.fail("get_dataset"); err != nil {
		return nil, err
	}
	dataset := f.datasets[req.GetDatasetId()]
	if dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
}

func (f *catalogMetadataFake) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq, _ ...client.Option) (*storagepb.CreateDatasetRsp, error) {
	if err := f.fail("create_dataset"); err != nil {
		return nil, err
	}
	// 和 Storage 一样：数据集建好即启用，列随数据集一起写入。
	dataset := req.GetDataset()
	dataset.Status = "active"
	for _, column := range req.GetColumns() {
		f.createColumns = append(f.createColumns, column.GetColumnName())
	}
	f.datasetColumns[dataset.GetDatasetId()] = append([]*storagepb.DatasetColumn(nil), req.GetColumns()...)
	f.datasets[dataset.GetDatasetId()] = dataset
	return &storagepb.CreateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
}

func (f *catalogMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq, _ ...client.Option) (*storagepb.DeleteDatasetRsp, error) {
	f.deleteOrder = append(f.deleteOrder, "dataset")
	if f.failCleanup {
		return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "cleanup dataset failed"}}, nil
	}
	if err := f.fail("delete_dataset"); err != nil {
		return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	delete(f.datasets, req.GetDatasetId())
	return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *catalogMetadataFake) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq, _ ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.upsertColumns++
	if err := f.fail("dataset_column"); err != nil {
		return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	column := req.GetColumn()
	existing := f.datasetColumns[column.GetDatasetId()]
	found := false
	for _, current := range existing {
		found = found || current.GetColumnName() == column.GetColumnName()
	}
	if !found {
		f.datasetColumns[column.GetDatasetId()] = append(existing, column)
	}
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

// indexedColumns 返回数据集查询索引已建的列：和 Storage 一样，等于数据集的列。
func (f *catalogMetadataFake) indexedColumns(datasetID string) []*storagepb.ResultColumn {
	var out []*storagepb.ResultColumn
	for _, column := range f.datasetColumns[datasetID] {
		out = append(out, &storagepb.ResultColumn{ColumnName: column.GetColumnName(), ValueType: column.GetValueType(), DisplayName: column.GetDisplayName()})
	}
	return out
}

// catalogViewStateFake 按元数据假实现里的数据集回答索引状态：数据集不存在时是 VIEW_NOT_FOUND；
// 先按 buildingPolls 回答几次"索引还在建"，之后索引建好、已建列就是数据集的列。
type catalogViewStateFake struct {
	metadata *catalogMetadataFake
}

func (f catalogViewStateFake) GetViewState(_ context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	if err := f.metadata.fail("view_state"); err != nil {
		return nil, err
	}
	if f.metadata.datasets[req.GetDatasetId()] == nil {
		return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}, nil
	}
	if f.metadata.buildingPolls > 0 {
		f.metadata.buildingPolls--
		return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
	}
	return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, IndexId: "idx", Columns: f.metadata.indexedColumns(req.GetDatasetId())}, nil
}

func newPrepareTargetFixture(failAt string) (*catalogMetadataFake, *Catalog, domain.CollectionTask, *domain.CollectParams, storagesource.DatasetInfo, []domain.Subject) {
	metadata := newCatalogMetadataFake(failAt)
	catalog := &Catalog{Metadata: metadata, View: catalogViewStateFake{metadata: metadata}}
	rule := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample"}
	params := &domain.CollectParams{
		Provider: "moox", MarketType: "spot",
		SourceDatasetID: "source-bars", SourceFrequency: "1m", SourceSeriesTag: "venue:binance",
		TargetDatasetID: "caller-selected-target", TargetFrequency: "5m", Alignment: "epoch_utc",
	}
	source := storagesource.DatasetInfo{DataSourceID: "crypto"}
	subjects := []domain.Subject{{SubjectID: "BTC", Status: "active"}}
	return metadata, catalog, rule, params, source, subjects
}

// 新建目标数据集时列随 CreateDataset 一次写入；已有的目标数据集按同一份列定义补列，不再新建。
func TestPrepareTargetCreatesDatasetWithColumnsInOneCall(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("")

	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	want := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	require.Equal(t, want, metadata.createColumns)
	require.Zero(t, metadata.upsertColumns)

	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	require.Equal(t, want, metadata.createColumns, "已有的目标数据集不再新建")
	require.Equal(t, len(want), metadata.upsertColumns)
}

// 合成任务不另起名字：目标列的显示名、分组与类型按字段名取自采集结果的基础字段。
func TestTargetDatasetColumnsTakeNamesFromKlineOutputFields(t *testing.T) {
	for _, tradeCount := range []bool{true, false} {
		for _, column := range targetDatasetColumns(tradeCount, nil) {
			field, ok := taskresult.KlineOutputField(column.GetColumnName())
			require.True(t, ok, column.GetColumnName())
			assert.Equal(t, field.DisplayName, column.GetDisplayName())
			assert.Equal(t, field.Group, column.GetGroup())
			assert.Equal(t, field.Kind, column.GetValueType())
		}
	}
	quote := targetDatasetColumns(true, []string{"quote_volume"})
	require.Len(t, quote, 1)
	assert.Equal(t, "计价币成交额", quote[0].GetDisplayName())
	assert.Equal(t, "成交估值", quote[0].GetGroup())
}

// 查询索引的列就是目标数据集的列，准备目标时等这些列都建进索引才放行。
func TestPrepareTargetWaitsUntilDatasetColumnsAreIndexed(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("")
	metadata.buildingPolls = 2

	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	require.Zero(t, metadata.buildingPolls, "索引建好之前一直在等")
	ids := taskresult.ResultIDs(rule.TaskID)
	declared := metadata.datasetColumns[ids.DatasetID]
	require.Len(t, declared, 7)

	// 索引已建好但还缺一列时不放行。
	state := &storagepb.GetViewStateRsp{IndexId: "idx", Columns: metadata.indexedColumns(ids.DatasetID)[:6]}
	assert.False(t, targetViewColumnsReady(state, declared))
}

func TestPrepareTargetCompensatesCreatedDatasetAfterLaterFailure(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("view_state")

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.Error(t, err)
	require.Equal(t, taskresult.ResultIDs(rule.TaskID).DatasetID, params.TargetDatasetID)
	require.Empty(t, metadata.datasets)
	require.Equal(t, []string{"dataset"}, metadata.deleteOrder)
}

func TestPrepareTargetNeverDeletesExistingResourcesOnFailure(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("view_state")
	ids := taskresult.ResultIDs(rule.TaskID)
	metadata.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: rule.SpaceID, DatasetId: ids.DatasetID,
		DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: "5m", Status: "active", Alignment: params.Alignment,
		Attributes: map[string]string{
			"owner_module": "collector", "managed_by": "collector", "collector_task_id": rule.TaskID,
			"market_type": "spot", "storage_model": "wide_common_metrics", "dataset_role": "kline_resample_result",
			"source_dataset_id": params.SourceDatasetID, "source_data_source_id": source.DataSourceID,
			"source_freq": params.SourceFrequency, "source_series_tag": params.SourceSeriesTag,
			"target_freq": "5m",
		},
	}

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.Error(t, err)
	require.Empty(t, metadata.deleteOrder)
	require.Contains(t, metadata.datasets, ids.DatasetID)
}

func TestPrepareTargetMergesOriginalAndCompensationErrors(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("view_state")
	metadata.failCleanup = true

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.Error(t, err)
	require.ErrorContains(t, err, "get target View state")
	require.ErrorContains(t, err, "cleanup dataset failed")
	require.Equal(t, []string{"dataset"}, metadata.deleteOrder)
}

func TestTargetViewColumnsReadyRequiresActiveIndexAndColumns(t *testing.T) {
	declared := []*storagepb.DatasetColumn{{ColumnName: "open"}, {ColumnName: "close"}}
	built := []*storagepb.ResultColumn{{ColumnName: "open"}, {ColumnName: "close"}, {ColumnName: "volume"}}
	assert.False(t, targetViewColumnsReady(&storagepb.GetViewStateRsp{IndexId: "idx", Columns: built[:1]}, declared), "还有列没建进索引")
	assert.False(t, targetViewColumnsReady(&storagepb.GetViewStateRsp{Columns: built}, declared), "索引还没建成")
	assert.True(t, targetViewColumnsReady(&storagepb.GetViewStateRsp{IndexId: "idx", Columns: built}, declared))
}

// 查询索引还没开始建（VIEW_NOT_FOUND）、还在建（index_id 为空）都继续等，直到列建好。
func TestWaitTargetViewColumnsRetriesUntilColumnsAreBuilt(t *testing.T) {
	success := &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}
	state := &fakeViewState{states: []*storagepb.GetViewStateRsp{
		{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}},
		{RetInfo: success},
		{RetInfo: success, IndexId: "idx"},
		{RetInfo: success, IndexId: "idx", Columns: []*storagepb.ResultColumn{{ColumnName: "close"}}},
	}}
	require.NoError(t, waitTargetViewColumns(context.Background(), state, "crypto", "dataset", []*storagepb.DatasetColumn{{ColumnName: "close"}}, 2*time.Second))
	assert.Equal(t, 3, state.index)
}

func TestWaitTargetViewColumnsReturnsNotReadyWhenTimeoutExpires(t *testing.T) {
	state := &fakeViewState{states: []*storagepb.GetViewStateRsp{{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}}}
	err := waitTargetViewColumns(context.Background(), state, "crypto", "dataset", []*storagepb.DatasetColumn{{ColumnName: "close"}}, 50*time.Millisecond)
	assert.ErrorIs(t, err, ErrTargetViewNotReady)
}

func TestWaitTargetViewColumnsFailsOnOtherErrors(t *testing.T) {
	state := &fakeViewState{states: []*storagepb.GetViewStateRsp{{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_NO_PERMISSION, Msg: "无权限"}}}}
	err := waitTargetViewColumns(context.Background(), state, "crypto", "dataset", []*storagepb.DatasetColumn{{ColumnName: "close"}}, time.Second)
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTargetViewNotReady)
}

func TestValidateTargetDatasetChecksImmutableLineage(t *testing.T) {
	want := map[string]string{
		"owner_module":          "collector",
		"managed_by":            "collector",
		"collector_task_id":     "rule-5m",
		"market_type":           "spot",
		"storage_model":         "wide_common_metrics",
		"dataset_role":          "kline_resample_result",
		"source_dataset_id":     "dataset_binance_kline_1m",
		"source_data_source_id": "binance",
		"source_freq":           "1m",
		"source_series_tag":     "venue:binance",
		"target_freq":           "5m",
	}
	dataset := &storagepb.Dataset{
		DataKind:   storagepb.DataKind_DATA_KIND_TIME_SERIES,
		Freq:       "5m",
		Attributes: cloneStringMap(want),
	}
	require.NoError(t, validateTargetDataset(dataset, want, "5m"))

	for key, value := range want {
		t.Run("attribute/"+key, func(t *testing.T) {
			copy := proto.Clone(dataset).(*storagepb.Dataset)
			copy.Attributes = cloneStringMap(dataset.Attributes)
			copy.Attributes[key] = value + "-drift"
			require.ErrorContains(t, validateTargetDataset(copy, want, "5m"), "immutable lineage attribute")
		})
	}
	wrongSource := proto.Clone(dataset).(*storagepb.Dataset)
	wrongSource.DataSourceId = "binance"
	require.ErrorContains(t, validateTargetDataset(wrongSource, want, "5m"), "must not declare a single data source")
	monthly := proto.Clone(dataset).(*storagepb.Dataset)
	monthly.Freq = "1mo"
	require.ErrorContains(t, validateTargetDataset(monthly, want, "5m"), `freq is "1mo", not 5m`)
}

// 已经 Ready 的任务复核列时 storage-view 不可达（恢复、重建期间同理）：保持 Ready、只记日志；
// 还没 Ready 的任务照常报错，等下一轮准备。
func TestPrepareTargetKeepsReadyTaskWhenViewStateUnavailable(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("")
	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	metadata.failAt = "view_state"

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.ErrorContains(t, err, "view_state failed", "还没 Ready 的任务要等列建好")

	rule.PrepareState = domain.PrepareStateReady
	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	require.NotEmpty(t, metadata.datasets, "目标数据集保留")
}

// 逐级合成：源是上一级合成结果、没有数据来源时，目标沿用它记下的原始 K 线来源，因子任务才能以它为源。
func TestPrepareTargetInheritsSourceDataSourceForChainedResample(t *testing.T) {
	metadata, catalog, rule, params, _, subjects := newPrepareTargetFixture("")
	source := storagesource.DatasetInfo{Attributes: map[string]string{"dataset_role": "kline_resample_result", "source_data_source_id": "binance"}}

	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	ids := taskresult.ResultIDs(rule.TaskID)
	require.Equal(t, "binance", metadata.datasets[ids.DatasetID].GetAttributes()["source_data_source_id"])
}
