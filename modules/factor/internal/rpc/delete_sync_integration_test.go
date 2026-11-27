package rpc

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/factor/internal/catalog"
	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/enginehub"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
)

type syncMetaFake struct {
	datasets map[string]storageio.DatasetInfo
	columns  map[string][]storageio.ColumnInfo
}

func newSyncMetaFake() *syncMetaFake {
	return &syncMetaFake{
		datasets: map[string]storageio.DatasetInfo{
			"crypto/dataset_src": {
				SpaceID: "crypto", DatasetID: "dataset_src", DataSourceID: "ds_binance", Name: "行情源",
				DataKind: storageio.DataKindTimeSeries, Freq: "1m", Status: storageio.DatasetStatusActive,
			},
		},
		columns: map[string][]storageio.ColumnInfo{
			"crypto/dataset_src": {{ColumnName: "close", ValueType: storageio.ColumnTypeDouble}},
		},
	}
}

func (m *syncMetaFake) GetDataset(_ context.Context, spaceID, datasetID string) (storageio.DatasetInfo, error) {
	ds, ok := m.datasets[spaceID+"/"+datasetID]
	if !ok {
		return storageio.DatasetInfo{}, errors.New("dataset not found")
	}
	return ds, nil
}

func (m *syncMetaFake) ListColumns(_ context.Context, spaceID, datasetID string) ([]storageio.ColumnInfo, error) {
	return m.columns[spaceID+"/"+datasetID], nil
}

func (m *syncMetaFake) CreateResultDataset(_ context.Context, spec storageio.ResultDatasetSpec) error {
	key := spec.SpaceID + "/" + spec.DatasetID
	m.datasets[key] = storageio.DatasetInfo{
		SpaceID: spec.SpaceID, DatasetID: spec.DatasetID, DataSourceID: spec.DataSourceID, Name: spec.Name,
		DataKind: spec.DataKind, Freq: spec.Frequency, Status: storageio.DatasetStatusActive,
		SubjectTags: append([]string(nil), spec.SubjectTags...), Attributes: spec.Attributes,
	}
	m.columns[key] = append([]storageio.ColumnInfo(nil), spec.Columns...)
	return nil
}

func (m *syncMetaFake) UpsertColumns(_ context.Context, spaceID, datasetID string, cols []storageio.ColumnInfo) error {
	m.columns[spaceID+"/"+datasetID] = append(m.columns[spaceID+"/"+datasetID], cols...)
	return nil
}

func (m *syncMetaFake) DeleteDataset(_ context.Context, spaceID, datasetID string) error {
	delete(m.datasets, spaceID+"/"+datasetID)
	delete(m.columns, spaceID+"/"+datasetID)
	return nil
}

func (m *syncMetaFake) SetDatasetSubjectTags(_ context.Context, spaceID, datasetID string, tags []string) error {
	key := spaceID + "/" + datasetID
	ds := m.datasets[key]
	ds.SubjectTags = append([]string(nil), tags...)
	m.datasets[key] = ds
	return nil
}

