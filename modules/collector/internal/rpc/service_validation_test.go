package rpc

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/mooyang-code/moox/modules/collector/internal/markets"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/structpb"
	"gorm.io/gorm"
)

func TestCheckSourceRetentionRejectsRangeOlderThanRetainedSource(t *testing.T) {
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	require.ErrorContains(t, checkSourceRetention(storagesource.DatasetInfo{Retention: "1h"}, now.Add(-2*time.Hour), now), "older than source Dataset retention")
	require.NoError(t, checkSourceRetention(storagesource.DatasetInfo{Retention: "24h"}, now.Add(-3*time.Hour), now))
	require.NoError(t, checkSourceRetention(storagesource.DatasetInfo{Retention: "forever"}, now.Add(-1000*time.Hour), now))
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
	mu                sync.Mutex
	datasets          map[string]*storagepb.Dataset
	getDatasets       int
	deleteDatasets    int
	upsertColumns     int
	failDeleteDataset bool
	failGetDataset    error
	// indexedTo 是 storage-view 里各数据集已建索引的上界；没有的按 VIEW_NOT_FOUND 回答。
	indexedTo map[string]string
}

func newTaskResultMetadataFake(ids taskresult.IDs, taskID string) *taskResultMetadataFake {
	return &taskResultMetadataFake{
		datasets: map[string]*storagepb.Dataset{
			ids.DatasetID: {
				SpaceId: "crypto", DatasetId: ids.DatasetID, Status: "active",
				Attributes: map[string]string{"owner_module": "collector", "collector_task_id": taskID},
			},
		},
	}
}

func (f *taskResultMetadataFake) GetDataset(_ context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getDatasets++
	if f.failGetDataset != nil {
		return nil, f.failGetDataset
	}
	dataset := f.datasets[req.GetDatasetId()]
	if dataset == nil {
		return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_DATASET_NOT_FOUND}}, nil
	}
	return &storagepb.GetDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
}

func (f *taskResultMetadataFake) CreateDataset(_ context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dataset := req.GetDataset()
	dataset.Status = "active"
	f.datasets[dataset.GetDatasetId()] = dataset
	return &storagepb.CreateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: dataset}, nil
}

func (f *taskResultMetadataFake) UpdateDataset(_ context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.datasets[req.GetDataset().GetDatasetId()] = req.GetDataset()
	return &storagepb.UpdateDatasetRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, Dataset: req.GetDataset()}, nil
}

func (f *taskResultMetadataFake) DeleteDataset(_ context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *taskResultMetadataFake) UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upsertColumns++
	return &storagepb.UpsertDatasetColumnRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}}, nil
}

func (f *taskResultMetadataFake) GetViewState(_ context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	indexedTo, ok := f.indexedTo[req.GetDatasetId()]
	if !ok {
		return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_VIEW_NOT_FOUND}}, nil
	}
	return &storagepb.GetViewStateRsp{RetInfo: &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}, IndexId: "idx", IndexedTo: indexedTo}, nil
}

func TestValidateCollectionTaskDatasetsRejectsMarketAndFrequencyMismatch(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"symbols": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_RECORD, Status: "active", Attributes: map[string]string{"market_type": "spot"}},
		"bars":    {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m", Attributes: map[string]string{"market_type": "spot"}},
	}}
	task := domain.CollectionTask{SpaceID: "crypto", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"5m"}`}
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), task), `freq is "1m", not "5m"`)

	task.CollectParams = `{"provider":"binance","market_type":"swap","subject_tags":["binance_spot"],"target_dataset_id":"bars","frequency":"1m"}`
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), task), "market_type=spot does not match task market_type=swap")
}

func TestValidateCollectionTaskDatasetsRejectsTargetMarketMismatch(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"bars": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m", Attributes: map[string]string{"market_type": "spot"}},
	}}
	task := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"swap","subject_tags":["binance_swap"],"target_dataset_id":"bars","frequency":"1m"}`,
	}
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), task), "market_type=spot does not match task market_type=swap")
}

func TestValidateCollectionTaskAcceptsCollectorLocalResampleWithoutCloudRoute(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", TaskName: "resample", DataType: "kline_resample", CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_spot_kline_derived_4h","target_frequency":"4h","alignment":"epoch_utc"}`,
	}
	require.NoError(t, validateCollectionTask(task))
}

func TestValidateCollectionTaskAcceptsBoundedStockHistoryMode(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "stockcn", TaskID: "stock-bars", TaskName: "stock bars", DataType: "kline", TagIDs: []string{"cn_a_share"},
		CollectParams: `{"provider":"eastmoney","market_type":"equity","target_dataset_id":"dataset_stock_bars","frequency":"5m"}`,
	}
	require.NoError(t, validateCollectionTask(task))
}

func TestNormalizeCollectionTaskGeneratesUUIDTaskIDAndTrimsName(t *testing.T) {
	first := normalizeCollectionTask(domain.CollectionTask{TaskName: "  first task  "})
	second := normalizeCollectionTask(domain.CollectionTask{TaskName: " second task "})

	require.NotEqual(t, first.TaskID, second.TaskID)
	for _, task := range []domain.CollectionTask{first, second} {
		require.Len(t, task.TaskID, 20)
		require.Regexp(t, `^[a-z0-9]+$`, task.TaskID)
	}
	require.Equal(t, "first task", first.TaskName)
	require.Equal(t, "second task", second.TaskName)
}

func TestValidateCreateTaskResultIdentityRejectsCallerOwnedResults(t *testing.T) {
	for _, field := range []string{"target_dataset_id", "result_dataset_id", "result_id"} {
		t.Run(field, func(t *testing.T) {
			require.ErrorContains(t, validateCreateTaskResultIdentity(fmt.Sprintf(`{"%s":"existing"}`, field)), field)
		})
	}

	require.NoError(t, validateCreateTaskResultIdentity(`{"source_dataset_id":"source"}`))
}

func TestAssignGeneratedCollectionTaskResultKeepsInputSourceFields(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"frequency":"1m"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)

	assigned, ids, err := assignGeneratedCollectionTaskResult(task, params)
	require.NoError(t, err)
	require.NotEmpty(t, ids.DatasetID)
	require.Equal(t, ids.DatasetID, assigned.ResultDatasetID)

	assignedParams, err := domain.ParseCollectParams(assigned.CollectParams, "", "", assigned.DataType)
	require.NoError(t, err)
	require.Equal(t, ids.DatasetID, assignedParams.TargetDatasetID)
}

func TestAssignGeneratedCollectionTaskResultUsesOneIdentityForEveryResampleFrequency(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample", CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"caller-target","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)

	assigned, ids, err := assignGeneratedCollectionTaskResult(task, params)
	require.NoError(t, err)
	expected := taskresult.ResultIDs(task.TaskID)
	require.Equal(t, expected, ids)
	require.Equal(t, expected.DatasetID, assigned.ResultDatasetID)
	require.Equal(t, expected.DatasetID, mustParseCollectParams(t, assigned).TargetDatasetID)

	task.CollectParams = strings.Replace(task.CollectParams, `"5m"`, `"4h"`, 1)
	params, err = domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)
	_, otherIDs, err := assignGeneratedCollectionTaskResult(task, params)
	require.NoError(t, err)
	require.Equal(t, ids, otherIDs)
}

func mustParseCollectParams(t *testing.T, task domain.CollectionTask) *domain.CollectParams {
	t.Helper()
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)
	return params
}

func TestCollectionTaskResultIDsIgnoreUserTargetAndFrequency(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample",
		CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"caller-target","target_frequency":"4h","alignment":"epoch_utc"}`,
	}
	require.Equal(t, taskresult.ResultIDs(task.TaskID), collectionTaskResultIDs(task))
}

