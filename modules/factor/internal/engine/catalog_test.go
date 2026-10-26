package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/stretchr/testify/require"
)

func engineTask(taskID, datasetID string, ready bool, factorIDs ...string) domain.EngineTask {
	factors := make([]domain.FactorDef, 0, len(factorIDs))
	for _, id := range factorIDs {
		factors = append(factors, domain.MustDefinition(id, id, "output: ts_mean(close, 2)\n"))
	}
	return domain.EngineTask{
		Set: domain.FactorTask{
			TaskID: taskID, SpaceID: "crypto", SourceDatasetID: datasetID, Freq: "1m",
			ResultDatasetID: "result_" + datasetID, Status: domain.TaskStatusEnabled,
		},
		Members: domain.AsMembers(factors...), ResultReady: ready,
	}
}

func newTestCache(t *testing.T) (*CatalogCache, string) {
	t.Helper()
	stateFile := filepath.Join(t.TempDir(), "data", "catalog.json")
	cache, err := NewCatalogCache(stateFile)
	require.NoError(t, err)
	return cache, stateFile
}

// 快照里的定义来自 JSON，没有编译结果；目录安装快照时编译每个定义（设计 8）。
func uncompiled(set domain.EngineTask) domain.EngineTask {
	raw, err := json.Marshal(set)
	if err != nil {
		panic(err)
	}
	var decoded domain.EngineTask
	if err := json.Unmarshal(raw, &decoded); err != nil {
		panic(err)
	}
	return decoded
}

func TestCatalogApplyCompilesAndPersists(t *testing.T) {
	cache, stateFile := newTestCache(t)
	set := uncompiled(engineTask("ftask_a", "dataset_a", true, "bias"))
	require.Nil(t, set.Members[0].Factor.Compiled())

	changed, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{set}})

	require.NoError(t, err)
	require.True(t, changed)
	got, lookup := cache.SetByID("ftask_a")
	require.Equal(t, SetFound, lookup)
	require.NotNil(t, got.Members[0].Factor.Compiled())
	require.Equal(t, 2, got.Members[0].Factor.LookbackPeriods)
	info, err := os.Stat(stateFile)
	require.NoError(t, err)
	require.False(t, info.IsDir())
	status := cache.Status()
	require.True(t, status.Loaded)
	require.Equal(t, "hash-1", status.Hash)
	require.Equal(t, 1, status.Sets)
}

func TestCatalogLoadFromStateFile(t *testing.T) {
	cache, stateFile := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	restarted, err := NewCatalogCache(stateFile)
	require.NoError(t, err)
	loaded, err := restarted.Load()

	require.NoError(t, err)
	require.True(t, loaded)
	require.Equal(t, "hash-1", restarted.Status().Hash)
	sets, err := restarted.EnabledTasksByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.Len(t, sets, 1)
	require.Equal(t, "ftask_a", sets[0].Set.TaskID)
	require.Len(t, sets[0].Members, 1)
	require.NotNil(t, sets[0].Members[0].Factor.Compiled(), "加载快照时重新编译")
	require.Equal(t, []string{"bias"}, sets[0].Members[0].Factor.Outputs)
}

// 派生字段以编译结果为准：快照里的派生字段即使被改动，也按 spec 重新推导。
// 编译失败的定义原样保留，由流水线按该因子失败处理，不影响同任务的其他因子。
func TestCatalogRederivesFieldsAndKeepsUncompilableDefinitions(t *testing.T) {
	cache, _ := newTestCache(t)
	set := uncompiled(engineTask("ftask_a", "dataset_a", true, "bias", "cci"))
	set.Members[0].Factor.LookbackPeriods, set.Members[0].Factor.Outputs = 999, []string{"forged"}
	set.Members[1].Factor.Spec = "output: future_function(close)\n"
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{set}})
	require.NoError(t, err)
	got, _ := cache.SetByID("ftask_a")
	require.Equal(t, 2, got.Members[0].Factor.LookbackPeriods)
	require.Equal(t, []string{"bias"}, got.Members[0].Factor.Outputs)
	require.Nil(t, got.Members[1].Factor.Compiled())
}

