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
	// createColumns 是随 CreateDataset 一起写入的列；upsertColumns 是单独补列的次数；updateDatasets 是更新数据集的次数。
	createColumns  []string
	upsertColumns  int
	updateDatasets int
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

func (f *catalogMetadataFake) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq, _ ...client.Option) (*storagepb.UpdateDatasetRsp, error) {
	f.updateDatasets++
	if err := f.fail("update_dataset"); err != nil {
		return nil, err
	}
	dataset := proto.Clone(req.GetDataset()).(*storagepb.Dataset)
	f.datasets[dataset.GetDatasetId()] = dataset
	return &storagepb.UpdateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
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

// 逐级合成：源是上一级合成结果、没有数据来源时，目标沿用它记下的原始 K 线来源，因子任务才能以它为源。
func TestPrepareTargetInheritsSourceDataSourceForChainedResample(t *testing.T) {
	metadata, catalog, rule, params, _, subjects := newPrepareTargetFixture("")
	source := storagesource.DatasetInfo{Attributes: map[string]string{"dataset_role": "kline_resample_result", "source_data_source_id": "binance"}}

	require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
	ids := taskresult.ResultIDs(rule.TaskID)
	require.Equal(t, "binance", metadata.datasets[ids.DatasetID].GetAttributes()["source_data_source_id"])
}

// 交易时段市场（如 A 股）的重采样目标数据集必须写入 calendar 属性，防止下游策略回退到 crypto_24x7 日历；
// 连续交易市场（crypto）不写 calendar 属性；
// 已有目标数据集仅缺失 calendar 时原地 UpdateDataset 回填成功，calendar 非空且不匹配时按血缘冲突拒绝。
func TestPrepareTargetWritesCalendarAttributeForSessionMarkets(t *testing.T) {
	// 公共逻辑 taskresult.SessionAttributes 对所有已登记交易时段市场均输出对应日历。
	for _, tc := range []struct {
		spaceID      string
		wantCalendar string
		wantAlign    string
	}{
		{spaceID: "stockcn", wantCalendar: "cn_stock", wantAlign: "session"},
		{spaceID: "stockhk", wantCalendar: "hk_stock", wantAlign: "session"},
		{spaceID: "stockus", wantCalendar: "us_stock", wantAlign: "session"},
		{spaceID: "crypto", wantCalendar: "", wantAlign: ""},
	} {
		attrs, alignment, err := taskresult.SessionAttributes(tc.spaceID)
		require.NoError(t, err, tc.spaceID)
		require.Equal(t, tc.wantAlign, alignment, tc.spaceID)
		require.Equal(t, tc.wantCalendar, attrs["calendar"], tc.spaceID)
	}

	// PrepareTarget 端到端校验（测试包登记了 stockcn 与 crypto 的标的规则）。
	cases := []struct {
		spaceID      string
		wantCalendar string
		wantAlign    string
	}{
		{spaceID: "stockcn", wantCalendar: "cn_stock", wantAlign: "session"},
		{spaceID: "crypto", wantCalendar: "", wantAlign: "epoch_utc"},
	}
	for _, tc := range cases {
		t.Run(tc.spaceID, func(t *testing.T) {
			metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("")
			rule.SpaceID = tc.spaceID
			params.Alignment = tc.wantAlign
			params.MarketType = "equity"
			if tc.spaceID == "crypto" {
				params.MarketType = "spot"
			}
			source.DataSourceID = tc.spaceID

			require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
			ids := taskresult.ResultIDs(rule.TaskID)
			dataset := metadata.datasets[ids.DatasetID]
			require.NotNil(t, dataset)
			require.Equal(t, tc.wantAlign, dataset.GetAlignment())
			require.Zero(t, metadata.updateDatasets, "新建数据集时无需 UpdateDataset")
			if tc.wantCalendar == "" {
				_, exists := dataset.GetAttributes()["calendar"]
				require.False(t, exists, "连续交易市场不写 calendar 属性")
			} else {
				require.Equal(t, tc.wantCalendar, dataset.GetAttributes()["calendar"])

				// 1. 已有目标数据集仅缺失 calendar（存量历史数据集）：自动调用 UpdateDataset 原地回填成功。
				delete(dataset.Attributes, "calendar")
				require.NoError(t, catalog.PrepareTarget(context.Background(), rule, params, source, subjects))
				require.Equal(t, 1, metadata.updateDatasets, "历史缺失 calendar 应原地 UpdateDataset 回填")
				require.Equal(t, tc.wantCalendar, metadata.datasets[ids.DatasetID].GetAttributes()["calendar"])

				// 2. 已有目标数据集 calendar 非空且不匹配（真实血缘冲突）：必须拒绝且不调用 UpdateDataset。
				metadata.datasets[ids.DatasetID].Attributes["calendar"] = "hk_stock"
				err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
				require.ErrorContains(t, err, "immutable lineage attribute calendar")
				require.Equal(t, 1, metadata.updateDatasets, "真实血缘冲突不得覆盖写入")
			}
		})
	}
}
