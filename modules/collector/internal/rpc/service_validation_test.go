package rpc

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestValidateResampleBackfillWindowRejectsOpenOrExpiredSourceWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	request := domain.ResampleBackfillRequest{RequestID: "r1", Start: now.Add(-2 * time.Hour), End: now.Add(-time.Hour)}
	require.ErrorContains(t, validateResampleBackfillWindow(request, time.Hour, 10*time.Second, "1h", now), "older than source Dataset retention")

	request.Start = now.Add(-2 * time.Hour)
	request.End = now.Add(time.Hour)
	require.ErrorContains(t, validateResampleBackfillWindow(request, time.Hour, 0, "", now), "closed bucket")
}

func TestValidateResampleBackfillWindowAcceptsClosedRetainedWindow(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	request := domain.ResampleBackfillRequest{RequestID: "r1", Start: now.Add(-3 * time.Hour), End: now.Add(-time.Hour)}
	require.NoError(t, validateResampleBackfillWindow(request, time.Hour, 10*time.Second, "24h", now))
}

type validationDatasetSource map[string]storagesource.DatasetInfo

func (s validationDatasetSource) GetDataset(_ context.Context, _ string, datasetID string) (storagesource.DatasetInfo, error) {
	info, ok := s[datasetID]
	if !ok {
		return storagesource.DatasetInfo{}, fmt.Errorf("missing dataset %s", datasetID)
	}
	return info, nil
}

func (s validationDatasetSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	return []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}}, nil
}

type taskResultMetadataFake struct {
	datasets          map[string]*storagepb.Dataset
	views             map[string]*storagepb.View
	deleteDatasets    int
	deleteViews       int
	failDeleteDataset bool
}

func newTaskResultMetadataFake(ids taskresult.IDs, taskID string) *taskResultMetadataFake {
	return &taskResultMetadataFake{
		datasets: map[string]*storagepb.Dataset{
			ids.DatasetID: {
				SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": taskID},
			},
		},
		views: map[string]*storagepb.View{
			ids.ViewID: {
				SpaceId: "crypto", ViewId: ids.ViewID, DatasetId: ids.DatasetID, Status: "active",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": taskID},
			},
		},
	}
}

func (f *taskResultMetadataFake) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	dataset := f.datasets[req.GetDatasetId()]
	if dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
}

func (f *taskResultMetadataFake) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	dataset := req.GetDataset()
	f.datasets[dataset.GetDatasetId()] = dataset
	return &storagepb.CreateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
}

func (f *taskResultMetadataFake) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.datasets[req.GetDataset().GetDatasetId()] = req.GetDataset()
	return &storagepb.UpdateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: req.GetDataset()}, nil
}

func (f *taskResultMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	f.deleteDatasets++
	if f.failDeleteDataset {
		return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "dataset delete failed"}}, nil
	}
	if _, ok := f.datasets[req.GetDatasetId()]; !ok {
		return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	delete(f.datasets, req.GetDatasetId())
	return &storagepb.DeleteDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *taskResultMetadataFake) GetView(_ context.Context, req *storagepb.GetViewReq) (*storagepb.GetViewRsp, error) {
	view := f.views[req.GetViewId()]
	if view == nil {
		return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}, nil
	}
	return &storagepb.GetViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

func (f *taskResultMetadataFake) CreateView(_ context.Context, req *storagepb.CreateViewReq) (*storagepb.CreateViewRsp, error) {
	view := req.GetView()
	f.views[view.GetViewId()] = view
	return &storagepb.CreateViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, View: view}, nil
}

func (f *taskResultMetadataFake) DeleteView(_ context.Context, req *storagepb.DeleteViewReq) (*storagepb.DeleteViewRsp, error) {
	f.deleteViews++
	if _, ok := f.views[req.GetViewId()]; !ok {
		return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}, nil
	}
	delete(f.views, req.GetViewId())
	return &storagepb.DeleteViewRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *taskResultMetadataFake) UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *taskResultMetadataFake) UpsertViewColumn(context.Context, *storagepb.UpsertViewColumnReq) (*storagepb.UpsertViewColumnRsp, error) {
	return &storagepb.UpsertViewColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *taskResultMetadataFake) CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return &storagepb.CheckDatasetActivationRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Ready: true}, nil
}