func TestCanonicalizeResampleTaskStartsInWaitingViewState(t *testing.T) {
	task := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-resample", DataType: "kline_resample", CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_collector_0123456789abcdef","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	canonical, err := canonicalizeCollectionTask(task)
	require.NoError(t, err)
	require.Equal(t, domain.PrepareStateWaitingView, canonical.PrepareState)
}

func TestDeleteTaskRetainsResultMetadataWhenRequested(t *testing.T) {
	db := openCollectorTestStore(t)
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-retain", TaskName: "retain", Enabled: true}
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	metadata := newTaskResultMetadataFake(taskresult.ResultIDs(task.TaskID), task.TaskID)
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: taskresult.NewManagerWithAPI(metadata)}

	rsp, err := service.DeleteTask(context.Background(), &pb.DeleteTaskReq{SpaceId: task.SpaceID, TaskId: task.TaskID})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 0, metadata.deleteDatasets)
	require.NotEmpty(t, metadata.datasets)
}

func TestDeleteTaskPhysicallyDeletesOwnedResultAndManagerDeleteIsIdempotent(t *testing.T) {
	db := openCollectorTestStore(t)
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-delete", TaskName: "delete", Enabled: true}
	ids := taskresult.ResultIDs(task.TaskID)
	task.ResultDatasetID = ids.DatasetID
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	metadata := newTaskResultMetadataFake(ids, task.TaskID)
	manager := taskresult.NewManagerWithAPI(metadata)
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: manager}

	rsp, err := service.DeleteTask(context.Background(), &pb.DeleteTaskReq{SpaceId: task.SpaceID, TaskId: task.TaskID, DeleteResultData: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Equal(t, 1, metadata.deleteDatasets)
	require.Empty(t, metadata.datasets)

	require.NoError(t, manager.Delete(context.Background(), task.SpaceID, ids))
	require.Equal(t, 2, metadata.deleteDatasets)
}

func TestDeleteTaskResultFailureLeavesDisabledTaskRetryable(t *testing.T) {
	db := openCollectorTestStore(t)
	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-retry", TaskName: "retry", Enabled: true}
	ids := taskresult.ResultIDs(task.TaskID)
	task.ResultDatasetID = ids.DatasetID
	require.NoError(t, db.Tasks().Create(context.Background(), task))
	metadata := newTaskResultMetadataFake(ids, task.TaskID)
	metadata.failDeleteDataset = true
	manager := taskresult.NewManagerWithAPI(metadata)
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
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	return db
}

func TestGetTaskListDoesNotFanOutToStorageAndDetailInspectsOneTask(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	metadata := &taskResultMetadataFake{
		datasets: make(map[string]*storagepb.Dataset),
	}
	const taskCount = 1000
	for index := 0; index < taskCount; index++ {
		taskID := fmt.Sprintf("task-%04d", index)
		ids := taskresult.ResultIDs(taskID)
		task := domain.CollectionTask{
			SpaceID: "crypto", TaskID: taskID, TaskName: taskID, DataType: "kline", Enabled: true,
			PrepareState: domain.PrepareStateReady, ResultDatasetID: ids.DatasetID,
		}
		require.NoError(t, db.Tasks().Create(ctx, task))
		taskMetadata := newTaskResultMetadataFake(ids, taskID)
		metadata.datasets[ids.DatasetID] = taskMetadata.datasets[ids.DatasetID]
	}
	service := &Service{
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
	}

	list, err := service.GetTaskList(ctx, &pb.GetTaskListReq{SpaceId: "crypto", Page: &commonpb.Page{Page: 1, Size: taskCount}})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, list.GetRetInfo().GetCode())
	require.Len(t, list.GetTasks(), taskCount)
	require.Equal(t, 0, metadata.getDatasets)
	require.Equal(t, taskresult.ResultStatusUnknown, list.GetTasks()[0].GetResult().GetStatus())

	detail, err := service.GetTaskDetail(ctx, &pb.GetTaskDetailReq{SpaceId: "crypto", TaskId: "task-0000"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_SUCCESS, detail.GetRetInfo().GetCode())
	require.Equal(t, taskresult.ResultStatusReady, detail.GetTask().GetResult().GetStatus())
	require.Equal(t, 1, metadata.getDatasets)
}

func TestGetTaskDetailDistinguishesResultContractFailureFromStorageFailure(t *testing.T) {
	for _, test := range []struct {
		name       string
		prepare    func(*taskResultMetadataFake, taskresult.IDs)
		wantStatus string
		wantError  string
	}{
		{
			name: "ownership mismatch is permanent error",
			prepare: func(metadata *taskResultMetadataFake, ids taskresult.IDs) {
				metadata.datasets[ids.DatasetID].Attributes["collector_task_id"] = "another-task"
			},
			wantStatus: taskresult.ResultStatusError,
			wantError:  "owned by another task",
		},
		{
			name: "Storage outage remains unknown",
			prepare: func(metadata *taskResultMetadataFake, _ taskresult.IDs) {
				metadata.failGetDataset = errors.New("Storage unavailable")
			},
			wantStatus: taskresult.ResultStatusUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openCollectorTestStore(t)
			task := domain.CollectionTask{
				SpaceID: "crypto", TaskID: "task-contract", TaskName: "contract", DataType: "kline",
				PrepareState: domain.PrepareStateReady,
			}
			ids := taskresult.ResultIDs(task.TaskID)
			task.ResultDatasetID = ids.DatasetID
			require.NoError(t, db.Tasks().Create(context.Background(), task))
			metadata := newTaskResultMetadataFake(ids, task.TaskID)
			test.prepare(metadata, ids)
			service := &Service{
				taskRepo:      db.Tasks(),
				resultManager: taskresult.NewManagerWithAPI(metadata),
			}
			response, err := service.GetTaskDetail(context.Background(), &pb.GetTaskDetailReq{SpaceId: task.SpaceID, TaskId: task.TaskID})
			require.NoError(t, err)
			require.Equal(t, pb.ErrorCode_SUCCESS, response.GetRetInfo().GetCode())
			require.Equal(t, test.wantStatus, response.GetTask().GetResult().GetStatus())
			require.Contains(t, response.GetTask().GetLastError(), test.wantError)
		})
	}
}

