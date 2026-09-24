package taskresult

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestResultIDsUseReadableTaskSlug(t *testing.T) {
	ids := ResultIDs("crypto", "builtin-binance-spot-kline-1m")
	require.Equal(t, "dataset_binance_spot_kline_1m", ids.DatasetID)
	require.Equal(t, "view_binance_spot_kline_1m", ids.ViewID)
	require.Equal(t, ids, ResultIDs("stockcn", "builtin-binance-spot-kline-1m"))

	symbols := ResultIDs("crypto", "builtin-binance-swap-symbols-1h")
	require.Equal(t, "dataset_binance_swap_symbols", symbols.DatasetID)
	require.Equal(t, "view_binance_swap_symbols", symbols.ViewID)

	fiveMinute := ResultIDs("crypto", "task-5m")
	require.Equal(t, "dataset_task_5m", fiveMinute.DatasetID)
	require.Equal(t, "view_task_5m", fiveMinute.ViewID)
	require.NotEqual(t, fiveMinute, ResultIDs("crypto", "task-1h"))
}

func TestResultIDsUseCanonicalCryptoHourlyDatasets(t *testing.T) {
	spot := ResultIDs("crypto", "builtin-binance-spot-kline-1h")
	require.Equal(t, "dataset_spot_kline_1h", spot.DatasetID)
	require.Equal(t, "view_crypto_spot_kline_1h", spot.ViewID)

	swap := ResultIDs("crypto", "builtin-binance-swap-kline-1h")
	require.Equal(t, "dataset_perpetual_kline_1h", swap.DatasetID)
	require.Equal(t, "view_crypto_swap_kline_1h", swap.ViewID)
}

func TestResultDisplayNameUsesShortChinese(t *testing.T) {
	require.Equal(t, "合约小时K线", resultDisplayName(Config{Name: "Binance 合约 K 线 1H"}, "builtin-binance-swap-kline-1h"))
	require.Equal(t, "采集结果", resultDisplayName(Config{Name: "too long english name"}, "custom-task"))
	require.True(t, isChineseDisplayName("现货分钟K线"))
	require.False(t, isChineseDisplayName("Binance 现货 K 线 1m"))
}

func TestIsLegacyHashedIDs(t *testing.T) {
	require.True(t, IsLegacyHashedIDs(IDs{DatasetID: "dataset_collector_df4b3afb3ff5547c", ViewID: "view_collector_df4b3afb3ff5547c"}))
	require.False(t, IsLegacyHashedIDs(ResultIDs("crypto", "builtin-binance-spot-kline-1m")))
}

type resultMetadataFake struct {
	datasets       map[string]*storagepb.Dataset
	views          map[string]*storagepb.View
	createDatasets int
	createViews    int
	failView       bool
}

func newResultMetadataFake() *resultMetadataFake {
	return &resultMetadataFake{datasets: map[string]*storagepb.Dataset{}, views: map[string]*storagepb.View{}}
}

func resultOK() *storagepb.RetInfo { return &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS} }
func resultNotFound() *storagepb.RetInfo {
	return &storagepb.RetInfo{Code: storagepb.ErrorCode_NOT_FOUND}
}

func TestResultDeleteAcceptedIsIdempotent(t *testing.T) {
	for _, code := range []storagepb.ErrorCode{
		storagepb.ErrorCode_SUCCESS,
		storagepb.ErrorCode_DATASET_NOT_FOUND,
		storagepb.ErrorCode_VIEW_NOT_FOUND,
		storagepb.ErrorCode_NOT_FOUND,
	} {
		require.True(t, resultDeleteAccepted(&storagepb.RetInfo{Code: code}), "code=%s", code)
	}
	require.False(t, resultDeleteAccepted(&storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR}))
}

func (f *resultMetadataFake) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	dataset := f.datasets[req.GetDatasetId()]
	if dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: resultNotFound()}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: resultOK(), Dataset: dataset}, nil
}

func (f *resultMetadataFake) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	f.createDatasets++
	f.datasets[req.GetDataset().GetDatasetId()] = req.GetDataset()
	return &storagepb.CreateDatasetRsp{RetInfo: resultOK(), Dataset: req.GetDataset()}, nil
}