func (f *taskResultMetadataFake) ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return &storagepb.ActivateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func TestValidateCollectionTaskDatasetsRejectsMarketAndFrequencyMismatch(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"symbols": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		"bars":    {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	task := domain.CollectionTask{SpaceID: "crypto", DataType: "kline", Provider: "binance", MarketType: "spot", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"5m"}`}
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), task), `does not enable frequency "5m"`)

	task.CollectParams = `{"provider":"binance","market_type":"swap","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), task), "market_type=spot does not match task market_type=swap")
}

func TestValidateCollectionTaskDatasetsRejectsTargetMarketMismatch(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"bars": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	task := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", Provider: "binance", MarketType: "swap",
		CollectParams: `{"provider":"binance","market_type":"swap","subject_tags":["binance_swap"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), task), "market_type=spot does not match task market_type=swap")
}

func TestValidateCollectionTaskDatasetsAcceptsStockSharedDataSource(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"symbols":                      {DataSourceID: "stockcn", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active", Attributes: map[string]string{"market_type": "equity"}},
		"dataset_stockcn_equity_kline": {DataSourceID: "stockcn", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, Attributes: map[string]string{"market_type": "equity"}},
	}}
	task := domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "stock-bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity",
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m"}`,
	}
	require.NoError(t, service.validateCollectionTaskDatasets(context.Background(), task))
}

func TestValidateCollectionTaskAcceptsCollectorLocalResampleWithoutCloudRoute(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", TaskName: "resample", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_spot_kline_derived_4h","target_frequency":"4H","alignment":"epoch_utc"}`,
	}
	require.NoError(t, validateCollectionTask(task))
}

func TestValidateCollectionTaskAcceptsBoundedStockHistoryMode(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "stock-bars", TaskName: "stock bars", DataType: "kline", Provider: "stockcn_multi", MarketType: "equity",
		CollectParams: `{"provider":"stockcn_multi","market_type":"equity","subject_tags":["binance_spot"],"target_dataset_id":"dataset_stockcn_equity_kline","frequency":"1m","history_policy":{"mode":"lookback","lookback":5}}`,
	}
	require.NoError(t, validateCollectionTask(task))
}

func TestNormalizeCollectionTaskGeneratesUUIDTaskIDAndTrimsName(t *testing.T) {
	first := normalizeCollectionTask(domain.CollectionTask{TaskName: "  first task  "})
	second := normalizeCollectionTask(domain.CollectionTask{TaskName: " second task "})

	require.NotEqual(t, first.TaskID, second.TaskID)
	for _, task := range []domain.CollectionTask{first, second} {
		require.Equal(t, "task_", task.TaskID[:len("task_")])
		_, err := uuid.Parse(strings.TrimPrefix(task.TaskID, "task_"))
		require.NoError(t, err)
	}
	require.Equal(t, "first task", first.TaskName)
	require.Equal(t, "second task", second.TaskName)
}

func TestValidateCreateTaskResultIdentityRejectsCallerOwnedResults(t *testing.T) {
	for _, field := range []string{"target_dataset_id", "result_dataset_id", "result_view_id", "target_view_id"} {
		t.Run(field, func(t *testing.T) {
			require.ErrorContains(t, validateCreateTaskResultIdentity(fmt.Sprintf(`{"%s":"existing"}`, field)), field)
		})
	}

	require.NoError(t, validateCreateTaskResultIdentity(`{"source_dataset_id":"source"}`))
}

func TestAssignGeneratedCollectionTaskResultKeepsInputSourceFields(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"frequency":"1m"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)

	assigned, ids, err := assignGeneratedCollectionTaskResult(task, params)
	require.NoError(t, err)
	require.NotEmpty(t, ids.DatasetID)
	require.NotEmpty(t, ids.ViewID)
	require.Equal(t, ids.DatasetID, assigned.ResultDatasetID)
	require.Equal(t, ids.ViewID, assigned.ResultViewID)

	assignedParams, err := domain.ParseCollectParams(assigned.CollectParams, assigned.Provider, assigned.MarketType, assigned.DataType)
	require.NoError(t, err)
	require.Equal(t, ids.DatasetID, assignedParams.TargetDatasetID)
}

func TestAssignGeneratedCollectionTaskResultUsesOneIdentityForEveryResampleFrequency(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"caller-target","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)

	assigned, ids, err := assignGeneratedCollectionTaskResult(task, params)
	require.NoError(t, err)
	expected := taskresult.ResultIDs(task.SpaceID, task.TaskID)
	require.Equal(t, expected, ids)
	require.Equal(t, expected.DatasetID, assigned.ResultDatasetID)
	require.Equal(t, expected.ViewID, assigned.ResultViewID)
	require.Equal(t, expected.DatasetID, mustParseCollectParams(t, assigned).TargetDatasetID)

	task.CollectParams = strings.Replace(task.CollectParams, `"5m"`, `"4h"`, 1)
	params, err = domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)
	_, otherIDs, err := assignGeneratedCollectionTaskResult(task, params)
	require.NoError(t, err)
	require.Equal(t, ids, otherIDs)
}

func mustParseCollectParams(t *testing.T, task domain.CollectionTask) *domain.CollectParams {
	t.Helper()
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)
	return params
}

func TestCollectionTaskResultIDsIgnoreUserTargetAndFrequency(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"caller-target","target_frequency":"4h","alignment":"epoch_utc"}`,
	}
	ids, err := collectionTaskResultIDs(task)
	require.NoError(t, err)
	require.Equal(t, taskresult.ResultIDs(task.SpaceID, task.TaskID), ids)
}

