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

type fakeViewGetter struct {
	views []*storagepb.View
	index int
}

func (f *fakeViewGetter) GetView(_ context.Context, _ *storagepb.GetViewReq, _ ...client.Option) (*storagepb.GetViewRsp, error) {
	if len(f.views) == 0 {
		return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
	}
	view := f.views[f.index]
	if f.index < len(f.views)-1 {
		f.index++
	}
	return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

type catalogMetadataFake struct {
	datasets    map[string]*storagepb.Dataset
	views       map[string]*storagepb.View
	failAt      string
	failCleanup bool
	deleteOrder []string
	// createColumns 是随 CreateDataset 一起写入的列；upsertColumns 是单独补列的次数。
	createColumns []string
	upsertColumns int
}

func newCatalogMetadataFake(failAt string) *catalogMetadataFake {
	return &catalogMetadataFake{
		datasets: make(map[string]*storagepb.Dataset),
		views:    make(map[string]*storagepb.View),
		failAt:   failAt,
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

func (f *catalogMetadataFake) GetView(_ context.Context, req *storagepb.GetViewReq, _ ...client.Option) (*storagepb.GetViewRsp, error) {
	if err := f.fail("get_view"); err != nil {
		return nil, err
	}
	view := f.views[req.GetViewId()]
	if view == nil {
		return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}, nil
	}
	return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

func (f *catalogMetadataFake) CreateView(_ context.Context, req *storagepb.CreateViewReq, _ ...client.Option) (*storagepb.CreateViewRsp, error) {
	if err := f.fail("create_view"); err != nil {
		return &storagepb.CreateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	view := req.GetView()
	// 和 Storage 一样：View 的频率取自它的数据集。
	view.Freq = f.datasets[view.GetDatasetId()].GetFreq()
	if f.failAt == "update_view" {
		view.Attributes = cloneStringMap(view.GetAttributes())
		delete(view.Attributes, "route_ready_request_id")
	}
	f.views[view.GetViewId()] = view
	return &storagepb.CreateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

func (f *catalogMetadataFake) DeleteView(_ context.Context, req *storagepb.DeleteViewReq, _ ...client.Option) (*storagepb.DeleteViewRsp, error) {
	f.deleteOrder = append(f.deleteOrder, "view")
	if f.failCleanup {
		return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "cleanup view failed"}}, nil
	}
	if err := f.fail("delete_view"); err != nil {
		return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	delete(f.views, req.GetViewId())
	return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *catalogMetadataFake) UpsertDatasetColumn(_ context.Context, _ *storagepb.UpsertDatasetColumnReq, _ ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.upsertColumns++
	if err := f.fail("dataset_column"); err != nil {
		return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *catalogMetadataFake) UpdateView(_ context.Context, req *storagepb.UpdateViewReq, _ ...client.Option) (*storagepb.UpdateViewRsp, error) {
	if err := f.fail("update_view"); err != nil {
		return &storagepb.UpdateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	f.views[req.GetView().GetViewId()] = req.GetView()
	return &storagepb.UpdateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: req.GetView()}, nil
}

func (f *catalogMetadataFake) UpsertViewColumn(_ context.Context, req *storagepb.UpsertViewColumnReq, _ ...client.Option) (*storagepb.UpsertViewColumnRsp, error) {
	if err := f.fail("view_column"); err != nil {
		return &storagepb.UpsertViewColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: err.Error()}}, nil
	}
	view := f.views[req.GetColumn().GetViewId()]
	if view != nil {
		view.ActiveColumns = append(view.ActiveColumns, req.GetColumn())
		view.Columns = append(view.Columns, req.GetColumn())
		view.ActiveIndexId = "idx"
		view.Status = "active"
	}
	return &storagepb.UpsertViewColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

type catalogViewSyncFake struct {
	metadata *catalogMetadataFake
}

func (f catalogViewSyncFake) WaitViewSyncPoint(_ context.Context, _ *storagepb.WaitViewSyncPointReq) (*storagepb.WaitViewSyncPointRsp, error) {
	if err := f.metadata.fail("view_sync"); err != nil {
		return nil, err
	}
	return &storagepb.WaitViewSyncPointRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Ready: true}, nil
}

func newPrepareTargetFixture(failAt string) (*catalogMetadataFake, *Catalog, domain.CollectionTask, *domain.CollectParams, storagesource.DatasetInfo, []domain.Subject) {
	metadata := newCatalogMetadataFake(failAt)
	catalog := &Catalog{Metadata: metadata, Auth: &storagepb.AuthInfo{AppId: "collector"}, ViewSync: catalogViewSyncFake{metadata: metadata}}
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

func TestPrepareTargetCompensatesCreatedDatasetWhenViewCreationFails(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("create_view")

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.Error(t, err)
	require.Equal(t, taskresult.ResultIDs(rule.SpaceID, rule.TaskID).DatasetID, params.TargetDatasetID)
	require.Empty(t, metadata.datasets)
	require.Empty(t, metadata.views)
	require.Equal(t, []string{"dataset"}, metadata.deleteOrder)
}

func TestPrepareTargetCompensatesNewResourcesInReverseOrderAfterLaterFailure(t *testing.T) {
	for _, testCase := range []struct {
		failAt      string
		deleteOrder []string
	}{
		{failAt: "update_view", deleteOrder: []string{"view", "dataset"}},
		{failAt: "view_column", deleteOrder: []string{"view", "dataset"}},
		{failAt: "view_sync", deleteOrder: []string{"view", "dataset"}},
	} {
		t.Run(testCase.failAt, func(t *testing.T) {
			metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture(testCase.failAt)

			err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
			require.Error(t, err)
			require.Empty(t, metadata.datasets)
			require.Empty(t, metadata.views)
			require.Equal(t, testCase.deleteOrder, metadata.deleteOrder)
		})
	}
}

func TestPrepareTargetNeverDeletesExistingResourcesOnFailure(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("view_column")
	ids := taskresult.ResultIDs(rule.SpaceID, rule.TaskID)
	metadata.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: rule.SpaceID, DatasetId: ids.DatasetID,
		DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freq: "5m", Status: "active",
		Attributes: map[string]string{
			"owner_module": "collector", "managed_by": "collector", "collector_task_id": rule.TaskID,
			"market_type": "spot", "storage_model": "wide_common_metrics", "dataset_role": "kline_resample_result",
			"source_dataset_id": params.SourceDatasetID, "source_data_source_id": source.DataSourceID,
			"source_freq": params.SourceFrequency, "source_series_tag": params.SourceSeriesTag,
			"target_freq": "5m", "alignment": params.Alignment,
		},
	}
	metadata.views[ids.ViewID] = &storagepb.View{
		SpaceId: rule.SpaceID, ViewId: ids.ViewID, DatasetId: ids.DatasetID, Engine: "duckdb",
		Freq: "5m", GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}, Status: "active",
		Attributes: map[string]string{"owner_module": "collector", "managed_by": "collector", "collector_task_id": rule.TaskID},
	}

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.Error(t, err)
	require.Empty(t, metadata.deleteOrder)
	require.Contains(t, metadata.datasets, ids.DatasetID)
	require.Contains(t, metadata.views, ids.ViewID)
}

func TestPrepareTargetMergesOriginalAndCompensationErrors(t *testing.T) {
	metadata, catalog, rule, params, source, subjects := newPrepareTargetFixture("view_sync")
	metadata.failCleanup = true

	err := catalog.PrepareTarget(context.Background(), rule, params, source, subjects)
	require.Error(t, err)
	require.ErrorContains(t, err, "wait target View sync point")
	require.ErrorContains(t, err, "cleanup view failed")
	require.ErrorContains(t, err, "cleanup dataset failed")
	require.Equal(t, []string{"view", "dataset"}, metadata.deleteOrder)
}

func TestTargetViewRevisionReadyRequiresActiveIndexAndRevision(t *testing.T) {
	declared := []*storagepb.ViewColumn{{ColumnName: "open"}, {ColumnName: "close"}}
	built := []*storagepb.ViewColumn{{ColumnName: "open"}, {ColumnName: "close"}, {ColumnName: "volume"}}
	assert.False(t, targetViewColumnsReady(&storagepb.View{Status: "active", ActiveColumns: declared[:1], ActiveIndexId: "idx"}, declared), "还有列没建进索引")
	assert.False(t, targetViewColumnsReady(&storagepb.View{Status: "active", ActiveColumns: built}, declared), "索引还没建成")
	assert.False(t, targetViewColumnsReady(&storagepb.View{Status: "building", ActiveColumns: built, ActiveIndexId: "idx"}, declared))
	assert.True(t, targetViewColumnsReady(&storagepb.View{Status: "active", ActiveColumns: built, ActiveIndexId: "idx"}, declared))
}

func TestWaitTargetViewRevisionPollsUntilDesiredIndexIsActive(t *testing.T) {
	getter := &fakeViewGetter{views: []*storagepb.View{
		{Status: "active", ActiveIndexId: "idx"},
		{Status: "active", ActiveIndexId: "idx", ActiveColumns: []*storagepb.ViewColumn{{ColumnName: "close"}}},
	}}
	require.NoError(t, waitTargetViewColumns(context.Background(), getter, nil, "crypto", "view", []*storagepb.ViewColumn{{ColumnName: "close"}}, 500*time.Millisecond))
	assert.Equal(t, 1, getter.index)
}

func TestWaitTargetViewRevisionReturnsNotReadyWhenTimeoutExpires(t *testing.T) {
	getter := &fakeViewGetter{views: []*storagepb.View{{Status: "active", ActiveIndexId: "idx"}}}
	err := waitTargetViewColumns(context.Background(), getter, nil, "crypto", "view", []*storagepb.ViewColumn{{ColumnName: "close"}}, 20*time.Millisecond)
	assert.ErrorIs(t, err, ErrTargetViewNotReady)
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
		"alignment":             "epoch_utc",
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

func TestPrepareTargetViewContractUsesDatasetFrequency(t *testing.T) {
	view := &storagepb.View{DatasetId: "dataset_spot_kline_derived_5m", Freq: "5m", Engine: "duckdb", GrainKeys: []string{"subject_id", "freq", "data_time", "series_tag"}}
	require.NoError(t, validateTargetView(view, domain.CollectionTask{}, &domain.CollectParams{TargetDatasetID: "dataset_spot_kline_derived_5m"}, "5m"))
}
