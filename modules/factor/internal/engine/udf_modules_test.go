package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/udfhost"
	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

func exampleWasm(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "factorudf", "testdata", name+".wasm"))
	require.NoError(t, err)
	return b
}

// fakeFetcher 模拟管理端的 GetUdfModule：按哈希返回字节，可以让它失败或返回错误的内容。
type fakeFetcher struct {
	files map[string][]byte
	calls atomic.Int32
	fail  atomic.Bool
	wrong []byte // 非空时无论要什么都返回它
}

func (f *fakeFetcher) fetch(_ context.Context, hash string) ([]byte, error) {
	f.calls.Add(1)
	if f.fail.Load() {
		return nil, errors.New("管理端不可达")
	}
	if f.wrong != nil {
		return f.wrong, nil
	}
	b, ok := f.files[hash]
	if !ok {
		return nil, errors.New("没有这个版本")
	}
	return b, nil
}

func newModules(t *testing.T, f *fakeFetcher) *udfModules {
	t.Helper()
	cfg := Default().UDF
	cfg.Dir = t.TempDir()
	m, err := newUDFModules(context.Background(), cfg, 2, nil, f.fetch)
	require.NoError(t, err)
	t.Cleanup(func() { _ = m.Close(context.Background()) })
	return m
}

// udfDef 构造一个引用 ts_mean_udf 的定义（绑定取自真实模块的元数据）。
func udfDef(t *testing.T, wasm []byte) (domain.FactorDef, string) {
	t.Helper()
	info, err := factorudf.Inspect(wasm, factorudf.Limits{})
	require.NoError(t, err)
	u, err := udfhost.UDFFromManifest(info.Manifest, info.Hash, udfhost.Describe{})
	require.NoError(t, err)
	def := domain.MustDefinition("fu", "fu", "output: close\n")
	def.Spec = "output: udf.ts_mean_udf(close, 3)\n"
	def.SpecHash = domain.SpecHash(def.Spec)
	def.UDFs = map[string]factorexpr.UDF{u.Name: u}
	def, err = domain.CompileDefinition(def)
	require.NoError(t, err)
	return def, info.Hash
}

func udfTask(t *testing.T, wasm []byte) (domain.EngineTask, string) {
	t.Helper()
	def, hash := udfDef(t, wasm)
	task := engineTask("ftask_u", "dataset_u", true)
	task.Members = domain.AsMembers(def)
	return task, hash
}