func TestCanonicalizeResampleTaskStartsInWaitingViewState(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_collector_0123456789abcdef","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	canonical, err := canonicalizeCollectionTask(task)
	require.NoError(t, err)
	require.Equal(t, domain.PrepareStateWaitingView, canonical.PrepareState)
}

func TestDeleteTaskRetainsResultMetadataWhenRequested(t *testing.T) {
	db := openCollectorTestStore(t)
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-retain", TaskName: "retain", Enabled: true}
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	metadata := newTaskResultMetadataFake(taskresult.ResultIDs(task.SpaceID, task.TaskID), task.TaskID)
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"})}

	rsp, err := service.DeleteTask(context.Background(), &pb.DeleteTaskReq{SpaceId: task.SpaceID, TaskId: task.TaskID})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 0, metadata.deleteViews)
	require.Equal(t, 0, metadata.deleteDatasets)
	require.NotEmpty(t, metadata.views)
	require.NotEmpty(t, metadata.datasets)
}

func TestDeleteTaskPhysicallyDeletesOwnedResultAndManagerDeleteIsIdempotent(t *testing.T) {
	db := openCollectorTestStore(t)
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-delete", TaskName: "delete", Enabled: true}
	ids := taskresult.ResultIDs(task.SpaceID, task.TaskID)
	task.ResultDatasetID, task.ResultViewID = ids.DatasetID, ids.ViewID
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	metadata := newTaskResultMetadataFake(ids, task.TaskID)
	manager := taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"})
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: manager}

	rsp, err := service.DeleteTask(context.Background(), &pb.DeleteTaskReq{SpaceId: task.SpaceID, TaskId: task.TaskID, DeleteResultData: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, metadata.deleteViews)
	require.Equal(t, 1, metadata.deleteDatasets)
	require.Empty(t, metadata.views)
	require.Empty(t, metadata.datasets)

	require.NoError(t, manager.Delete(context.Background(), task.SpaceID, ids))
	require.Equal(t, 2, metadata.deleteViews)
	require.Equal(t, 2, metadata.deleteDatasets)
}