func TestGetTaskDetailMapsOnlyMissingTasksToNotFound(t *testing.T) {
	db := openCollectorTestStore(t)
	service := &Service{taskRepo: db.Tasks()}
	ctx := context.Background()

	missing, err := service.GetTaskDetail(ctx, &pb.GetTaskDetailReq{SpaceId: "crypto", TaskId: "missing"})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_NOT_FOUND, missing.GetRetInfo().GetCode())

	task := domain.CollectionTask{SpaceID: "crypto", TaskID: "present", TaskName: "Present", DataType: "kline"}
	require.NoError(t, db.Tasks().Create(ctx, task))
	require.NoError(t, db.Close())

	unavailable, err := service.GetTaskDetail(ctx, &pb.GetTaskDetailReq{SpaceId: task.SpaceID, TaskId: task.TaskID})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, unavailable.GetRetInfo().GetCode())
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
	for _, field := range []string{"target_dataset_id", "result_dataset_id"} {
		t.Run(field, func(t *testing.T) {
			params, err := structpb.NewStruct(map[string]any{field: "existing"})
			require.NoError(t, err)
			rsp, callErr := (&Service{}).CreateTask(context.Background(), &pb.CreateTaskReq{
				Task: &pb.CollectionTask{TaskName: "new task", CollectParams: params},
			})
			require.NoError(t, callErr)
			require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
			require.Contains(t, rsp.GetRetInfo().GetMsg(), field)
		})
	}
}

func TestCreateTasksWithDifferentIDsOwnDifferentResultDatasets(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}}
	service := &Service{
		taskRepo: db.Tasks(), resultManager: taskresult.NewManagerWithAPI(metadata),
		datasetSrc: acceptingKlineDatasetSource{},
	}
	var datasets []string
	var taskIDs []string
	for index, taskName := range []string{"task-owned-first", "task-owned-second"} {
		params, err := structpb.NewStruct(map[string]any{
			"subject_tags": []any{"binance_spot"}, "frequency": "1m", "output_fields": []any{[]string{"close", "volume"}[index]},
		})
		require.NoError(t, err)
		rsp, err := service.CreateTask(ctx, &pb.CreateTaskReq{Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskName: taskName, DataType: "kline", CollectParams: params,
		}})
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		taskID := rsp.GetTaskId()
		require.NotEmpty(t, taskID)
		stored, err := db.Tasks().GetByTaskID(ctx, "crypto", taskID)
		require.NoError(t, err)
		require.Equal(t, taskresult.ResultIDs(taskID).DatasetID, stored.ResultDatasetID)
		require.Equal(t, stored.ResultDatasetID, mustParseCollectParams(t, *stored).TargetDatasetID)
		require.Equal(t, taskID, metadata.datasets[stored.ResultDatasetID].GetAttributes()["collector_task_id"])
		datasets = append(datasets, stored.ResultDatasetID)
		taskIDs = append(taskIDs, taskID)
	}
	require.NotEqual(t, taskIDs[0], taskIDs[1])
	require.NotEqual(t, datasets[0], datasets[1])
	require.Len(t, metadata.datasets, 2, "each task provisions its own result Dataset instead of sharing the snapshot target")
}

func TestUpdateTaskRejectsResultDatasetReplacement(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	existing := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-owned", TaskName: "owned result", DataType: "kline", Enabled: true,
		CollectParams:   `{"target_dataset_id":"dataset-original","frequency":"1m"}`,
		ResultDatasetID: "dataset-original", PrepareState: domain.PrepareStateReady,
	}
	require.NoError(t, db.Tasks().Create(ctx, existing))
	before, err := db.Tasks().GetByTaskID(ctx, existing.SpaceID, existing.TaskID)
	require.NoError(t, err)
	params, err := structpb.NewStruct(map[string]any{"target_dataset_id": "dataset-other", "frequency": "1m"})
	require.NoError(t, err)
	service := &Service{taskRepo: db.Tasks(), datasetSrc: acceptingKlineDatasetSource{}}
	rsp, err := service.UpdateTask(ctx, &pb.UpdateTaskReq{
		SpaceId: existing.SpaceID, TaskId: existing.TaskID,
		Task: &pb.CollectionTask{TaskName: existing.TaskName, DataType: existing.DataType, CollectParams: params},
	})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "create a new task")
	after, err := db.Tasks().GetByTaskID(ctx, existing.SpaceID, existing.TaskID)
	require.NoError(t, err)
	require.Equal(t, before, after, "a rejected target change must not alter the owning Dataset or persisted task")
}