func TestModuleEnsureFetchesVerifiesAndCachesLocally(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	hash := factorudf.Hash(wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	m := newModules(t, f)
	ctx := context.Background()

	require.NoError(t, m.Ensure(ctx, hash, "ts_mean_udf"))
	require.True(t, m.Ready(hash))
	require.EqualValues(t, 1, f.calls.Load())
	require.Equal(t, []domain.UDFModuleStatus{{Hash: hash, Name: "ts_mean_udf", State: domain.UDFModuleReady}}, m.Status())
	saved, err := os.ReadFile(m.path(hash))
	require.NoError(t, err)
	require.Equal(t, wasm, saved, "按内容寻址存在本地")

	// 重启（新的存储对象、同一个目录）：本地命中，不再向管理端拉取。
	cfg := Default().UDF
	cfg.Dir = m.dir
	again, err := newUDFModules(ctx, cfg, 2, nil, f.fetch)
	require.NoError(t, err)
	defer again.Close(ctx)
	require.NoError(t, again.Ensure(ctx, hash, "ts_mean_udf"))
	require.EqualValues(t, 1, f.calls.Load(), "本地命中")

	// 本地文件被改坏：哈希对不上，重新拉取并覆盖。
	require.NoError(t, os.WriteFile(again.path(hash), []byte("corrupted"), 0o600))
	again.host.Unload(ctx, hash)
	require.NoError(t, again.Ensure(ctx, hash, "ts_mean_udf"))
	require.EqualValues(t, 2, f.calls.Load())
}

func TestModuleEnsureReportsFetchAndCompileFailures(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	hash := factorudf.Hash(wasm)
	ctx := context.Background()

	// 拉取失败。
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	f.fail.Store(true)
	m := newModules(t, f)
	require.Error(t, m.Ensure(ctx, hash, "ts_mean_udf"))
	require.False(t, m.Ready(hash))
	require.Equal(t, domain.UDFModuleFetchFailed, m.Status()[0].State)
	require.Contains(t, m.Status()[0].Error, "不可达")
	f.fail.Store(false)
	require.NoError(t, m.Ensure(ctx, hash, "ts_mean_udf"), "恢复后自动完成")
	require.Equal(t, domain.UDFModuleReady, m.Status()[0].State)

	// 拉到的字节哈希不符。
	f2 := &fakeFetcher{wrong: wasm}
	m2 := newModules(t, f2)
	err := m2.Ensure(ctx, "11"+hash[2:], "x")
	require.ErrorContains(t, err, "哈希")
	require.Equal(t, domain.UDFModuleFetchFailed, m2.Status()[0].State)

	// 哈希对得上但不是合格模块（编译失败）。
	junk := []byte("\x00asm\x01\x00\x00\x00junk")
	jh := factorudf.Hash(junk)
	f3 := &fakeFetcher{files: map[string][]byte{jh: junk}}
	m3 := newModules(t, f3)
	require.Error(t, m3.Ensure(ctx, jh, "junk"))
	require.Equal(t, domain.UDFModuleCompileFailed, m3.Status()[0].State)
}

// 目录同步：新快照引用的模块没有全部就绪就不应用、不记下快照哈希，下次同步重试；就绪后应用。
func TestSnapshotIsNotAppliedUntilModulesAreReady(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, hash := udfTask(t, wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	f.fail.Store(true)
	r := &Runtime{udf: newModules(t, f)}
	cache, _ := newTestCache(t)
	r.catalog = cache
	client := &catalogClientFake{snapshots: []CatalogSnapshot{{Hash: "h1", Sets: []domain.EngineTask{task}}, {Hash: "h1", Sets: []domain.EngineTask{task}}}}
	syncer := &catalogSyncer{client: client, cache: cache, prepare: r.prepareSnapshot, applied: r.retainModules}

	require.Error(t, syncer.SyncOnce(context.Background()))
	require.False(t, cache.Loaded(), "模块没有就绪：不应用快照")
	require.Empty(t, cache.Status().Hash, "不记下快照哈希：下次同步会重新取得")
	require.NotEmpty(t, cache.Status().SyncError)

	f.fail.Store(false)
	require.NoError(t, syncer.SyncOnce(context.Background()))
	require.True(t, cache.Loaded())
	require.Equal(t, "h1", cache.Status().Hash)
	require.True(t, r.udf.Ready(hash))
	require.Equal(t, []string{"", ""}, client.known, "两次都带着空哈希请求：失败那次没有记下哈希")
}

// 生命周期检查：引用的模块没有就绪，任务检查不过（不可执行）；就绪后通过。
func TestTaskCheckFailsUntilItsModulesAreReady(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, hash := udfTask(t, wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	f.fail.Store(true)
	r := &Runtime{udf: newModules(t, f)}

	check, ok := r.modulesCheck(task)
	require.True(t, ok)
	require.Equal(t, lifecycle.CheckFail, check.Status)
	require.Contains(t, check.Message, "没有就绪")

	require.Error(t, r.udf.Ensure(context.Background(), hash, "ts_mean_udf"))
	check, _ = r.modulesCheck(task)
	require.Contains(t, check.Message, "fetch_failed", "检查项带出失败原因")

	f.fail.Store(false)
	require.NoError(t, r.udf.Ensure(context.Background(), hash, "ts_mean_udf"))
	check, _ = r.modulesCheck(task)
	require.Equal(t, lifecycle.CheckPass, check.Status)

	// 不引用自定义算子的任务没有这一项。
	_, ok = r.modulesCheck(engineTask("ftask_a", "dataset_a", true, "bias"))
	require.False(t, ok)
}

// 恢复追赶的前置条件：模块没有全部就绪时追赶不写任何结果（不会拿旧版本、或没编译好的版本计算）。
func TestCatchupWaitsForModules(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, hash := udfTask(t, wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	r, _ := newLifecycleRuntime(t)
	startRunningModule(t, r)
	r.udf = newModules(t, f)
	_, err := r.catalog.Apply(CatalogSnapshot{Hash: "h-udf", Sets: []domain.EngineTask{task, engineTask("ftask_a", "dataset_a", true, "bias")}})
	require.NoError(t, err)
	ctx := context.Background()

	_, err = r.life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{})
	require.NoError(t, err)
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, Ref: lifecycle.Ref{}, ManualRelease: true})
	require.NoError(t, err)

	require.Contains(t, r.catchupPrecondition("ftask_u"), "自定义算子", "引用了没有就绪模块的任务不追赶")
	require.Empty(t, r.catchupPrecondition("ftask_a"), "不引用自定义算子的任务不受影响")
	require.False(t, r.catchupGate("ftask_u"))
	require.Contains(t, r.lifecycleStatus().CatchupBlocked, "自定义算子", "上报里带出阻塞原因")

	// 模块级的追赶钩子同样要满足前置条件：任务在重启后还没有预载（不在任务级追赶范围里）时，不能因此直接放行模块。
	moduleCatchup, err := r.catchupHook(ctx, lifecycle.Ref{}, time.Now())
	require.NoError(t, err)
	require.False(t, moduleCatchup.CaughtUp)
	require.Contains(t, moduleCatchup.Message, "自定义算子")

	require.NoError(t, r.udf.Ensure(ctx, hash, "ts_mean_udf"))
	require.Empty(t, r.catchupPrecondition("ftask_u"))
	require.Empty(t, r.lifecycleStatus().CatchupBlocked)
	moduleCatchup, err = r.catchupHook(ctx, lifecycle.Ref{}, time.Now())
	require.NoError(t, err)
	require.True(t, moduleCatchup.CaughtUp)
}

// 补算领取：作业绑定的模块准备失败，作业报告失败、没有写任何分块；准备好后可以重新发起。
func TestRecalcJobFailsWithoutWritingWhenModulesCannotBePrepared(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	hash := factorudf.Hash(wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	f.fail.Store(true)
	r := &Runtime{udf: newModules(t, f)}

	job := testJob("BTC")
	task, _ := udfTask(t, wasm)
	job.Set = task
	client := &jobClientFake{job: job, found: true}
	runner := &planRecorder{out: pipeline.Outcome{Status: "complete"}}
	loop := &recalcLoop{client: client, executor: recalcexec.NewExecutor(runner), source: &sourceFake{}, poll: time.Millisecond, prepare: r.prepareJobModules}

	found, err := loop.RunOnce(context.Background())
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, runner.plans, "没有写任何分块")
	require.Len(t, client.reports, 1)
	require.Equal(t, jobStatusFailed, client.reports[0].status)
	require.Contains(t, client.reports[0].errText, "自定义算子")
}

// 目录快照在重启后（JSON 恢复）仍带着绑定，定义能重新编译。
func TestCatalogRestoresUdfBindingsAfterRestart(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, _ := udfTask(t, wasm)
	cache, stateFile := newTestCache(t)
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{task}})
	require.NoError(t, err)

	reopened, err := NewCatalogCache(stateFile)
	require.NoError(t, err)
	loaded, err := reopened.Load()
	require.NoError(t, err)
	require.True(t, loaded)
	set, lookup := reopened.SetByID("ftask_u")
	require.Equal(t, SetFound, lookup)
	def := set.Members[0].Factor
	require.NotEmpty(t, def.UDFs)
	require.NotNil(t, def.Compiled(), "重启后定义带着绑定重新编译成功")
	require.Contains(t, def.Compiled().CanonicalText(), "udf.ts_mean_udf@")
}

