package taskresult

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestResultIDsUseReadableTaskSlug(t *testing.T) {
	ids := ResultIDs("crypto", "builtin-binance-spot-kline-1m")
	require.Equal(t, "dataset_builtin_binance_spot_kline_1m", ids.DatasetID)
	require.Equal(t, "view_builtin_binance_spot_kline_1m", ids.ViewID)
	require.Equal(t, ids, ResultIDs("stockcn", "builtin-binance-spot-kline-1m"))

	custom := ResultIDs("crypto", "builtin-custom-kline-1m")
	require.Equal(t, "dataset_builtin_custom_kline_1m", custom.DatasetID)
	require.Equal(t, "view_builtin_custom_kline_1m", custom.ViewID)

	fiveMinute := ResultIDs("crypto", "task-5m")
	require.Equal(t, "dataset_task_5m", fiveMinute.DatasetID)
	require.Equal(t, "view_task_5m", fiveMinute.ViewID)
	require.NotEqual(t, fiveMinute, ResultIDs("crypto", "task-1h"))
}

func TestResultIDsUseTagTaskTypeFrequencyViewIdentity(t *testing.T) {
	ids := ResultIDsForTask("crypto", "task-one", "binance_spot", "kline", "1m")
	require.Equal(t, "dataset_task_one", ids.DatasetID)
	require.Equal(t, "view_task_one_kline_1m", ids.ViewID)
	require.NotEqual(t, ids.ViewID, ResultIDsForTask("crypto", "task-two", "binance_spot", "kline", "1m").ViewID)
	require.NotEqual(t, ids.ViewID, ResultIDsForTask("crypto", "task-two", "binance_spot", "kline", "5m").ViewID)
	require.Equal(t, "view_task_one_kline_1m", ResultIDsForTask("crypto", "task-one", "", "kline", "1m").ViewID)
}

func TestBuiltinResultIDsRemainTaskOwnedAndDoNotReuseCatalogIDs(t *testing.T) {
	spot := ResultIDs("crypto", "builtin-binance-spot-kline-1h")
	require.Equal(t, "dataset_builtin_binance_spot_kline_1h", spot.DatasetID)
	require.Equal(t, "view_builtin_binance_spot_kline_1h", spot.ViewID)
	require.NotEqual(t, "dataset_spot_kline_1h", spot.DatasetID)

	swap := ResultIDs("crypto", "builtin-binance-swap-kline-1h")
	require.Equal(t, "dataset_builtin_binance_swap_kline_1h", swap.DatasetID)
	require.Equal(t, "view_builtin_binance_swap_kline_1h", swap.ViewID)
	require.NotEqual(t, "dataset_perpetual_kline_1h", swap.DatasetID)
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
	columnNames    []string
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

func (f *resultMetadataFake) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.datasets[req.GetDataset().GetDatasetId()] = req.GetDataset()
	return &storagepb.UpdateDatasetRsp{RetInfo: resultOK(), Dataset: req.GetDataset()}, nil
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

func (f *resultMetadataFake) UpdateView(_ context.Context, req *storagepb.UpdateViewReq) (*storagepb.UpdateViewRsp, error) {
	view := req.GetView()
	f.views[view.GetViewId()] = view
	return &storagepb.UpdateViewRsp{RetInfo: resultOK(), View: view}, nil
}

func (f *resultMetadataFake) DeleteView(_ context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	delete(f.views, req.GetViewId())
	return &storagepb.DeleteViewRsp{RetInfo: resultOK()}, nil
}

func (f *resultMetadataFake) UpsertDatasetColumn(_ context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.columnNames = append(f.columnNames, req.GetColumn().GetColumnName())
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
	config := Config{DataNodeID: "node-1", Frequency: "1h", SubjectTags: []string{"binance_spot"}, OutputFields: []string{"close"}}
	first, err := manager.Ensure(context.Background(), "crypto", "task-1", "kline", "spot", config)
	require.NoError(t, err)
	second, err := manager.Ensure(context.Background(), "crypto", "task-1", "kline", "spot", config)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, fake.createDatasets)
	require.Equal(t, 1, fake.createViews)
	require.Equal(t, "collector", fake.datasets[first.DatasetID].GetAttributes()["owner_module"])
	require.Equal(t, "raw_collection", fake.datasets[first.DatasetID].GetAttributes()["dataset_role"])
	require.Equal(t, "task-1", fake.datasets[first.DatasetID].GetAttributes()["collector_task_id"])
	require.Equal(t, "task-1", fake.views[first.ViewID].GetAttributes()["collector_task_id"])
	require.Equal(t, []string{"binance_spot"}, fake.datasets[first.DatasetID].GetSubjectTags())
	require.Equal(t, []string{"close", "close"}, fake.columnNames)
	require.Equal(t, "view_task_1_kline_1h", first.ViewID)
}