func TestDeleteTaskResultFailureLeavesDisabledTaskRetryable(t *testing.T) {
	db := openCollectorTestStore(t)
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-retry", TaskName: "retry", Enabled: true}
	ids := taskresult.ResultIDs(task.SpaceID, task.TaskID)
	task.ResultDatasetID, task.ResultViewID = ids.DatasetID, ids.ViewID
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	metadata := newTaskResultMetadataFake(ids, task.TaskID)
	metadata.failDeleteDataset = true
	manager := taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"})
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: manager}

	rsp, err := service.DeleteTask(context.Background(), &pb.DeleteTaskReq{SpaceId: task.SpaceID, TaskId: task.TaskID, DeleteResultData: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "delete task result failed")
	stored, getErr := db.Tasks().GetByTaskID(context.Background(), task.SpaceID, task.TaskID)
	require.NoError(t, getErr)
	require.False(t, stored.Enabled)

	metadata.failDeleteDataset = false
	rsp, err = service.DeleteTask(context.Background(), &pb.DeleteTaskReq{SpaceId: task.SpaceID, TaskId: task.TaskID, DeleteResultData: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	_, getErr = db.Tasks().GetByTaskID(context.Background(), task.SpaceID, task.TaskID)
	require.ErrorIs(t, getErr, gorm.ErrRecordNotFound)
}

func openCollectorTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	return db
}

func TestCreateTaskRejectsInvalidNamesBeforeStorage(t *testing.T) {
	service := &Service{}
	for _, name := range []string{" \t\n", strings.Repeat("任", 81)} {
		t.Run(name, func(t *testing.T) {
			rsp, err := service.CreateTask(context.Background(), &pb.CreateTaskReq{
				Task: &pb.CollectionTask{TaskName: name},
			})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			require.Contains(t, rsp.GetRetInfo().GetMsg(), "task_name")
		})
	}
}

func TestCreateTaskRejectsCallerResultIdentityBeforeStorage(t *testing.T) {
	params, err := structpb.NewStruct(map[string]any{"target_dataset_id": "existing"})
	require.NoError(t, err)

	rsp, callErr := (&Service{}).CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{TaskName: "new task", CollectParams: params},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "target_dataset_id")

	rsp, callErr = (&Service{}).CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{
			TaskName: "new task",
			Result:   &pb.TaskResult{ViewId: "existing-view"},
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "result.view_id")
}

type acceptingKlineDatasetSource struct{}

func (acceptingKlineDatasetSource) GetDataset(_ context.Context, _ string, _ string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{
		DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"},
		SubjectTags: []string{"binance_spot"},
		Attributes:  map[string]string{"market_type": "spot"},
	}, nil
}

func (acceptingKlineDatasetSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	return []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}}, nil
}

type failingDatasetSource struct{}

func (failingDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{}, fmt.Errorf("dataset lookup failed")
}

func (failingDatasetSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	return nil, nil
}

type emptySubjectsDatasetSource struct{}

func (emptySubjectsDatasetSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{}, nil
}

func (emptySubjectsDatasetSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	return nil, nil
}

func TestResolveSubjectTagsRequiresNonEmptyKlineTags(t *testing.T) {
	service := &Service{datasetSrc: acceptingKlineDatasetSource{}}
	task := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","frequency":"1m"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)
	_, _, err = service.resolveSubjectTags(context.Background(), task, params, "")
	require.ErrorContains(t, err, "请选择标的标签")
}

func TestResolveSubjectTagsRejectsEmptyStorageSubjectResolution(t *testing.T) {
	service := &Service{datasetSrc: emptySubjectsDatasetSource{}}
	task := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"frequency":"1m"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, task.Provider, task.MarketType, task.DataType)
	require.NoError(t, err)
	_, _, err = service.resolveSubjectTags(context.Background(), task, params, "")
	require.ErrorContains(t, err, "所选标签没有有效标的")
}

func TestResolveSubjectTagsSupportsExplicitAndInheritedResampleTags(t *testing.T) {
	source := validationDatasetSource{
		"source": {SubjectTags: []string{"source_tag"}},
	}
	service := &Service{datasetSrc: source}

	explicit := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","subject_tags":["explicit_tag"],"source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	explicitParams, err := domain.ParseCollectParams(explicit.CollectParams, explicit.Provider, explicit.MarketType, explicit.DataType)
	require.NoError(t, err)
	clean, tags, err := service.resolveSubjectTags(context.Background(), explicit, explicitParams, "")
	require.NoError(t, err)
	require.Equal(t, []string{"explicit_tag"}, tags)
	require.NotContains(t, clean, "subject_tags")

	inherited := explicit
	inherited.CollectParams = strings.Replace(explicit.CollectParams, `"subject_tags":["explicit_tag"],`, "", 1)
	inheritedParams, err := domain.ParseCollectParams(inherited.CollectParams, inherited.Provider, inherited.MarketType, inherited.DataType)
	require.NoError(t, err)
	clean, tags, err = service.resolveSubjectTags(context.Background(), inherited, inheritedParams, "")
	require.NoError(t, err)
	require.Equal(t, []string{"source_tag"}, tags)
	require.NotContains(t, clean, "subject_tags")
}

