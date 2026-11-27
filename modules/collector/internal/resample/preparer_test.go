package resample

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/storagesource"
	"github.com/mooyang-code/moox/modules/collector/internal/planner/taskresult"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type countingSubjectSource struct {
	getDatasetCalls      atomic.Int32
	resolveSubjectsCalls atomic.Int32
	info                 storagesource.DatasetInfo
	subjects             []domain.Subject
}

func (s *countingSubjectSource) GetDataset(context.Context, string, string) (storagesource.DatasetInfo, error) {
	s.getDatasetCalls.Add(1)
	return s.info, nil
}

func (s *countingSubjectSource) ResolveSubjects(context.Context, string, []string) ([]domain.Subject, error) {
	s.resolveSubjectsCalls.Add(1)
	return s.subjects, nil
}

type countingViewState struct {
	inner catalogViewStateFake
	calls atomic.Int32
}

func (v *countingViewState) GetViewState(ctx context.Context, req *storagepb.GetViewStateReq) (*storagepb.GetViewStateRsp, error) {
	v.calls.Add(1)
	return v.inner.GetViewState(ctx, req)
}

// Preparer.RunOnce 不对已 Ready 的重采样任务发任何空转 RPC；
// 新建任务、待视图任务、失败重试任务以及状态回退为非 Ready 时仍能正常准备并切回 Ready。
func TestPreparerSkipsReadyTasksAndPreparesNonReadyTasks(t *testing.T) {
	ctx := context.Background()
	mgr := openState(t)
	metadata := newCatalogMetadataFake("")
	view := &countingViewState{inner: catalogViewStateFake{metadata: metadata}}
	source := &countingSubjectSource{
		info:     storagesource.DatasetInfo{DataSourceID: "binance", SubjectTags: []string{"binance_spot"}},
		subjects: []domain.Subject{{SubjectID: "BTC-USDT", Status: "active"}},
	}
	preparer := &Preparer{
		Tasks:   mgr.Tasks(),
		Source:  source,
		Catalog: &Catalog{Metadata: metadata, View: view},
		Limit:   50,
	}

	makeParams := func(taskID string) string {
		ids := taskresult.ResultIDs(taskID)
		return `{"provider":"moox","market_type":"spot","source_dataset_id":"dataset_src","source_frequency":"1m","source_series_tag":"venue:binance","target_dataset_id":"` + ids.DatasetID + `","target_frequency":"5m","alignment":"epoch_utc"}`
	}

	// 1. 仅存在已 Ready 任务：RunOnce 必须零次调用 Source / Metadata / View RPC。
	readyIDs := taskresult.ResultIDs("task-ready")
	require.NoError(t, mgr.Tasks().Create(ctx, domain.CollectionTask{
		SpaceID: "crypto", TaskID: "task-ready", TaskName: "已就绪合成", DataType: "kline_resample",
		CollectParams: makeParams("task-ready"), Enabled: true,
		PrepareState: domain.PrepareStateReady, ResultDatasetID: readyIDs.DatasetID,
	}))
	require.NoError(t, preparer.RunOnce(ctx))
	require.Zero(t, source.getDatasetCalls.Load(), "已 Ready 任务不应触发 GetDataset RPC")
	require.Zero(t, source.resolveSubjectsCalls.Load(), "已 Ready 任务不应触发 ResolveSubjects RPC")
	require.Zero(t, view.calls.Load(), "已 Ready 任务不应触发 GetViewState RPC")
	require.Zero(t, metadata.upsertColumns)

	// 2. 新建/失败的非 Ready 任务会被正常准备并推进至 Ready。
	for _, tc := range []struct {
		id    string
		state domain.CollectionTaskPrepareState
	}{
		{id: "task-pending", state: domain.PrepareStatePending},
		{id: "task-waiting", state: domain.PrepareStateWaitingView},
		{id: "task-error", state: domain.PrepareStateError},
	} {
		ids := taskresult.ResultIDs(tc.id)
		require.NoError(t, mgr.Tasks().Create(ctx, domain.CollectionTask{
			SpaceID: "crypto", TaskID: tc.id, TaskName: tc.id, DataType: "kline_resample",
			CollectParams: makeParams(tc.id), Enabled: true,
			PrepareState: tc.state, ResultDatasetID: ids.DatasetID,
		}))
	}
	require.NoError(t, preparer.RunOnce(ctx))
	require.EqualValues(t, 3, source.getDatasetCalls.Load(), "3 个非 Ready 任务各准备一次，跳过已 Ready 任务")
	for _, id := range []string{"task-pending", "task-waiting", "task-error"} {
		stored, err := mgr.Tasks().GetByTaskID(ctx, "crypto", id)
		require.NoError(t, err)
		require.Equal(t, domain.PrepareStateReady, stored.PrepareState)
	}

	// 3. 全部 Ready 后再次 RunOnce：零新增 RPC。
	beforeGet := source.getDatasetCalls.Load()
	beforeView := view.calls.Load()
	require.NoError(t, preparer.RunOnce(ctx))
	require.Equal(t, beforeGet, source.getDatasetCalls.Load())
	require.Equal(t, beforeView, view.calls.Load())

	// 4. 任务状态回到非 Ready（例如出错重试或重新准备）后再次 RunOnce：恢复准备并重新切回 Ready。
	require.NoError(t, mgr.Tasks().SetPrepareState(ctx, "crypto", "task-waiting", domain.PrepareStateWaitingView, "recheck"))
	require.NoError(t, preparer.RunOnce(ctx))
	require.Equal(t, beforeGet+1, source.getDatasetCalls.Load())
	stored, err := mgr.Tasks().GetByTaskID(ctx, "crypto", "task-waiting")
	require.NoError(t, err)
	require.Equal(t, domain.PrepareStateReady, stored.PrepareState)
}