func TestEnsureOutputFieldsReplacesExplicitViewProjection(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "task-projection")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-projection"},
	}
	fake.views[ids.ViewID] = &storagepb.View{
		SpaceId: "crypto", ViewId: ids.ViewID, Name: "任务结果", DatasetId: ids.DatasetID, Status: "active",
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-projection", "moox.columns_explicit": "true"},
		Columns: []*storagepb.ViewColumn{
			{ColumnName: ids.DatasetID + ".close", OriginId: ids.DatasetID + ".close"},
			{ColumnName: ids.DatasetID + ".provider_id", OriginId: ids.DatasetID + ".provider_id"},
		},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	require.NoError(t, manager.EnsureOutputFields(context.Background(), "crypto", "task-projection", ids.DatasetID, ids.ViewID, "kline", []string{"open", "close"}))

	got := fake.views[ids.ViewID].GetColumns()
	require.Len(t, got, 2)
	require.Equal(t, ids.DatasetID+".close", got[0].GetColumnName())
	require.Equal(t, ids.DatasetID+".open", got[1].GetColumnName())
	for _, column := range got {
		require.NotContains(t, column.GetColumnName(), "provider_id")
	}
}

func TestEnsurePreservesExistingSubjectTagsWhenConfigOmitsTags(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "task-existing-tags")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes:  map[string]string{"owner_module": "collector", "collector_task_id": "task-existing-tags"},
		SubjectTags: []string{"binance_spot"},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

	_, err := manager.Ensure(context.Background(), "crypto", "task-existing-tags", "kline", "spot", Config{
		DataNodeID: "node-1", Frequency: "1h",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"binance_spot"}, fake.datasets[ids.DatasetID].GetSubjectTags())
}

func TestEnsureUpdatesExistingSubjectTagsWithRequestedScope(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "task-update-tags")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
		Attributes:  map[string]string{"owner_module": "collector", "collector_task_id": "task-update-tags"},
		SubjectTags: []string{"old_tag"},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

	_, err := manager.Ensure(context.Background(), "crypto", "task-update-tags", "kline", "spot", Config{
		DataNodeID: "node-1", Frequency: "1h", SubjectTags: []string{"new_tag"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"new_tag"}, fake.datasets[ids.DatasetID].GetSubjectTags())
}