// 引擎用同一套宿主执行：模块就绪后，实时计算的节点能算出结果。
func TestEngineHostExecutesLoadedModule(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	hash := factorudf.Hash(wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	m := newModules(t, f)
	require.NoError(t, m.Ensure(context.Background(), hash, "ts_mean_udf"))
	var calls atomic.Int32
	m.Host().SetObserver(func(udf string, _ time.Duration, copied int64, reason string) {
		require.Equal(t, "ts_mean_udf", udf)
		require.Positive(t, copied)
		require.Empty(t, reason)
		calls.Add(1)
	})
	module, ok := m.Host().Module(hash)
	require.True(t, ok)
	b := &factorudf.Batch{NSeries: 1, InLen: 4, OutLen: 2, Inputs: [][]float64{{1, 2, 3, 4}}, Params: []float64{3}, Out: make([]float64, 2)}
	require.NoError(t, module.Eval(context.Background(), b))
	require.Equal(t, []float64{2, 3}, b.Out)
}

// 目录同步正在准备的新版本，不能被其他入口（补算作业结束的回收）卸载；新目录应用之后保留集合以它为准。
func TestSyncPreparedModulesSurviveAReclaimBeforeTheCatalogIsApplied(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, hash := udfTask(t, wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	r, _ := newLifecycleRuntime(t)
	r.udf = newModules(t, f)
	r.udf.reclaim = r.reclaimModules
	ctx := context.Background()

	snapshot := CatalogSnapshot{Hash: "h-udf", Sets: []domain.EngineTask{task}}
	require.NoError(t, r.prepareSnapshot(ctx, snapshot))
	require.True(t, r.udf.host.Has(hash))

	// 此时有一个补算作业结束，触发回收：目录还没应用，保留集合里没有新版本。
	release := r.udf.Pin([]udfRef{{name: "other", hash: "other-hash"}})
	release()
	require.True(t, r.udf.host.Has(hash), "同步准备中的版本不能被回收卸载")

	_, err := r.catalog.Apply(snapshot)
	require.NoError(t, err)
	r.retainModules()
	require.True(t, r.udf.host.Has(hash), "新目录引用它，应用之后仍保留")
	require.Nil(t, r.udf.syncPin, "应用之后同步登记已释放")

	// 目录不再引用：下一次应用之后卸载。
	_, err = r.catalog.Apply(CatalogSnapshot{Hash: "h-empty", Sets: nil})
	require.NoError(t, err)
	r.retainModules()
	require.False(t, r.udf.host.Has(hash))
}

// 准备失败遗留的同步登记，在下一次同步成功（哪怕目录没有变化）时释放，之后回收照常进行。
func TestStaleSyncPinIsReleasedWhenTheNextSyncIsNotModified(t *testing.T) {
	wasm := exampleWasm(t, "ts_mean")
	task, hash := udfTask(t, wasm)
	f := &fakeFetcher{files: map[string][]byte{hash: wasm}}
	r, _ := newLifecycleRuntime(t)
	r.udf = newModules(t, f)
	r.udf.reclaim = r.reclaimModules
	ctx := context.Background()

	// 准备目录 B 之后没有应用（应用失败或管理端撤销了变更）：登记遗留。
	require.NoError(t, r.prepareSnapshot(ctx, CatalogSnapshot{Hash: "h-b", Sets: []domain.EngineTask{task}}))
	require.True(t, r.udf.host.Has(hash))
	require.NotNil(t, r.udf.syncPin)

	// 下一次同步目录没变：应用回调仍然被调用，登记释放、不再被目录引用的模块被回收。
	client := &catalogClientFake{snapshots: []CatalogSnapshot{{NotModified: true, Hash: r.catalog.Status().Hash}}}
	syncer := &catalogSyncer{client: client, cache: r.catalog, prepare: r.prepareSnapshot, applied: r.retainModules}
	require.NoError(t, syncer.SyncOnce(ctx))
	require.Nil(t, r.udf.syncPin)
	require.False(t, r.udf.host.Has(hash))
}
