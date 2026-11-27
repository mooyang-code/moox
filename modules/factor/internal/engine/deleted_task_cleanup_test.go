package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorwire"
	"github.com/mooyang-code/moox/modules/factor/internal/gapledger"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/setlock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger/eventconsumer"
	"github.com/mooyang-code/moox/modules/factor/internal/watermark"
	"github.com/stretchr/testify/require"
)

type cleanupCatalogFake struct {
	snapshot CatalogSnapshot
}

func (f *cleanupCatalogFake) SyncCatalog(_ context.Context, knownHash string) (CatalogSnapshot, error) {
	if knownHash != "" && knownHash == f.snapshot.Hash {
		return CatalogSnapshot{Hash: f.snapshot.Hash, NotModified: true}, nil
	}
	return f.snapshot, nil
}

type cleanupParamManager struct {
	states map[string][]factorwire.MemberParamState
}

func (m *cleanupParamManager) GetTaskParams(_ context.Context, taskID string) ([]factorwire.MemberParamState, error) {
	return m.states[taskID], nil
}

type cleanupStorage struct {
	base time.Time
}

func (s cleanupStorage) GetDataset(context.Context, string, string) (storageio.DatasetInfo, error) {
	return storageio.DatasetInfo{Status: storageio.DatasetStatusActive, Retention: "forever"}, nil
}

func (s cleanupStorage) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return []storageio.ColumnInfo{{ColumnName: "close", ValueType: storageio.ColumnTypeDouble}}, nil
}

func (s cleanupStorage) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (s cleanupStorage) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"BTC"}, nil
}

func (s cleanupStorage) EarliestPeriod(context.Context, string, string, string, []string, []string) (time.Time, bool, error) {
	return s.base, true, nil
}

func (s cleanupStorage) ReadDatasetChanges(context.Context, string, string, *storageio.ChangeCursor, bool, int) (storageio.Changes, error) {
	return storageio.Changes{Next: storageio.ChangeCursor{StoreID: "store-1", Sequence: 1}}, nil
}

func (s cleanupStorage) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	out := make(map[string]*storageio.Frame, len(req.Subjects))
	for _, subject := range req.Subjects {
		var rows [][]any
		for i := 0; i < 6; i++ {
			at := s.base.Add(time.Duration(i) * time.Minute)
			if !req.Start.IsZero() && at.Before(req.Start) {
				continue
			}
			if !req.End.IsZero() && !at.Before(req.End) {
				continue
			}
			rows = append(rows, []any{at, "", float64(100 + i)})
		}
		out[subject] = &storageio.Frame{SubjectID: subject, Columns: []string{"data_time", "series_tag", "close"}, Rows: rows}
	}
	return out, nil
}

func cleanupTask(taskID, sourceDataset string) domain.EngineTask {
	def := domain.MustDefinition("bias", "bias", "params:\n  window: {type: int, min: 1, max: 10, default: 2}\noutput: ts_mean(close, window)\n")
	member := domain.MemberDef{Key: "bias__var", Kind: domain.MemberKindVar, Factor: def, Params: map[string]float64{"window": 2}}
	return domain.EngineTask{
		Set: domain.FactorTask{
			TaskID:          taskID,
			SpaceID:         "crypto",
			Name:            taskID,
			SourceDatasetID: sourceDataset,
			Freq:            "1m",
			ResultDatasetID: "dataset_factor_" + taskID,
			Status:          domain.TaskStatusEnabled,
		},
		Members:     []domain.MemberDef{member},
		ResultReady: true,
	}
}