func TestEnsureDeclaresAllCollectionFrequencies(t *testing.T) {
	fake := newResultMetadataFake()
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	ids, err := manager.Ensure(context.Background(), "crypto", "task-multi-frequency", "kline", "spot", Config{
		DataNodeID: "node-1", Frequency: "1m", Frequencies: []string{"1m", "5m", "1m"},
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
	_, err := manager.Ensure(context.Background(), "crypto", "task-view-failure", "kline", "spot", Config{DataNodeID: "node-1", Frequency: "1h"})
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
		SpaceId: "crypto", DatasetId: ids.DatasetID,
		Attributes: map[string]string{
			"owner_module":      "collector",
			"collector_task_id": "task-inspect",
		},
		Status: "active",
	}
	fake.views[ids.ViewID] = &storagepb.View{
		SpaceId:    "crypto",
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
		SpaceId: "crypto", DatasetId: ids.DatasetID,
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
		SpaceId: "crypto", DatasetId: ids.DatasetID,
		Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "other-task"},
		Status:     "active",
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

	_, err := manager.Inspect(context.Background(), "crypto", "task-inspect")
	require.ErrorContains(t, err, "owned by another task")
	require.ErrorIs(t, err, ErrResultContract)
}

func TestInspectIDsRejectsMetadataIdentityMismatch(t *testing.T) {
	tests := []struct {
		name       string
		dataset    *storagepb.Dataset
		view       *storagepb.View
		wantObject string
	}{
		{
			name: "dataset space mismatch",
			dataset: &storagepb.Dataset{
				SpaceId: "other-space", DatasetId: "requested-dataset",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
			},
			wantObject: "dataset",
		},
		{
			name: "dataset id mismatch",
			dataset: &storagepb.Dataset{
				SpaceId: "crypto", DatasetId: "other-dataset",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
			},
			wantObject: "dataset",
		},
		{
			name: "view space mismatch",
			dataset: &storagepb.Dataset{
				SpaceId: "crypto", DatasetId: "requested-dataset",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
			},
			view: &storagepb.View{
				SpaceId: "other-space", ViewId: "requested-view", DatasetId: "requested-dataset",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
			},
			wantObject: "view",
		},
		{
			name: "view id mismatch",
			dataset: &storagepb.Dataset{
				SpaceId: "crypto", DatasetId: "requested-dataset",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
			},
			view: &storagepb.View{
				SpaceId: "crypto", ViewId: "other-view", DatasetId: "requested-dataset",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": "task-inspect"},
			},
			wantObject: "view",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := newResultMetadataFake()
			ids := IDs{DatasetID: "requested-dataset", ViewID: "requested-view"}
			fake.datasets[ids.DatasetID] = tt.dataset
			if tt.view != nil {
				fake.views[ids.ViewID] = tt.view
			}
			manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})

			_, err := manager.InspectIDs(context.Background(), "crypto", "task-inspect", ids)
			require.ErrorIs(t, err, ErrResultContract)
			require.ErrorContains(t, err, tt.wantObject+" identity does not match requested")
		})
	}
}

func TestOwnedByTaskDoesNotAcceptConflictingLegacyOwner(t *testing.T) {
	require.True(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-b"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "factor", "collector_task_id": "task-a"}, "task-a"))
}

func TestOwnedByTaskRejectsUnownedCatalogDataset(t *testing.T) {
	require.False(t, ownedByTask(map[string]string{"market_type": "spot"}, "builtin-binance-spot-kline-1m"))
	require.False(t, ownedByTask(nil, "builtin-binance-spot-kline-1m"))
}

func TestEnsureRejectsUnownedDatasetCollision(t *testing.T) {
	fake := newResultMetadataFake()
	ids := ResultIDs("crypto", "builtin-binance-spot-kline-1m")
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{
		SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active", Attributes: map[string]string{"market_type": "spot"},
	}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	_, err := manager.Ensure(context.Background(), "crypto", "builtin-binance-spot-kline-1m", "kline", "spot", Config{
		DataNodeID: "node-1", Frequency: "1m",
	})
	require.ErrorContains(t, err, "owned by another task")
}

func TestDeleteRemovesTaskOwnedDatasetAndView(t *testing.T) {
	fake := newResultMetadataFake()
	taskID := "builtin-binance-spot-kline-1m"
	ids := ResultIDs("crypto", taskID)
	attrs := map[string]string{"owner_module": "collector", "collector_task_id": taskID}
	fake.datasets[ids.DatasetID] = &storagepb.Dataset{SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active", Attributes: attrs}
	fake.views[ids.ViewID] = &storagepb.View{ViewId: ids.ViewID, DatasetId: ids.DatasetID, Attributes: attrs}
	manager := NewManagerWithAPI(fake, &storagepb.AuthInfo{AppId: "collector"})
	require.NoError(t, manager.Delete(context.Background(), "crypto", ids))
	_, datasetExists := fake.datasets[ids.DatasetID]
	require.False(t, datasetExists)
	_, viewExists := fake.views[ids.ViewID]
	require.False(t, viewExists)
}