type acceptingKlineDatasetSource struct{}

func (acceptingKlineDatasetSource) GetDataset(_ context.Context, _ string, _ string) (storagesource.DatasetInfo, error) {
	return storagesource.DatasetInfo{
		DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m",
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
		SpaceID: "crypto", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","frequency":"1m"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)
	_, _, err = service.resolveSubjectTags(context.Background(), task, params, "")
	require.ErrorContains(t, err, "请选择标的标签")
}

func TestResolveSubjectTagsRejectsEmptyStorageSubjectResolution(t *testing.T) {
	service := &Service{datasetSrc: emptySubjectsDatasetSource{}}
	task := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["binance_spot"],"frequency":"1m"}`,
	}
	params, err := domain.ParseCollectParams(task.CollectParams, "", "", task.DataType)
	require.NoError(t, err)
	_, _, err = service.resolveSubjectTags(context.Background(), task, params, "")
	require.ErrorContains(t, err, "所选标签没有有效标的")
}

func TestResolveSubjectTagsResampleInheritsSourceTags(t *testing.T) {
	source := validationDatasetSource{
		"source": {SubjectTags: []string{"source_tag"}},
	}
	service := &Service{datasetSrc: source}

	explicit := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline_resample", CollectParams: `{"provider":"moox","market_type":"spot","subject_tags":["explicit_tag"],"source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	explicitParams, err := domain.ParseCollectParams(explicit.CollectParams, "", "", explicit.DataType)
	require.NoError(t, err)
	_, _, err = service.resolveSubjectTags(context.Background(), explicit, explicitParams, "")
	require.ErrorContains(t, err, "不能单独选择标签")

	matching := explicit
	matching.CollectParams = strings.Replace(explicit.CollectParams, `"explicit_tag"`, `"source_tag"`, 1)
	matchingParams, err := domain.ParseCollectParams(matching.CollectParams, "", "", matching.DataType)
	require.NoError(t, err)
	clean, tags, err := service.resolveSubjectTags(context.Background(), matching, matchingParams, "")
	require.NoError(t, err)
	require.Equal(t, []string{"source_tag"}, tags)
	require.NotContains(t, clean, "subject_tags")

	inherited := explicit
	inherited.CollectParams = strings.Replace(explicit.CollectParams, `"subject_tags":["explicit_tag"],`, "", 1)
	inheritedParams, err := domain.ParseCollectParams(inherited.CollectParams, "", "", inherited.DataType)
	require.NoError(t, err)
	clean, tags, err = service.resolveSubjectTags(context.Background(), inherited, inheritedParams, "")
	require.NoError(t, err)
	require.Equal(t, []string{"source_tag"}, tags)
	require.NotContains(t, clean, "subject_tags")
}

func TestCreateTaskProvisionsExclusiveResult(t *testing.T) {
	db := openCollectorTestStore(t)
	metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}}
	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
		datasetSrc:    acceptingKlineDatasetSource{},
	}

	rsp, callErr := service.CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskName: "  Binance 现货 K 线  ", DataType: "kline",
			CollectParams: params,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	require.Len(t, rsp.GetTaskId(), 20)
	require.Regexp(t, `^[a-z0-9]+$`, rsp.GetTaskId())

	stored, getErr := db.Tasks().GetByTaskID(context.Background(), "crypto", rsp.GetTaskId())
	require.NoError(t, getErr)
	require.Equal(t, "Binance 现货 K 线", stored.TaskName)
	expected := taskresult.ResultIDs(rsp.GetTaskId())
	require.Equal(t, expected.DatasetID, stored.ResultDatasetID)
	require.Contains(t, metadata.datasets, expected.DatasetID)
	datasetCountBeforeDuplicate := len(metadata.datasets)
	require.Equal(t, "collector", metadata.datasets[expected.DatasetID].GetAttributes()["owner_module"])
	require.Equal(t, "raw_collection", metadata.datasets[expected.DatasetID].GetAttributes()["dataset_role"])
	require.Equal(t, rsp.GetTaskId(), metadata.datasets[expected.DatasetID].GetAttributes()["collector_task_id"])
	require.Equal(t, []string{"binance_spot"}, metadata.datasets[expected.DatasetID].GetSubjectTags())
	require.NotContains(t, stored.CollectParams, "subject_tags")

	duplicateParams, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m",
	})
	require.NoError(t, err)
	duplicate, duplicateErr := service.CreateTask(context.Background(), &pb.CreateTaskReq{Task: &pb.CollectionTask{
		SpaceId: "crypto", TaskName: "重复逻辑视图", DataType: "kline", CollectParams: duplicateParams,
	}})
	require.NoError(t, duplicateErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, duplicate.GetRetInfo().GetCode())
	require.ErrorContains(t, fmt.Errorf("%s", duplicate.GetRetInfo().GetMsg()), "same tag")
	require.Len(t, metadata.datasets, datasetCountBeforeDuplicate, "duplicate must be rejected before creating its Dataset")
}