func TestCreateTaskProvisionsExclusiveResult(t *testing.T) {
	db := openCollectorTestStore(t)
	metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}, views: map[string]*storagepb.View{}}
	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{
		taskRepo:         db.Tasks(),
		resultManager:    taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"}),
		datasetSrc:       acceptingKlineDatasetSource{},
		resultDataNodeID: "node-collector",
	}

	rsp, callErr := service.CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskName: "  Binance 现货 K 线  ", DataType: "kline",
			Provider: "binance", MarketType: "spot", CollectParams: params,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.True(t, strings.HasPrefix(rsp.GetTaskId(), "task_"))
	_, err = uuid.Parse(strings.TrimPrefix(rsp.GetTaskId(), "task_"))
	require.NoError(t, err)

	stored, getErr := db.Tasks().GetByTaskID(context.Background(), "crypto", rsp.GetTaskId())
	require.NoError(t, getErr)
	require.Equal(t, "Binance 现货 K 线", stored.TaskName)
	expected := taskresult.ResultIDs("crypto", rsp.GetTaskId())
	require.Equal(t, expected.DatasetID, stored.ResultDatasetID)
	require.Equal(t, expected.ViewID, stored.ResultViewID)
	require.Contains(t, metadata.datasets, expected.DatasetID)
	require.Contains(t, metadata.views, expected.ViewID)
	require.Equal(t, "collector", metadata.datasets[expected.DatasetID].GetAttributes()["owner_module"])
	require.Equal(t, rsp.GetTaskId(), metadata.datasets[expected.DatasetID].GetAttributes()["collector_task_id"])
	require.Equal(t, []string{"binance_spot"}, metadata.datasets[expected.DatasetID].GetSubjectTags())
	require.NotContains(t, stored.CollectParams, "subject_tags")
}

func TestUpdateTaskPersistsSubjectTagsOnResultDatasetOnly(t *testing.T) {
	db := openCollectorTestStore(t)
	ids := taskresult.ResultIDs("crypto", "task-tags")
	metadata := newTaskResultMetadataFake(ids, "task-tags")
	metadata.datasets[ids.DatasetID].SubjectTags = []string{"old_tag"}
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-tags", TaskName: "标签任务", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: fmt.Sprintf(`{"provider":"binance","market_type":"spot","target_dataset_id":%q,"frequency":"1m"}`, ids.DatasetID),
		Enabled:       true, ResultDatasetID: ids.DatasetID, ResultViewID: ids.ViewID,
	}))

	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"new_tag"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"}),
		datasetSrc: validationDatasetSource{
			ids.DatasetID: {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, SubjectTags: []string{"old_tag"}, Attributes: map[string]string{"market_type": "spot"}},
		},
	}

	rsp, callErr := service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		SpaceId: "crypto", TaskId: "task-tags",
		Task: &pb.CollectionTask{SpaceId: "crypto", TaskId: "task-tags", TaskName: "标签任务", DataType: "kline", Provider: "binance", MarketType: "spot", CollectParams: params},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	updated, err := db.Tasks().GetByTaskID(context.Background(), "crypto", "task-tags")
	require.NoError(t, err)
	require.NotContains(t, updated.CollectParams, "subject_tags")
	require.Equal(t, []string{"new_tag"}, metadata.datasets[ids.DatasetID].GetSubjectTags())
}

func TestCreateTaskCompensatesResultWhenDatasetValidationFails(t *testing.T) {
	db := openCollectorTestStore(t)
	metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}, views: map[string]*storagepb.View{}}
	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{
		taskRepo:         db.Tasks(),
		resultManager:    taskresult.NewManagerWithAPI(metadata, &storagepb.AuthInfo{AppId: "collector"}),
		datasetSrc:       failingDatasetSource{},
		resultDataNodeID: "node-collector",
	}

	rsp, callErr := service.CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskName: "补偿任务", DataType: "kline",
			Provider: "binance", MarketType: "spot", CollectParams: params,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Empty(t, metadata.datasets)
	require.Empty(t, metadata.views)
	_, getErr := db.Tasks().GetByTaskName(context.Background(), "crypto", "补偿任务")
	require.ErrorIs(t, getErr, gorm.ErrRecordNotFound)
}