func TestCatalogLoadWithoutStateFile(t *testing.T) {
	cache, _ := newTestCache(t)

	loaded, err := cache.Load()

	require.NoError(t, err)
	require.False(t, loaded)
	_, err = cache.EnabledTasksByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.ErrorIs(t, err, storageio.ErrInfra, "events wait until a catalog exists")
}

func TestCatalogApplyFailureKeepsPreviousSnapshot(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	// 快照写盘失败：新快照不生效。
	blocker := filepath.Join(t.TempDir(), "file")
	require.NoError(t, os.WriteFile(blocker, nil, 0o600))
	cache.stateFile = filepath.Join(blocker, "catalog.json")

	_, err = cache.Apply(CatalogSnapshot{Hash: "hash-2", Sets: []domain.EngineTask{engineTask("ftask_b", "dataset_b", true, "cci")}})

	require.Error(t, err)
	require.Equal(t, "hash-1", cache.Status().Hash)
	sets, err := cache.EnabledTasksByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.Len(t, sets, 1)
}

func TestCatalogNotModifiedKeepsTasks(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	changed, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", NotModified: true})

	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, 1, cache.Status().Sets)
}

func TestCatalogSyncFailureKeepsServing(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	cache.MarkSyncFailed(errors.New("manager unreachable"))

	sets, err := cache.EnabledTasksByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.Len(t, sets, 1)
	status := cache.Status()
	require.False(t, status.SyncErrorSince.IsZero())
	require.Equal(t, "manager unreachable", status.SyncError)

	_, err = cache.Apply(CatalogSnapshot{Hash: "hash-1", NotModified: true})
	require.NoError(t, err)
	require.True(t, cache.Status().SyncErrorSince.IsZero(), "a successful sync clears the failure")
}

func TestCatalogNotReadyTaskReturnsInfraError(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", false, "bias")}})
	require.NoError(t, err)

	sets, err := cache.EnabledTasksByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	require.Len(t, sets, 1)
	require.False(t, sets[0].ResultReady, "结果数据集没就绪的任务交给调用方重投")
	_, found, err := cache.EnabledTask(context.Background(), "ftask_a")
	require.ErrorIs(t, err, storageio.ErrInfra)
	require.False(t, found)
}

// 同一源数据集上的多个任务（各策略实例的私有任务）都返回。
func TestCatalogReturnsEverySetOverDataset(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{
		engineTask("ftask_a", "dataset_a", true, "bias"), engineTask("ftask_i_x", "dataset_a", true, "cci"), engineTask("ftask_b", "dataset_b", true, "bias"),
	}})
	require.NoError(t, err)
	sets, err := cache.EnabledTasksByDataset(context.Background(), "crypto", "dataset_a", "1m")
	require.NoError(t, err)
	var ids []string
	for _, set := range sets {
		ids = append(ids, set.Set.TaskID)
	}
	require.ElementsMatch(t, []string{"ftask_a", "ftask_i_x"}, ids)
}

func TestCatalogUnknownDatasetNotFound(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	sets, err := cache.EnabledTasksByDataset(context.Background(), "crypto", "dataset_other", "1m")

	require.NoError(t, err)
	require.Empty(t, sets)
}

func TestCatalogFilterSubjects(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{
		engineTask("ftask_b", "dataset_b", true, "bias"), engineTask("ftask_a", "dataset_a", true, "bias"),
	}})
	require.NoError(t, err)

	filters, err := cache.FilterSubjects(context.Background())

	require.NoError(t, err)
	registry, err := events.DefaultRegistry()
	require.NoError(t, err)
	var want []string
	for _, dataset := range []string{"dataset_a", "dataset_b"} {
		subject, renderErr := registry.RenderSubject(events.CollectorPeriodCompleted, "crypto", dataset)
		require.NoError(t, renderErr)
		want = append(want, subject)
	}
	require.ElementsMatch(t, want, filters)
}

