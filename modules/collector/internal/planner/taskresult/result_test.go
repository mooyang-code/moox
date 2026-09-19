package taskresult

import (
	"context"
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

func TestResultIDsWithFrequencyAreTaskExclusiveAndFrequencyQualified(t *testing.T) {
	ids := ResultIDsWithFrequency("crypto", "task-5m", "5m")
	require.Equal(t, "dataset_collector_"+hashForTest("crypto", "task-5m")+"_5m", ids.DatasetID)
	require.Equal(t, "view_collector_"+hashForTest("crypto", "task-5m")+"_5m", ids.ViewID)
	require.NotEqual(t, ids, ResultIDsWithFrequency("crypto", "task-1h", "1h"))
}

func hashForTest(spaceID, taskID string) string {
	return resultIDs(spaceID, taskID).DatasetID[len("dataset_collector_"):]
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

func TestOwnedByTaskDoesNotAcceptConflictingLegacyOwner(t *testing.T) {
	require.True(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-b"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "collector", "collector_task_id": "task-b", "resample_task_id": "task-a"}, "task-a"))
	require.False(t, ownedByTask(map[string]string{"owner_module": "factor", "collector_task_id": "task-a"}, "task-a"))
}