func TestCreateTaskRejectsDuplicateNameWithinSpace(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "existing", TaskName: "same name",
	}))

	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{taskRepo: db.Tasks(), datasetSrc: acceptingKlineDatasetSource{}}
	rsp, callErr := service.CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskName: " same name ", DataType: "kline",
			Provider: "binance", MarketType: "spot", CollectParams: params,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "task_name")
}

func TestUpdateTaskOnlyChangesMutableFields(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", TaskName: "original", Description: "before",
		DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-original","frequency":"1m"}`,
		Enabled:       true, Creator: "creator", PrepareState: domain.PrepareStateReady,
		ResultDatasetID: "dataset-original", ResultViewID: "view-original",
	}))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-2", TaskName: "occupied",
		DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-occupied","frequency":"1m"}`,
		Enabled:       true, PrepareState: domain.PrepareStateReady,
		ResultDatasetID: "dataset-occupied", ResultViewID: "view-occupied",
	}))

	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "frequency": "1m",
	})
	require.NoError(t, err)
	enabled := false
	service := &Service{
		taskRepo: db.Tasks(),
		datasetSrc: validationDatasetSource{
			"dataset-original": {
				DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1m"}, SubjectTags: []string{"binance_spot"},
				Attributes: map[string]string{"market_type": "spot"},
			},
		},
	}
	rsp, callErr := service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		SpaceId: "crypto", TaskId: "task-1",
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: "task-1", TaskName: " renamed ", Description: "after",
			DataType: "kline", Provider: "binance", MarketType: "spot",
			CollectParams: params, Enabled: &enabled,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())

	updated, err := db.Tasks().GetByTaskID(context.Background(), "crypto", "task-1")
	require.NoError(t, err)
	require.Equal(t, "renamed", updated.TaskName)
	require.Equal(t, "after", updated.Description)
	require.False(t, updated.Enabled)
	require.Equal(t, "kline", updated.DataType)
	require.Equal(t, "binance", updated.Provider)
	require.Equal(t, "spot", updated.MarketType)
	require.Equal(t, "dataset-original", updated.ResultDatasetID)
	require.Equal(t, "view-original", updated.ResultViewID)
	require.Equal(t, "creator", updated.Creator)

	rsp, callErr = service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		SpaceId: "crypto", TaskId: "task-1",
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: "task-1", TaskName: "occupied",
			DataType: "kline", Provider: "binance", MarketType: "spot",
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "task_name")

	rsp, callErr = service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		// A data type change is immutable; use a valid resample payload so the
		// assertion reaches the task identity guard rather than JSON parsing.
		SpaceId: "crypto", TaskId: "task-1",
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: "task-1", TaskName: "renamed",
			DataType: "kline_resample", Provider: "moox", MarketType: "spot",
			CollectParams: func() *structpb.Struct {
				value, structErr := structpb.NewStruct(map[string]any{"provider": "moox", "market_type": "spot", "source_dataset_id": "dataset-original", "source_frequency": "1m", "source_series_tag": "venue:binance", "target_frequency": "5m", "alignment": "epoch_utc"})
				require.NoError(t, structErr)
				return value
			}(),
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "create a new task")
}

func TestPreserveCollectionTaskCoverageStartOnOrdinaryUpdate(t *testing.T) {
	original := time.Date(2026, 8, 29, 1, 2, 0, 0, time.UTC)
	replacement := original.Add(24 * time.Hour)
	desired := domain.CollectionTask{CoverageStartTime: &replacement}
	preserveCollectionTaskCoverageStart(domain.CollectionTask{CoverageStartTime: &original}, &desired)
	require.NotNil(t, desired.CoverageStartTime)
	require.Equal(t, original, desired.CoverageStartTime.UTC())
}