// 停用任务只释放 localdata 窗口而保留水位/参数缓存；删除任务时，引擎在目录同步对账后同步清理本地落盘的
// watermarks.json、gapledger、param_cache.json、内存跟踪器以及不再被任何活跃任务引用的 localdata 源数据集，
// 同时不误删仍被其他活跃任务共享的源数据集。
func TestDeletedTaskSyncCleansWatermarkGapLedgerAndUnreferencedLocalData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)

	catalogCache, err := NewCatalogCache(filepath.Join(dir, "catalog.json"))
	require.NoError(t, err)
	wmPath := filepath.Join(dir, "watermarks.json")
	wmStore, err := watermark.Open(wmPath)
	require.NoError(t, err)
	gapDir := filepath.Join(dir, "gaps")
	ledger, _, err := gapledger.Open(gapDir, func() time.Time { return base }, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = ledger.Close() })

	seriesStore, err := seriesstore.Open(seriesstore.Options{Dir: filepath.Join(dir, "series"), BlockCacheBytes: 4 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = seriesStore.Close() })

	storageStub := cleanupStorage{base: base}
	data := localdata.New(localdata.Config{
		Store:   seriesStore,
		Storage: storageStub,
		Now:     func() time.Time { return base.Add(6 * time.Minute) },
	})
	t.Cleanup(data.Drain)

	cont, err := eventconsumer.NewContinuity(eventconsumer.ContinuityConfig{
		Store: wmStore,
		Local: data,
		Now:   func() time.Time { return base.Add(6 * time.Minute) },
	})
	require.NoError(t, err)

	paramFile := filepath.Join(dir, "param_cache.json")
	paramMgr := &cleanupParamManager{states: map[string][]factorwire.MemberParamState{}}
	params, err := newParamSelector(paramMgr, paramFile, time.Second)
	require.NoError(t, err)

	solo := cleanupTask("ftask_solo", "dataset_solo")
	sharedA := cleanupTask("ftask_shared_a", "dataset_shared")
	sharedB := cleanupTask("ftask_shared_b", "dataset_shared")
	allTasks := []domain.EngineTask{solo, sharedA, sharedB}

	// 初始化三个任务的水位、缺席记录、空洞账本、参数缓存、运行记录、已补周期及 localdata 窗口。
	axis, err := periodclock.AxisFor("crypto", "1m")
	require.NoError(t, err)
	runs := newRunTracker()
	filled := newFilledPeriods(func() time.Time { return base })
	for _, task := range allTasks {
		id := task.Set.TaskID
		require.NoError(t, wmStore.Put(watermark.State{
			TaskID: id, SpaceID: "crypto", Freq: "1m",
			CommittedThrough: base.Add(5 * time.Minute), Target: base.Add(5 * time.Minute),
		}))
		cont.NoteAbsent(id, base, base.Add(2*time.Minute), time.Minute, []string{"BTC"})
		require.NoError(t, ledger.RecordGaps(ctx, []pipeline.GapEntry{{
			TaskID: id, Subject: "BTC", Period: base.Add(5 * time.Minute),
			Member: "bias__var", DefinitionHash: "h1", TriggerEventID: "evt-1",
		}}))
		paramMgr.states[id] = []factorwire.MemberParamState{{
			MemberKey:           "bias__var",
			LastSubmittedPeriod: base.Add(4 * time.Minute).Unix(),
			Params:              domain.ParamSnapshot{"window": {Default: 3}},
		}}
		_, err := params.Select(ctx, task.Set, task.Members, base.Add(5*time.Minute))
		require.NoError(t, err)
		runs.record(id, axis, base.Add(5*time.Minute), "complete", pipeline.Outcome{}, base)
		filled.add(id, base, base.Add(5*time.Minute))
	}

	r := &Runtime{
		catalog:    catalogCache,
		locks:      setlock.New(),
		watermarks: wmStore,
		continuity: cont,
		gap:        ledger,
		params:     params,
		runs:       runs,
		filled:     filled,
		data:       data,
	}
	r.preload = newPreloader(catalogCache, storageStub, data, func() bool { return true }, nil)

	fakeClient := &cleanupCatalogFake{
		snapshot: CatalogSnapshot{
			Hash:               "hash-all-enabled",
			Sets:               allTasks,
			ExistingTaskIDs:    []string{"ftask_solo", "ftask_shared_a", "ftask_shared_b"},
			HasExistingTaskIDs: true,
		},
	}
	syncer := &catalogSyncer{client: fakeClient, cache: catalogCache, applied: r.onCatalogApplied}

	// 初始同步与预载：两个源数据集（dataset_solo、dataset_shared）下的三个任务都进入 localdata。
	require.NoError(t, syncer.SyncOnce(ctx))
	r.preload.once(ctx)
	require.Len(t, data.Sources(), 3)
	require.True(t, r.preload.Ready("ftask_solo"))
	require.True(t, r.preload.Ready("ftask_shared_a"))
	require.True(t, r.preload.Ready("ftask_shared_b"))

	// 步骤 1：仅停用 ftask_solo（仍在 existing_task_ids 中）。
	// 应释放其独占源数据集 dataset_solo 的窗口，但保留其 watermark、gapledger 与 param_cache。
	fakeClient.snapshot = CatalogSnapshot{
		Hash:               "hash-solo-disabled",
		Sets:               []domain.EngineTask{sharedA, sharedB},
		ExistingTaskIDs:    []string{"ftask_solo", "ftask_shared_a", "ftask_shared_b"},
		HasExistingTaskIDs: true,
	}
	require.NoError(t, syncer.SyncOnce(ctx))
	require.False(t, r.preload.Ready("ftask_solo"))
	sharedSrc := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_shared", Freq: "1m"}
	require.Equal(t, []localdata.SourceInfo{
		{Source: sharedSrc, TaskID: "ftask_shared_a"},
		{Source: sharedSrc, TaskID: "ftask_shared_b"},
	}, data.Sources())
	_, wmStillExists := wmStore.Get("ftask_solo")
	require.True(t, wmStillExists, "仅停用未删除的任务应保留 watermark")
	require.Equal(t, 3, ledger.PendingCount(), "仅停用未删除的任务不清理 gapledger")

	// 步骤 2：删除已停用的 ftask_solo（从 existing_task_ids 消失）。
	fakeClient.snapshot = CatalogSnapshot{
		Hash:               "hash-solo-deleted",
		Sets:               []domain.EngineTask{sharedA, sharedB},
		ExistingTaskIDs:    []string{"ftask_shared_a", "ftask_shared_b"},
		HasExistingTaskIDs: true,
	}
	require.NoError(t, syncer.SyncOnce(ctx))

	// 验证落盘的 watermarks.json 已移除 ftask_solo，保留其余两项。
	reopenedWM, err := watermark.Open(wmPath)
	require.NoError(t, err)
	_, okSolo := reopenedWM.Get("ftask_solo")
	_, okSharedA := reopenedWM.Get("ftask_shared_a")
	_, okSharedB := reopenedWM.Get("ftask_shared_b")
	require.False(t, okSolo, "已删任务的 watermark 必须从 watermarks.json 中移除")
	require.True(t, okSharedA)
	require.True(t, okSharedB)

	// 验证 gapledger 内存与重开回放都已取消 ftask_solo 的待恢复项，保留其余两项。
	require.Equal(t, 2, ledger.PendingCount())
	require.NoError(t, ledger.Close())
	ledger, _, err = gapledger.Open(gapDir, func() time.Time { return base }, 0)
	require.NoError(t, err)
	r.gap = ledger
	require.Equal(t, 2, ledger.PendingCount())
	for _, entry := range ledger.Pending() {
		require.NotEqual(t, "ftask_solo", entry.Set)
	}

	// 验证落盘的 param_cache.json、runTracker、filledPeriods 均已清理 ftask_solo。
	reopenedParams, err := newParamSelector(paramMgr, paramFile, time.Second)
	require.NoError(t, err)
	require.NotContains(t, reopenedParams.cache.InFlight, "ftask_solo")
	require.NotContains(t, reopenedParams.cache.Latest, "ftask_solo/bias__var")
	require.Contains(t, reopenedParams.cache.Latest, "ftask_shared_a/bias__var")
	require.False(t, filled.covers("ftask_solo", base.Add(time.Minute)))
	require.True(t, filled.covers("ftask_shared_a", base.Add(time.Minute)))
	for _, run := range runs.all(base) {
		require.NotEqual(t, "ftask_solo", run.TaskID)
	}

	// 步骤 3：删除与 ftask_shared_a 共享 dataset_shared 的 ftask_shared_b（如 DeleteBoundTask）。
	// 必须清理 ftask_shared_b 的本地状态，但绝对不能释放仍在被 ftask_shared_a 使用的 dataset_shared。
	fakeClient.snapshot = CatalogSnapshot{
		Hash:               "hash-shared-b-deleted",
		Sets:               []domain.EngineTask{sharedA},
		ExistingTaskIDs:    []string{"ftask_shared_a"},
		HasExistingTaskIDs: true,
	}
	require.NoError(t, syncer.SyncOnce(ctx))

	reopenedWM, err = watermark.Open(wmPath)
	require.NoError(t, err)
	_, okSharedA = reopenedWM.Get("ftask_shared_a")
	_, okSharedB = reopenedWM.Get("ftask_shared_b")
	require.True(t, okSharedA)
	require.False(t, okSharedB)
	require.Equal(t, 1, ledger.PendingCount())
	require.Equal(t, "ftask_shared_a", ledger.Pending()[0].Set)
	require.Equal(t, []localdata.SourceInfo{{Source: seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_shared", Freq: "1m"}, TaskID: "ftask_shared_a"}}, data.Sources(), "共享源数据集仍被活跃任务引用，不能被误删")

	// 步骤 4：删除最后一个任务 ftask_shared_a 后，dataset_shared 也被彻底释放，全部本地状态清空。
	fakeClient.snapshot = CatalogSnapshot{
		Hash:               "hash-all-deleted",
		Sets:               nil,
		ExistingTaskIDs:    []string{},
		HasExistingTaskIDs: true,
	}
	require.NoError(t, syncer.SyncOnce(ctx))
	require.Empty(t, data.Sources())
	require.Empty(t, wmStore.All())
	require.Zero(t, ledger.PendingCount())
	reopenedWM, err = watermark.Open(wmPath)
	require.NoError(t, err)
	require.Empty(t, reopenedWM.All())
}