func (f *resultMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	delete(f.datasets, req.GetDatasetId())
	return &storagepb.DeleteDatasetRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) GetView(_ context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	view := f.views[req.GetViewId()]
	if view == nil {
		return &storagepb.GetViewRsp{RetInfo: resultNotFound()}, nil
	}
	return &storagepb.GetViewRsp{RetInfo: resultOK(), View: view}, nil
}

func (f *resultMetadataFake) CreateView(_ context.Context, req *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	f.createViews++
	if f.failView {
		return &storagepb.CreateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "view create failed"}}, nil
	}
	f.views[req.GetView().GetViewId()] = req.GetView()
	return &storagepb.CreateViewRsp{RetInfo: resultOK(), View: req.GetView()}, nil
}

func (f *resultMetadataFake) DeleteView(_ context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	delete(f.views, req.GetViewId())
	return &storagepb.DeleteViewRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return &storagepb.UpsertViewColumnRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return &storagepb.CheckDatasetActivationRsp{RetInfo: resultOK(), Ready: true}, nil
}

func (f *resultMetadataFake) ActivateDataset(_ context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	dataset := f.datasets[req.GetDatasetId()]
	if dataset != nil {
		dataset.Status = "active"
	}
	return &storagepb.ActivateDatasetRsp{RetInfo: resultOK(), Dataset: dataset}, nil
}

func TestEnsureCreatesOneExclusiveResultAndIsIdempotent(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	config := Config{DataNodeID: "node-1", DataSourceID: "binance", Frequency: "1h"}
	first, err := manager.Ensure(context.Background(), "crypto", "task-1", "kline", "spot", config)
	require.NoError(t, err)
	second, err := manager.Ensure(context.Background(), "crypto", "task-1", "kline", "spot", config)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, fake.createDatasets)
	require.Equal(t, 1, fake.createViews)
	require.Equal(t, "task-1", fake.datasets[first.DatasetID].GetAttributes()["collector_task_id"])
	require.Equal(t, "task-1", fake.views[first.ViewID].GetAttributes()["collector_task_id"])
}

func TestEnsureDeclaresAllCollectionFrequencies(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	ids, err := manager.Ensure(context.Background(), "crypto", "task-multi-frequency", "kline", "spot", Config{
		DataNodeID: "node-1", DataSourceID: "binance", Frequency: "1m", Frequencies: []string{"1m", "5m", "1m"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"1m", "5m"}, fake.datasets[ids.DatasetID].GetFreqs())
	require.Equal(t, `{"freq":"1m"}`, fake.views[ids.ViewID].GetFilterJson())
}

func TestNormalizeKeepDurationAcceptsDaysAndWeeks(t *testing.T) {
	require.Equal(t, "720h0m0s", mustNormalizeKeepDuration(t, "30d"))
	require.Equal(t, "168h0m0s", mustNormalizeKeepDuration(t, "1w"))
	require.Equal(t, "0", mustNormalizeKeepDuration(t, "0"))
	_, err := normalizeKeepDuration("invalid")
	require.Error(t, err)
}

func mustNormalizeKeepDuration(t *testing.T, raw string) string {
	t.Helper()
	value, err := normalizeKeepDuration(raw)
	require.NoError(t, err)
	return value
}

func TestEnsureCompensatesDatasetWhenViewCreationFails(t *testing.T) {
	fake := newResultMetadataFake()
	fake.failView = true
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	_, err := manager.Ensure(context.Background(), "crypto", "task-view-failure", "kline", "spot", Config{DataNodeID: "node-1", DataSourceID: "binance", Frequency: "1h"})
	require.Error(t, err)
	ids := ResultIDs("crypto", "task-view-failure")
	_, datasetExists := fake.datasets[ids.DatasetID]
	require.False(t, datasetExists)
	require.Empty(t, fake.views)
}

func TestInspectReportsTaskOwnedReadyResultAndLastDataTime(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "task-inspect")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto",
		Attributes: map[string]string{
			"owner_module":      "collector",
			"collector_task_id": "task-inspect",
		},
		Status: "active",
	}
	fake.views[ids.ViewID] = &storagepb.View{
		ViewId:     ids.ViewID,
		DatasetId:  ids.DatasetID,
		Status:     "active",
		IndexedTo:  "2026-09-20T04:00:00Z",
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

	inspection, err := manager.Inspect(context.Background(), "crypto", "task-inspect")
	require.NoError(t, err)
	require.Equal(t, ids, inspection.IDs)
	require.Equal(t, "ready", inspection.Status)
	require.Equal(t, "2026-09-20T04:00:00Z", inspection.LastDataTime)
	require.Equal(t, ids.ViewID, inspection.View.GetViewId())
}