func TestUpdateTaskRejectsChangingSubjectTags(t *testing.T) {
	db := openCollectorTestStore(t)
	ids := taskresult.ResultIDs("task-tags")
	metadata := newTaskResultMetadataFake(ids, "task-tags")
	metadata.datasets[ids.DatasetID].SubjectTags = []string{"old_tag"}
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-tags", TaskName: "标签任务", DataType: "kline", CollectParams: fmt.Sprintf(`{"provider":"binance","market_type":"spot","target_dataset_id":%q,"frequency":"1m"}`, ids.DatasetID),
		Enabled: true, ResultDatasetID: ids.DatasetID,
	}))

	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"new_tag"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
		datasetSrc: validationDatasetSource{
			ids.DatasetID: {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m", SubjectTags: []string{"old_tag"}, Attributes: map[string]string{"market_type": "spot"}},
		},
	}

	rsp, callErr := service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		SpaceId: "crypto", TaskId: "task-tags",
		Task: &pb.CollectionTask{SpaceId: "crypto", TaskId: "task-tags", TaskName: "标签任务", DataType: "kline", CollectParams: params},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "范围不可修改")

	updated, err := db.Tasks().GetByTaskID(context.Background(), "crypto", "task-tags")
	require.NoError(t, err)
	require.NotContains(t, updated.CollectParams, "subject_tags")
	require.Equal(t, []string{"old_tag"}, metadata.datasets[ids.DatasetID].GetSubjectTags())
}

func TestCreateTaskCompensatesResultWhenDatasetValidationFails(t *testing.T) {
	db := openCollectorTestStore(t)
	metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}}
	params, err := structpb.NewStruct(map[string]any{
		"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m",
	})
	require.NoError(t, err)
	service := &Service{
		taskRepo:      db.Tasks(),
		resultManager: taskresult.NewManagerWithAPI(metadata),
		datasetSrc:    failingDatasetSource{},
	}

	rsp, callErr := service.CreateTask(context.Background(), &pb.CreateTaskReq{
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskName: "补偿任务", DataType: "kline",
			CollectParams: params,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Empty(t, metadata.datasets)
	_, getErr := db.Tasks().GetByTaskName(context.Background(), "crypto", "补偿任务")
	require.ErrorIs(t, getErr, gorm.ErrRecordNotFound)
}

func TestCreateTaskRejectsDuplicateNameWithinSpace(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
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
			CollectParams: params,
		},
	})
	require.NoError(t, callErr)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "task_name")
}

func TestUpdateTaskOnlyChangesMutableFields(t *testing.T) {
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "collector.db"), DefaultIngest: markets.DefaultIngest})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.ApplySchema(collectorschema.AllSQL()))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-1", TaskName: "original", Description: "before",
		DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-original","frequency":"1m"}`,
		Enabled: true, Creator: "creator", PrepareState: domain.PrepareStateReady,
		ResultDatasetID: "dataset-original",
	}))
	require.NoError(t, db.Tasks().Create(context.Background(), domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-2", TaskName: "occupied",
		DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-occupied","frequency":"1m"}`,
		Enabled: true, PrepareState: domain.PrepareStateReady,
		ResultDatasetID: "dataset-occupied",
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
				DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1m", SubjectTags: []string{"binance_spot"},
				Attributes: map[string]string{"market_type": "spot"},
			},
		},
	}
	rsp, callErr := service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		SpaceId: "crypto", TaskId: "task-1",
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: "task-1", TaskName: " renamed ", Description: "after",
			DataType: "kline", CollectParams: params, Enabled: &enabled,
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
	require.NotContains(t, updated.CollectParams, `"provider"`)
	require.NotContains(t, updated.CollectParams, `"market_type"`)
	require.Equal(t, "dataset-original", updated.ResultDatasetID)
	require.Equal(t, "creator", updated.Creator)

	rsp, callErr = service.UpdateTask(context.Background(), &pb.UpdateTaskReq{
		SpaceId: "crypto", TaskId: "task-1",
		Task: &pb.CollectionTask{
			SpaceId: "crypto", TaskId: "task-1", TaskName: "occupied",
			DataType: "kline"},
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
			DataType: "kline_resample", CollectParams: func() *structpb.Struct {
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

func TestValidateCollectionTaskUpdateAllowsOnlyMutableFields(t *testing.T) {
	base := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", TaskName: "original", DataType: "kline_resample", Enabled: true,
		CollectParams:   `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1h","source_series_tag":"venue:binance","target_dataset_id":"target","target_frequency":"4h","alignment":"epoch_utc"}`,
		ResultDatasetID: "target",
	}

	mutable := base
	mutable.TaskName = "renamed"
	mutable.Description = "updated"
	mutable.Enabled = false
	require.NoError(t, validateCollectionTaskUpdate(base, mutable))

	immutable := map[string]func(*domain.CollectionTask){
		"task_id":   func(task *domain.CollectionTask) { task.TaskID = "other" },
		"space_id":  func(task *domain.CollectionTask) { task.SpaceID = "other" },
		"data_type": func(task *domain.CollectionTask) { task.DataType = "kline" },
		"collect_params": func(task *domain.CollectionTask) {
			task.CollectParams = strings.Replace(task.CollectParams, `"4h"`, `"1h"`, 1)
		},
		"result_dataset": func(task *domain.CollectionTask) { task.ResultDatasetID = "other" },
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
		SpaceID: "crypto", TaskID: "task-legacy-tags", TaskName: "legacy tags", DataType: "kline", CollectParams: `{"provider":"binance","market_type":"spot","subject_tags":["old_tag"],"target_dataset_id":"dataset-result","frequency":"1m"}`,
		ResultDatasetID: "dataset-result",
	}
	desired := existing
	desired.CollectParams = `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-result","frequency":"1m"}`

	require.NoError(t, validateCollectionTaskUpdate(existing, desired))
}

// 每函数标的数与允许迟到是可变的运维属性，序列范围（频率等）仍不可改。
func TestValidateCollectionTaskUpdateAllowsPlanOptionChanges(t *testing.T) {
	existing := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-handoff", TaskName: "handoff", DataType: "kline", ResultDatasetID: "dataset-result",
		CollectParams: `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-result","frequency":"1m"}`,
	}
	desired := existing
	desired.CollectParams = `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-result","frequency":"1m","symbols_per_function":50,"allowed_lateness_seconds":15}`
	require.NoError(t, validateCollectionTaskUpdate(existing, desired))

	desired.CollectParams = `{"provider":"binance","market_type":"spot","target_dataset_id":"dataset-result","frequency":"5m"}`
	require.ErrorContains(t, validateCollectionTaskUpdate(existing, desired), "create a new task")
}

func TestValidateResampleSourceDoesNotFoldMonthIntoMinute(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"source": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1mo", Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", DataType: "kline_resample", CollectParams: `{"provider":"moox","market_type":"spot","source_dataset_id":"source","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"dataset_spot_kline_derived_5m","target_frequency":"5m","alignment":"epoch_utc"}`,
	}
	require.ErrorContains(t, service.validateCollectionTaskDatasets(context.Background(), rule), `freq is "1mo", not "1m"`)
}