func TestValidateCollectionTaskUpdateAllowsOnlyMutableFields(t *testing.T) {
	base := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", TaskName: "original", DataType: "kline_resample", Provider: "moox", MarketType: "spot", Enabled: true,
		CollectParams:   `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc","settle_delay_ms":10000}`,
		ResultDatasetID: "target", ResultViewID: "view_target",
	}

	mutable := base
	mutable.TaskName = "renamed"
	mutable.Description = "updated"
	mutable.Enabled = false
	require.NoError(t, validateCollectionTaskUpdate(base, mutable))

	immutable := map[string]func(*domain.CollectionTask){
		"task_id":     func(task *domain.CollectionTask) { task.TaskID = "other" },
		"space_id":    func(task *domain.CollectionTask) { task.SpaceID = "other" },
		"data_type":   func(task *domain.CollectionTask) { task.DataType = "kline" },
		"provider":    func(task *domain.CollectionTask) { task.Provider = "binance" },
		"market_type": func(task *domain.CollectionTask) { task.MarketType = "swap" },
		"collect_params": func(task *domain.CollectionTask) {
			task.CollectParams = strings.Replace(task.CollectParams, `"4H"`, `"1H"`, 1)
		},
		"result_dataset": func(task *domain.CollectionTask) { task.ResultDatasetID = "other" },
		"result_view":    func(task *domain.CollectionTask) { task.ResultViewID = "other" },
	}
	for name, change := range immutable {
		t.Run(name, func(t *testing.T) {
			changed := base
			change(&changed)
			require.ErrorContains(t, validateCollectionTaskUpdate(base, changed), "create a new task")
		})
	}
}

func TestValidateCollectionTaskUpdateIgnoresLegacySubjectTags(t *testing.T) {
	existing := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-legacy-tags", TaskName: "legacy tags", DataType: "kline", Provider: "binance", MarketType: "spot",
		CollectParams:   `{"provider":"binance","market_type":"spot","subject_tags":["old_tag"],"target_dataset_id":"dataset-result","frequency":"1m"}`,
		ResultDatasetID: "dataset-result", ResultViewID: "view-result",
	}
	desired := existing
	desired.CollectParams = `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-result","frequency":"1m"}`

	require.NoError(t, validateCollectionTaskUpdate(existing, desired))
}

func TestValidateTaskResultIdentityUpdateRejectsViewChanges(t *testing.T) {
	existing := domain.CollectionTask{ResultViewID: "view-original"}
	require.ErrorContains(t,
		validateTaskResultIdentityUpdate(existing, &pb.CollectionTask{Result: &pb.TaskResult{ViewId: "view-other"}}),
		"create a new task",
	)
	require.NoError(t, validateTaskResultIdentityUpdate(existing, &pb.CollectionTask{Result: &pb.TaskResult{ViewId: "view-original"}}))
}

func TestValidateResampleSourceDoesNotFoldMonthIntoMinute(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"source": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1M"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), rule), `does not enable frequency "1m"`)
}

func TestValidateCollectionTaskDatasetsAcceptsExchangeSourceForMooxResample(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"source": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freqs: []string{"1H"}, Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", DataType: "kline_resample", Provider: "moox", MarketType: "spot",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1H","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4H","alignment":"epoch_utc"}`,
	}
	require.NoError(t, service.validateCollectionTaskDatasets(context.Background(), rule))
}

func TestGetKlineResampleBackfillKeepsMixedActiveRequestCancelable(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	start := time.Date(2026, 8, 29, 8, 0, 0, 0, time.UTC)
	makeInstance := func(taskID string, state domain.ResampleBackfillState) domain.TaskInstance {
		result := domain.NewResampleTaskResult(start)
		result.LastError = "source retention expired"
		result.Backfill = &domain.ResampleBackfill{RequestID: "request-1", Start: start, End: start.Add(time.Hour), NextBucket: start, State: state}
		encoded, marshalErr := result.Marshal()
		require.NoError(t, marshalErr)
		return domain.TaskInstance{SpaceID: "crypto", InstanceID: taskID, CollectionTaskID: "rule-5m", DataType: "kline_resample", Result: encoded}
	}
	instances := []domain.TaskInstance{makeInstance("failed", domain.ResampleBackfillFailed), makeInstance("syncing", domain.ResampleBackfillSyncing)}
	require.NoError(t, db.TaskInstances().UpsertMany(context.Background(), instances))
	service := &Service{instanceRepo: db.TaskInstances()}
	rsp, err := service.GetKlineResampleBackfill(context.Background(), &pb.GetKlineResampleBackfillReq{SpaceId: "crypto", TaskId: "rule-5m", RequestId: "request-1"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, "syncing", rsp.GetState())
	require.EqualValues(t, 1, rsp.GetFailed())
	require.EqualValues(t, 1, rsp.GetSyncing())
}