// 端到端验证 DeleteFactorTask（先停用再删除）与 DeleteBoundTask 都会刷新 SyncEngineCatalog 的 catalog_hash
// 与 existing_task_ids，确保因子引擎及时发现任务被删除并清理本地 watermark 与 gapledger。
func TestDeleteTaskAndDeleteBoundTaskPropagateToSyncEngineCatalog(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(&store.Options{Path: filepath.Join(t.TempDir(), "factor.db")})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	hub := enginehub.New(db)
	meta := newSyncMetaFake()
	cat := catalog.NewService(db, meta, catalog.WithReadyRecorder(hub.SetResultReady))
	mgrSvc := NewService(cat, &recalcFake{})
	engSvc := NewEngineService(hub, nil)

	_, err = cat.CreateFactor(ctx, domain.FactorDef{FactorID: "bias", Name: "乖离", Spec: "output: close\n"})
	require.NoError(t, err)
	_, err = cat.CreateFactor(ctx, domain.FactorDef{
		FactorID: "varbias",
		Name:     "可变乖离",
		Spec:     "params:\n  window: {type: int, min: 1, max: 10, default: 2}\noutput: ts_mean(close, window)\n",
	})
	require.NoError(t, err)

	for _, task := range []domain.FactorTask{
		{TaskID: "ftask_plain", SpaceID: "crypto", Name: "普通任务", SourceDatasetID: "dataset_src", Freq: "1m"},
		{TaskID: "ftask_bound", SpaceID: "crypto", Name: "绑定任务", SourceDatasetID: "dataset_src", Freq: "1m"},
	} {
		_, err := cat.CreateTask(ctx, task)
		require.NoError(t, err)
		_, err = cat.AddTaskMember(ctx, task.TaskID, "bias", domain.MemberKindFixed, nil)
		require.NoError(t, err)
		_, err = cat.SetTaskMemberStatus(ctx, task.TaskID, "bias", domain.MemberStatusEnabled)
		require.NoError(t, err)
		if task.TaskID == "ftask_bound" {
			member, err := cat.AddTaskMember(ctx, task.TaskID, "varbias", domain.MemberKindVar, nil)
			require.NoError(t, err)
			_, err = cat.SetTaskMemberStatus(ctx, task.TaskID, member.MemberKey, domain.MemberStatusEnabled)
			require.NoError(t, err)
		}
		_, err = cat.SetTaskStatus(ctx, task.TaskID, domain.TaskStatusEnabled)
		require.NoError(t, err)
	}
	_, err = cat.BindTask(ctx, "ftask_bound", "inst-1")
	require.NoError(t, err)

	// 初始同步：两个任务都在线且在 existing_task_ids 中。
	rsp1, err := engSvc.SyncEngineCatalog(ctx, &factorpb.SyncEngineCatalogReq{Engine: testEngine})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, rsp1.GetRetInfo().GetCode())
	require.False(t, rsp1.GetNotModified())
	require.ElementsMatch(t, []string{"ftask_plain", "ftask_bound"}, rsp1.GetExistingTaskIds())
	require.Len(t, rsp1.GetTasks(), 2)

	// 1. 停用 ftask_plain：从 tasks 移除，但仍保留在 existing_task_ids 中。
	_, err = cat.SetTaskStatus(ctx, "ftask_plain", domain.TaskStatusDisabled)
	require.NoError(t, err)
	rsp2, err := engSvc.SyncEngineCatalog(ctx, &factorpb.SyncEngineCatalogReq{Engine: testEngine, KnownHash: rsp1.GetCatalogHash()})
	require.NoError(t, err)
	require.False(t, rsp2.GetNotModified())
	require.Len(t, rsp2.GetTasks(), 1)
	require.ElementsMatch(t, []string{"ftask_plain", "ftask_bound"}, rsp2.GetExistingTaskIds())

	// 2. DeleteFactorTask 删除已停用的 ftask_plain：catalog_hash 变化，existing_task_ids 移除 ftask_plain。
	_, err = cat.SetTaskMemberStatus(ctx, "ftask_plain", "bias", domain.MemberStatusDisabled)
	require.NoError(t, err)
	require.NoError(t, cat.RemoveTaskMember(ctx, "ftask_plain", "bias"))
	delPlain, err := mgrSvc.DeleteFactorTask(ctx, &factorpb.DeleteFactorTaskReq{TaskId: "ftask_plain", Purge: true})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, delPlain.GetRetInfo().GetCode())

	rsp3, err := engSvc.SyncEngineCatalog(ctx, &factorpb.SyncEngineCatalogReq{Engine: testEngine, KnownHash: rsp2.GetCatalogHash()})
	require.NoError(t, err)
	require.False(t, rsp3.GetNotModified(), "删除已停用任务后同步必须返回新快照而非 NotModified")
	require.Equal(t, []string{"ftask_bound"}, rsp3.GetExistingTaskIds())

	// 3. DeleteBoundTask 删除绑定任务 ftask_bound：catalog_hash 变化，tasks 和 existing_task_ids 均为空。
	delBound, err := mgrSvc.DeleteBoundTask(ctx, &factorpb.DeleteBoundTaskReq{TaskId: "ftask_bound", BoundInstanceId: "inst-1"})
	require.NoError(t, err)
	require.Equal(t, commonpb.ErrorCode_SUCCESS, delBound.GetRetInfo().GetCode())

	rsp4, err := engSvc.SyncEngineCatalog(ctx, &factorpb.SyncEngineCatalogReq{Engine: testEngine, KnownHash: rsp3.GetCatalogHash()})
	require.NoError(t, err)
	require.False(t, rsp4.GetNotModified())
	require.True(t, rsp4.GetHasExistingTaskIds())
	require.Empty(t, rsp4.GetTasks())
	require.Empty(t, rsp4.GetExistingTaskIds())
}