func TestValidateCollectionTaskDatasetsAcceptsExchangeSourceForMooxResample(t *testing.T) {
	service := &Service{datasetSrc: validationDatasetSource{
		"source": {DataSourceID: "binance", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Status: "active", Freq: "1h", Attributes: map[string]string{"market_type": "spot"}},
	}}
	rule := domain.CollectionTask{
		SpaceID: "crypto", TaskID: "resample-1", DataType: "kline_resample", CollectParams: `{"source_dataset_id":"source","source_frequency":"1h","source_series_tag":"venue:binance","target_dataset_id":"dataset_target","target_frequency":"4h"}`,
	}
	require.NoError(t, service.validateCollectionTaskDatasets(context.Background(), rule))
}

func TestCollectionTaskDefinitionHashCanonicalizesKlineDefinition(t *testing.T) {
	left := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", TagIDs: []string{"tag-b", "tag-a"},
		CollectParams: `{"provider":"binance","market_type":"spot","market_id":"crypto","instrument_type":"spot","source_id":"spot_http","series_tag":"venue:binance","target_dataset_id":"dataset-a","frequency":"1h","output_fields":["volume","close"]}`,
	}
	right := domain.CollectionTask{
		SpaceID: "crypto", DataType: "kline", TagIDs: []string{"tag-a", "tag-b"},
		CollectParams: `{"provider":"okx","market_type":"swap","market_id":"other","instrument_type":"swap","source_id":"swap_http","series_tag":"venue:okx","target_dataset_id":"dataset-b","frequency":"1h","output_fields":["close","volume"]}`,
	}
	require.Equal(t, CollectionTaskDefinitionHash(left), CollectionTaskDefinitionHash(right))

	changed := right
	changed.CollectParams = `{"frequency":"1h","output_fields":["close"]}`
	require.NotEqual(t, CollectionTaskDefinitionHash(left), CollectionTaskDefinitionHash(changed))
}

func TestStripCollectionTaskRoutingRemovesAllKlineRouteOverrides(t *testing.T) {
	clean, err := stripCollectionTaskRouting(`{"provider":"binance","market_type":"spot","market_id":"crypto","instrument_type":"spot","source_id":"spot_http","series_tag":"venue:binance","frequency":"1m","output_fields":["close"]}`)
	require.NoError(t, err)
	for _, forbidden := range []string{"provider", "market_type", "market_id", "instrument_type", "source_id", "series_tag"} {
		require.NotContains(t, clean, `"`+forbidden+`"`)
	}
	require.Contains(t, clean, `"frequency":"1m"`)
	require.Contains(t, clean, `"output_fields":["close"]`)
}

func TestDeleteTaskRefusesResultDatasetStillReferencedByAnotherTask(t *testing.T) {
	db := openCollectorTestStore(t)
	ctx := context.Background()
	owner := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-owner", TaskName: "owner", DataType: "kline", Enabled: true}
	ids := taskresult.ResultIDs(owner.TaskID)
	owner.ResultDatasetID = ids.DatasetID
	require.NoError(t, db.Tasks().Create(ctx, owner))
	// 手工写入的运行态让另一个任务也指向同一个结果数据集：删除前的保护必须拒绝。
	other := domain.CollectionTask{SpaceID: "crypto", TaskID: "task-other", TaskName: "other", DataType: "kline", Enabled: true}
	require.NoError(t, db.Tasks().Create(ctx, other))
	sqlDB, sqlErr := db.SQLDB()
	require.NoError(t, sqlErr)
	_, sqlErr = sqlDB.ExecContext(ctx, `UPDATE t_collector_tasks SET c_result_dataset_id = ? WHERE c_task_id = ?`, ids.DatasetID, other.TaskID)
	require.NoError(t, sqlErr)

	metadata := newTaskResultMetadataFake(ids, owner.TaskID)
	service := &Service{persistence: db, taskRepo: db.Tasks(), resultManager: taskresult.NewManagerWithAPI(metadata)}
	rsp, err := service.DeleteTask(ctx, &pb.DeleteTaskReq{SpaceId: owner.SpaceID, TaskId: owner.TaskID, DeleteResultData: true})
	require.NoError(t, err)
	require.Equal(t, pb.ErrorCode_INNER_ERR, rsp.GetRetInfo().GetCode())
	require.Contains(t, rsp.GetRetInfo().GetMsg(), "still referenced by another task")
	require.Equal(t, 0, metadata.deleteDatasets)
	stored, err := db.Tasks().GetByTaskID(ctx, owner.SpaceID, owner.TaskID)
	require.NoError(t, err)
	require.True(t, stored.Enabled, "preflight rejection must leave the task enabled")
}

type tagValidationSource struct {
	validationDatasetSource
	tags map[string]*storagepb.Tag
}

func (s tagValidationSource) GetTag(_ context.Context, _, tagID string) (*storagepb.Tag, error) {
	if tag, ok := s.tags[tagID]; ok {
		return tag, nil
	}
	return nil, fmt.Errorf("tag %s not found", tagID)
}