func TestCatalogApplyReportsTaskChanges(t *testing.T) {
	cache, _ := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)

	changed, err := cache.Apply(CatalogSnapshot{Hash: "hash-2", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias", "cci")}})
	require.NoError(t, err)
	require.False(t, changed, "a member change keeps the same subscriptions")

	changed, err = cache.Apply(CatalogSnapshot{Hash: "hash-3", Sets: []domain.EngineTask{
		engineTask("ftask_a", "dataset_a", true, "bias"), engineTask("ftask_b", "dataset_b", true, "bias"),
	}})
	require.NoError(t, err)
	require.True(t, changed)
}

func TestNextSyncAtAlignsToIntervalPlusOffset(t *testing.T) {
	at := func(hms string) time.Time {
		parsed, err := time.Parse("15:04:05", hms)
		require.NoError(t, err)
		return time.Date(2026, 10, 5, parsed.Hour(), parsed.Minute(), parsed.Second(), 0, time.UTC)
	}
	require.Equal(t, at("12:00:45"), NextSyncAt(at("12:00:10"), time.Minute, 45*time.Second))
	require.Equal(t, at("12:01:45"), NextSyncAt(at("12:00:45"), time.Minute, 45*time.Second))
	require.Equal(t, at("12:01:45"), NextSyncAt(at("12:00:50"), time.Minute, 45*time.Second))
	require.Equal(t, at("12:01:00"), NextSyncAt(at("12:00:00"), time.Minute, 0))
	require.Equal(t, at("12:05:30"), NextSyncAt(at("12:01:00"), 5*time.Minute, 30*time.Second))
}

type catalogClientFake struct {
	snapshots []CatalogSnapshot
	err       error
	known     []string
}

func (f *catalogClientFake) SyncCatalog(_ context.Context, knownHash string) (CatalogSnapshot, error) {
	f.known = append(f.known, knownHash)
	if f.err != nil {
		return CatalogSnapshot{}, f.err
	}
	next := f.snapshots[0]
	f.snapshots = f.snapshots[1:]
	return next, nil
}

func TestSyncerSendsKnownHashAndNotifiesChanges(t *testing.T) {
	cache, _ := newTestCache(t)
	client := &catalogClientFake{snapshots: []CatalogSnapshot{
		{Hash: "hash-1", Sets: []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}},
		{Hash: "hash-1", NotModified: true},
	}}
	changes := 0
	syncer := &catalogSyncer{client: client, cache: cache, onChange: func() { changes++ }}

	require.NoError(t, syncer.SyncOnce(context.Background()))
	require.NoError(t, syncer.SyncOnce(context.Background()))

	require.Equal(t, []string{"", "hash-1"}, client.known)
	require.Equal(t, 1, changes)
}

func TestSyncerFailureMarksCache(t *testing.T) {
	cache, _ := newTestCache(t)
	syncer := &catalogSyncer{client: &catalogClientFake{err: errors.New("dial tcp: timeout")}, cache: cache}

	require.Error(t, syncer.SyncOnce(context.Background()))

	require.Equal(t, "dial tcp: timeout", cache.Status().SyncError)
}

// Clear 清空目录缓存并写盘：重启后读到的也是空目录（loaded=false，等下一次同步）。
func TestCatalogCacheClearEmptiesMemoryAndDisk(t *testing.T) {
	cache, stateFile := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{uncompiled(engineTask("ftask_a", "dataset_a", true, "bias"))}})
	require.NoError(t, err)
	require.Equal(t, []string{"ftask_a"}, cache.TaskIDs())

	require.NoError(t, cache.Clear())
	require.Empty(t, cache.TaskIDs())
	require.False(t, cache.Loaded())

	again, err := NewCatalogCache(stateFile)
	require.NoError(t, err)
	loaded, err := again.Load()
	require.NoError(t, err)
	if loaded {
		require.Empty(t, again.TaskIDs(), "磁盘上的快照也是空的")
	}
}