func TestInspectReportsPendingWhenResultMetadataIsIncomplete(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "task-pending")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-pending"},
		Status:     "draft",
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

	inspection, err := manager.Inspect(context.Background(), "crypto", "task-pending")
	require.NoError(t, err)
	require.Equal(t, "pending", inspection.Status)
	require.Empty(t, inspection.LastDataTime)
	require.Nil(t, inspection.View)
}

func TestInspectRejectsResultOwnedByAnotherTask(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "task-inspect")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "other-task"},
		Status:     "active",
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

	_, err := manager.Inspect(context.Background(), "crypto", "task-inspect")
	require.ErrorContains(t, err, "owned by another task")
}

func TestOwnedByTaskDoesNotAcceptConflictingLegacyOwner(t *testing.T) {
	require.True(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-b"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "factor", "collector_task_id": "task-a"}, "task-a"))
}

func TestOwnedByTaskAdoptsCatalogDataset(t *testing.T) {
	require.True(t, ownedByTask(map[string]string{"market_type": "spot"}, "builtin-binance-spot-kline-1m"))
	require.True(t, ownedByTask(nil, "builtin-binance-spot-kline-1m"))
}

func TestEnsureAdoptsCatalogDatasetAndCreatesTaskView(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "builtin-binance-spot-kline-1m")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Name: "加密现货一分钟行情", Status: "active",
		KeepDuration: "720h0m0s", Attributes: map[string]string{"market_type": "spot"},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	got, err := manager.Ensure(context.Background(), "crypto", "builtin-binance-spot-kline-1m", "kline", "spot", Config{
		DataNodeID: "node-1", Name: "Binance 现货 K 线 1m", DataSourceID: "binance", Frequency: "1m",
	})
	require.NoError(t, err)
	require.Equal(t, ids, got)
	require.Equal(t, 0, fake.createDatasets)
	require.Equal(t, 1, fake.createViews)
	require.Equal(t, "现货分钟K线", fake.views[ids.ViewID].GetName())
	require.Equal(t, "720h0m0s", fake.views[ids.ViewID].GetKeepDuration())
}

type restoreFailCleaner struct{}

func (restoreFailCleaner) DeleteDatasetRows(context.Context, *storagepb.PrimaryDeleteDatasetRowsReq) (*storagepb.PrimaryDeleteDatasetRowsRsp, error) {
	return &storagepb.PrimaryDeleteDatasetRowsRsp{RetInfo: resultOK()}, nil
}

func (restoreFailCleaner) RestoreDatasetRows(context.Context, *storagepb.PrimaryRestoreDatasetRowsReq) (*storagepb.PrimaryRestoreDatasetRowsRsp, error) {
	return &storagepb.PrimaryRestoreDatasetRowsRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "dataset is not a Collector task result"}}, nil
}

func TestEnsureSkipsRestoreForCatalogDataset(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "builtin-binance-swap-kline-1m")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes: map[string]string{"market_type": "swap"},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	manager.cleaner = restoreFailCleaner{}
	got, err := manager.Ensure(context.Background(), "crypto", "builtin-binance-swap-kline-1m", "kline", "swap", Config{
		DataNodeID: "node-1", DataSourceID: "binance", Frequency: "1m",
	})
	require.NoError(t, err)
	require.Equal(t, ids, got)
}

func TestDeleteLeavesCatalogDataset(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "builtin-binance-spot-kline-1m")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes: map[string]string{"market_type": "spot"},
	}
	fake.views[ids.ViewID] = &storagepb.View{ViewId: ids.ViewID, DatasetId: ids.DatasetID, Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "builtin-binance-spot-kline-1m"}}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	require.NoError(t, manager.Delete(context.Background(), "crypto", ids))
	_, datasetExists := fake.datasets[ids.DatasetID]
	require.True(t, datasetExists)
	_, viewExists := fake.views[ids.ViewID]
	require.False(t, viewExists)
}