func TestValidateCollectionTaskTagRoutes(t *testing.T) {
	service := &Service{datasetSrc: tagValidationSource{tags: map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
		"binance_swap": {TagId: "binance_swap", Attributes: map[string]string{"sync.source": "binance", "market_type": "swap"}},
		"cn_a_share":   {TagId: "cn_a_share", Attributes: map[string]string{"sync.source": "eastmoney", "market_type": "equity"}},
	}}}
	params := `{"target_dataset_id":"bars","frequency":"1m"}`
	task := domain.CollectionTask{SpaceID: "crypto", DataType: "kline", CollectParams: params}

	// 现货加永续不能放进同一个任务：要拆成两个任务（各一个数据集）。
	task.TagIDs = []string{"binance_spot", "binance_swap"}
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), task), "现货与永续请拆成两个任务")

	// 数据源不同的标签不能放进同一个任务。
	service.datasetSrc.(tagValidationSource).tags["okx_swap"] = &storagepb.Tag{TagId: "okx_swap", Attributes: map[string]string{"sync.source": "okx", "market_type": "swap"}}
	task.TagIDs = []string{"binance_spot", "okx_swap"}
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), task), "数据源不同")

	for _, tagID := range []string{"binance_spot", "cn_a_share"} {
		task.TagIDs = []string{tagID}
		require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task), tagID)
	}

	// 同一来源与市场的多个标签通过。
	service.datasetSrc.(tagValidationSource).tags["binance_spot_2"] = &storagepb.Tag{TagId: "binance_spot_2", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}}
	task.TagIDs = []string{"binance_spot", "binance_spot_2"}
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task))

	// 频率按注入的检查函数（生产里查行情源目录与路由文件）校验，拒绝的原因原样带回；市场 ID 取空间。
	var calls []string
	service.checkKlineFrequency = func(marketID, marketType, providerID, frequency string) error {
		calls = append(calls, marketID+"/"+marketType+"/"+providerID+"/"+frequency)
		if frequency == "4h" {
			return fmt.Errorf("不支持 4h K 线")
		}
		return nil
	}
	task.TagIDs = []string{"binance_spot"}
	task.CollectParams = `{"target_dataset_id":"bars","frequency":"4h"}`
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), task), "不支持 4h K 线")
	task.CollectParams = `{"target_dataset_id":"bars","frequency":"1mo"}`
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task))
	require.Equal(t, []string{"crypto/spot/binance/4h", "crypto/spot/binance/1mo"}, calls)
}

// 资金费率：只支持 1h、1d；标签必须是永续；行情源要具备这项能力。
func TestValidateCollectionTaskTagRoutesFundingRate(t *testing.T) {
	tags := map[string]*storagepb.Tag{
		"binance_spot": {TagId: "binance_spot", Attributes: map[string]string{"sync.source": "binance", "market_type": "spot"}},
		"binance_swap": {TagId: "binance_swap", Attributes: map[string]string{"sync.source": "binance", "market_type": "swap"}},
	}
	offers := marketdata.FieldOffers{{MarketID: "crypto", Instrument: "swap", ProviderID: "binance",
		Spec: marketdata.FieldSpec{ID: "funding_rate", Type: marketdata.FieldTypeDouble, Fill: "zero", Frequencies: []string{"1h", "1d"}}}}
	service := &Service{datasetSrc: tagValidationSource{tags: tags}, fieldOffers: offers}
	withFunding := func(frequency string) string {
		return `{"target_dataset_id":"bars","frequency":"` + frequency + `","output_fields":["close","funding_rate"]}`
	}
	task := domain.CollectionTask{SpaceID: "crypto", DataType: "kline", TagIDs: []string{"binance_swap"}}
	for _, frequency := range []string{"1h", "1d"} {
		task.CollectParams = withFunding(frequency)
		require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task), frequency)
	}
	for _, frequency := range []string{"1m", "5m", "30m", "1w", "1mo"} {
		task.CollectParams = withFunding(frequency)
		require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), task), "只支持 1h、1d", frequency)
	}

	task.CollectParams = withFunding("1h")
	task.TagIDs = []string{"binance_spot"}
	require.ErrorContains(t, service.validateCollectionTaskTagRoutes(context.Background(), task), "标签 binance_spot 的市场 spot 下没有来源", "只有永续有来源")
	// 不选这个字段时行为不变。
	task.CollectParams = `{"target_dataset_id":"bars","frequency":"1m"}`
	require.NoError(t, service.validateCollectionTaskTagRoutes(context.Background(), task))
}

// 建任务不填采集方式时等于市场默认值（crypto 是 stream）；填了就用填的；旧取值与任务参数里的旧字段都被拒绝。
func TestCreateTaskIngestDefaultsToTheMarketValue(t *testing.T) {
	ctx := context.Background()
	create := func(t *testing.T, dataType, ingest string, extra map[string]any) (*Service, *taskResultMetadataFake, *pb.CreateTaskRsp) {
		t.Helper()
		metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}}
		service := &Service{
			taskRepo:      openCollectorTestStore(t).Tasks(),
			resultManager: taskresult.NewManagerWithAPI(metadata),
			datasetSrc:    acceptingKlineDatasetSource{},
		}
		values := map[string]any{"provider": "binance", "market_type": "spot", "subject_tags": []any{"binance_spot"}, "frequency": "1m"}
		for key, value := range extra {
			values[key] = value
		}
		params, err := structpb.NewStruct(values)
		require.NoError(t, err)
		rsp, err := service.CreateTask(ctx, &pb.CreateTaskReq{Task: &pb.CollectionTask{SpaceId: "crypto", TaskName: "现货 K 线", DataType: dataType, Ingest: ingest, CollectParams: params}})
		require.NoError(t, err)
		return service, metadata, rsp
	}
	ingestOf := func(t *testing.T, service *Service, rsp *pb.CreateTaskRsp) string {
		t.Helper()
		require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
		detail, err := service.GetTaskDetail(ctx, &pb.GetTaskDetailReq{SpaceId: "crypto", TaskId: rsp.GetTaskId()})
		require.NoError(t, err)
		return detail.GetTask().GetIngest()
	}

	service, _, rsp := create(t, "kline", "", nil)
	require.Equal(t, "stream", ingestOf(t, service, rsp), "不填用 crypto 的市场默认值")
	service, _, rsp = create(t, "kline", " SCF ", nil)
	require.Equal(t, "scf", ingestOf(t, service, rsp))
	// 数据类型大小写不一样也是 K 线任务：存成小写，采集方式照样取市场默认值。
	service, _, rsp = create(t, " Kline ", "", nil)
	require.Equal(t, "stream", ingestOf(t, service, rsp))
	stored, err := service.taskRepo.GetByTaskID(ctx, "crypto", rsp.GetTaskId())
	require.NoError(t, err)
	require.Equal(t, domain.DataTypeKline, stored.DataType)
	require.Equal(t, domain.IngestStreaming, stored.Ingest)

	_, metadata, rsp := create(t, "kline", "pull", nil)
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "旧取值 pull 不再接受")
	require.Empty(t, metadata.datasets, "被拒绝的请求不准备结果数据集")
	_, metadata, rsp = create(t, "kline", "", map[string]any{"ingest": "stream", "executor": "scf"})
	require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode(), "“推送加云函数补采”无法表达")
	require.Empty(t, metadata.datasets)
}

// 合成任务在 waiting_view（目标数据集尚未建立）与 ready（已由 resample Catalog 建列）状态下缩减 output_fields，
// UpdateTask 均不得调用 resultManager.EnsureOutputFields（避免未就绪被拦截或覆写 amount 等可选列的 Required=false）。
func TestUpdateTaskResampleOutputFieldsReductionSkipsResultManagerEnsure(t *testing.T) {
	ctx := context.Background()
	sourceInfo := storagesource.DatasetInfo{
		DataSourceID: "eastmoney",
		DataKind:     storagepb.DataKind_DATA_KIND_TIME_SERIES,
		Status:       "active",
		Freq:         "1m",
		Alignment:    "session",
		SubjectTags:  []string{"cn_a_share"},
		Attributes:   map[string]string{"calendar": "cn_stock", "market_type": "equity"},
		Columns:      []string{"open", "high", "low", "close", "volume", "amount", "quote_volume"},
		ColumnTypes: map[string]storagepb.FieldValueType{
			"open":         storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			"high":         storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			"low":          storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			"close":        storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			"volume":       storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			"amount":       storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			"quote_volume": storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		},
	}

	cases := []struct {
		name         string
		taskID       string
		prepareState domain.CollectionTaskPrepareState
		targetExists bool
		initial      []string
		reduced      []string
	}{
		{
			name:         "waiting_view_before_target_created",
			taskID:       "resample-waiting",
			prepareState: domain.PrepareStateWaitingView,
			targetExists: false,
			initial:      []string{"open", "high", "low", "close", "volume", "amount"},
			reduced:      []string{"open", "high", "low", "close", "volume"},
		},
		{
			name:         "ready_preserves_optional_column_required_flag",
			taskID:       "resample-ready",
			prepareState: domain.PrepareStateReady,
			targetExists: true,
			initial:      []string{"open", "high", "low", "close", "volume", "amount", "quote_volume"},
			reduced:      []string{"open", "high", "low", "close", "volume", "amount"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openCollectorTestStore(t)
			taskID := tc.taskID
			targetID := taskresult.ResultIDs(taskID).DatasetID
			initialParams := domain.CollectParams{
				SourceDatasetID: "source_kline",
				SourceFrequency: "1m",
				SourceSeriesTag: "market:equity",
				TargetDatasetID: targetID,
				TargetFrequency: "5m",
				OutputFields:    tc.initial,
			}
			rawInitial, err := initialParams.CanonicalJSON()
			require.NoError(t, err)
			require.NoError(t, db.Tasks().Create(ctx, domain.CollectionTask{
				SpaceID:         "stockcn",
				TaskID:          taskID,
				TaskName:        "重采样-" + tc.name,
				DataType:        "kline_resample",
				CollectParams:   rawInitial,
				Enabled:         true,
				PrepareState:    tc.prepareState,
				ResultDatasetID: targetID,
			}))

			metadata := &taskResultMetadataFake{datasets: map[string]*storagepb.Dataset{}}
			if tc.targetExists {
				metadata.datasets[targetID] = &storagepb.Dataset{
					SpaceId: "stockcn", DatasetId: targetID, Status: "active",
					Attributes: map[string]string{"owner_module": "collector", "collector_task_id": taskID},
				}
			}
			service := &Service{
				persistence:   db,
				taskRepo:      db.Tasks(),
				resultManager: taskresult.NewManagerWithAPI(metadata),
				datasetSrc:    validationDatasetSource{"source_kline": sourceInfo},
			}

			reducedAny := make([]any, len(tc.reduced))
			for i, f := range tc.reduced {
				reducedAny[i] = f
			}
			reqParams, err := structpb.NewStruct(map[string]any{
				"source_dataset_id": "source_kline",
				"source_frequency":  "1m",
				"source_series_tag": "market:equity",
				"target_frequency":  "5m",
				"output_fields":     reducedAny,
			})
			require.NoError(t, err)

			rsp, callErr := service.UpdateTask(ctx, &pb.UpdateTaskReq{
				SpaceId: "stockcn",
				TaskId:  taskID,
				Task: &pb.CollectionTask{
					SpaceId:       "stockcn",
					TaskId:        taskID,
					TaskName:      "重采样-" + tc.name,
					DataType:      "kline_resample",
					CollectParams: reqParams,
				},
			})
			require.NoError(t, callErr)
			require.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode(), rsp.GetRetInfo().GetMsg())
			require.Zero(t, metadata.upsertColumns, "合成任务缩减输出字段不得调用 EnsureOutputFields / UpsertDatasetColumn")

			updated, err := db.Tasks().GetByTaskID(ctx, "stockcn", taskID)
			require.NoError(t, err)
			require.Equal(t, tc.prepareState, updated.PrepareState)
			parsed, err := domain.ParseCollectParams(updated.CollectParams, "", "", updated.DataType)
			require.NoError(t, err)
			require.ElementsMatch(t, tc.reduced, parsed.OutputFields)
		})
	}
}
