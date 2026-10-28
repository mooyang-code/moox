package catalog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	"github.com/mooyang-code/moox/packages/factorudf"
	"github.com/stretchr/testify/require"
)

func udfWasm(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "packages", "factorudf", "testdata", name+".wasm"))
	require.NoError(t, err)
	return b
}

func udfFixtures(t *testing.T, name string) factorudf.FixtureSet {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "..", "sdk", "rust", "examples", name, "tests", "fixtures")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	set := factorudf.FixtureSet{}
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		norm, err := factorudf.NormalizeFixtures(raw)
		require.NoError(t, err)
		set[e.Name()] = norm
	}
	return set
}

// udfPackage 用本地门禁同样的流程（加载、检查、一致性测试）打出一个 .udfpkg。
func udfPackage(t *testing.T, name string) []byte {
	t.Helper()
	rt, err := factorudf.NewRuntime(context.Background(), factorudf.Options{})
	require.NoError(t, err)
	defer rt.Close(context.Background())
	wasm := udfWasm(t, name)
	m, err := rt.Load(context.Background(), wasm)
	require.NoError(t, err)
	fx := udfFixtures(t, name)
	rep, err := factorudf.RunConformance(context.Background(), rt, m, fx, factorudf.ConformanceOptions{})
	require.NoError(t, err)
	raw, err := factorudf.WritePackage(&factorudf.Package{
		Wasm: wasm, Manifest: factorudf.PackageManifest{Manifest: *m.Info.Manifest, Doc: "示例", Source: factorudf.SourceRef{Repo: "repo", Commit: "abc"}},
		Fixtures: fx, Report: rep,
	})
	require.NoError(t, err)
	return raw
}

func newUdfService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	db := openCatalogStore(t)
	rt, err := factorudf.NewRuntime(context.Background(), factorudf.Options{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	svc := newReadyService(t, db, newMetadataFake())
	WithUDFVerifier(UDFVerifier{Runtime: rt})(svc)
	return svc, db
}

func TestPublishStoresAnImmutableInactiveVersion(t *testing.T) {
	svc, db := newUdfService(t)
	ctx := context.Background()
	raw := udfPackage(t, "ts_mean")

	res, err := svc.PublishUdf(ctx, raw)
	require.NoError(t, err)
	require.True(t, res.Created)
	require.Equal(t, "ts_mean_udf", res.Name)
	require.True(t, res.Report.Passed)
	name, versions, err := db.GetUdf(ctx, "ts_mean_udf")
	require.NoError(t, err)
	require.Empty(t, name.ActiveHash, "发布后未启用")
	require.Len(t, versions, 1)

	// 重复上传：只更新说明，不新增版本。
	again, err := svc.PublishUdf(ctx, raw)
	require.NoError(t, err)
	require.False(t, again.Created)
	require.Equal(t, res.Hash, again.Hash)
}

func TestPublishRejectsUnqualifiedPackages(t *testing.T) {
	svc, _ := newUdfService(t)
	ctx := context.Background()

	_, err := svc.PublishUdf(ctx, []byte("not a package"))
	require.ErrorIs(t, err, ErrInvalidUdf)

	// 服务端一致性测试没过：带全局状态的模块能通过静态检查，要靠测试拦住。
	rt, err := factorudf.NewRuntime(ctx, factorudf.Options{})
	require.NoError(t, err)
	defer rt.Close(ctx)
	wasm := udfWasm(t, "bad_state")
	m, err := rt.Load(ctx, wasm)
	require.NoError(t, err)
	rep, err := factorudf.RunConformance(ctx, rt, m, nil, factorudf.ConformanceOptions{})
	require.NoError(t, err)
	require.False(t, rep.Passed)
	rep.Passed = true // 伪造本地报告
	raw, err := factorudf.WritePackage(&factorudf.Package{Wasm: wasm, Manifest: factorudf.PackageManifest{Manifest: *m.Info.Manifest}, Report: rep})
	require.NoError(t, err)
	_, err = svc.PublishUdf(ctx, raw)
	require.ErrorIs(t, err, ErrInvalidUdf)
	require.ErrorContains(t, err, "一致性测试未通过")

	// 摘要不一致：本地报告的摘要被篡改。
	good := udfPackage(t, "ts_mean")
	pkg, err := factorudf.ReadPackage(good, factorudf.Limits{})
	require.NoError(t, err)
	pkg.Report.Digest = "00" + pkg.Report.Digest[2:]
	pkg.ReportRaw = nil
	forged, err := factorudf.WritePackage(pkg)
	require.NoError(t, err)
	_, err = svc.PublishUdf(ctx, forged)
	require.ErrorContains(t, err, "摘要不一致")

	// 脏工作区构建的包：服务端也拒绝，不能只靠命令行把关。
	dirty, err := factorudf.ReadPackage(good, factorudf.Limits{})
	require.NoError(t, err)
	dirty.Manifest.Source.Dirty = true
	dirtyRaw, err := factorudf.WritePackage(dirty)
	require.NoError(t, err)
	_, err = svc.PublishUdf(ctx, dirtyRaw)
	require.ErrorIs(t, err, ErrInvalidUdf)
	require.ErrorContains(t, err, "未提交改动")
}

// 首次启用没有引用方，不要求暂停引擎；之后因子定义才能引用它。
func TestFirstActivationNeedsNoPause(t *testing.T) {
	svc, db := newUdfService(t)
	ctx := context.Background()
	res, err := svc.PublishUdf(ctx, udfPackage(t, "ts_mean"))
	require.NoError(t, err)

	// 启用之前，定义不能引用它。
	_, err = svc.CreateFactor(ctx, factorWithSpec("f_mean", "output: udf.ts_mean_udf(close, 3)\n"))
	require.ErrorContains(t, err, "没有启用的版本")

	dry, err := svc.ActivateUdf(ctx, "ts_mean_udf", res.Hash, true)
	require.NoError(t, err)
	require.True(t, dry.DryRun)
	require.False(t, dry.Applied)
	for _, p := range dry.Prerequisites {
		require.True(t, p.OK, p.Name)
	}
	name, _, _ := db.GetUdf(ctx, "ts_mean_udf")
	require.Empty(t, name.ActiveHash, "dry_run 不改数据")

	applied, err := svc.ActivateUdf(ctx, "ts_mean_udf", res.Hash, false)
	require.NoError(t, err)
	require.True(t, applied.Applied)

	created, err := svc.CreateFactor(ctx, factorWithSpec("f_mean", "output: udf.ts_mean_udf(close, 3)\n"))
	require.NoError(t, err)
	require.Equal(t, 3, created.LookbackPeriods)
	require.Equal(t, res.Hash, created.UDFs["ts_mean_udf"].Hash)
}

type fakeEngine struct {
	mu     sync.Mutex
	info   domain.EngineInfo
	status domain.EngineStatus
	seen   bool
	beat   bool // 每次读取都推进心跳时间（模拟引擎持续上报）
}

func (f *fakeEngine) view(context.Context) (domain.EngineInfo, domain.EngineStatus, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beat {
		f.info.LastHeartbeatAt = f.info.LastHeartbeatAt.Add(time.Second)
	}
	return f.info, f.status, f.seen, nil
}

func (f *fakeEngine) set(stage string, quiesced bool, at time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = true
	f.info = domain.EngineInfo{Online: true, LastHeartbeatAt: time.Now()}
	f.status.Lifecycle = domain.EngineLifecycle{Stage: stage, Quiesced: quiesced, QuiescedAt: at}
}

// 切换在引擎暂停期间进行：未暂停、未收敛、离线时拒绝；有已领取的补算作业时拒绝；dry_run 不改数据。
func TestActivationPrerequisites(t *testing.T) {
	svc, db := newUdfService(t)
	ctx := context.Background()
	engine := &fakeEngine{beat: true}
	WithEngineView(engine.view)(svc)

	v1, err := svc.PublishUdf(ctx, udfPackage(t, "ts_mean"))
	require.NoError(t, err)
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v1.Hash, false)
	require.NoError(t, err)
	// 引用它的因子加入任务并启用。
	_, err = svc.CreateFactor(ctx, factorWithSpec("f_mean", "output: udf.ts_mean_udf(close, 3)\n"))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "f_mean", "", nil)
	require.NoError(t, err)
	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "f_mean", domain.MemberStatusEnabled)
	require.NoError(t, err)

	// 第二个版本：同一份 wasm 改写哈希来模拟（元数据完全相同）。
	old, err := db.GetUdfVersion(ctx, v1.Hash, true)
	require.NoError(t, err)
	v2 := old
	v2.Hash = "00" + old.Hash[2:]
	_, err = db.PutUdfVersion(ctx, v2)
	require.NoError(t, err)

	checks := func(res *ActivateResult) map[string]Prerequisite {
		m := map[string]Prerequisite{}
		for _, p := range res.Prerequisites {
			m[p.Name] = p
		}
		return m
	}
	const pausedName = "因子引擎模块已暂停且本次暂停已收敛"

	// 没有收到过心跳。
	dry, err := svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, true)
	require.NoError(t, err)
	require.False(t, checks(dry)[pausedName].OK)
	require.Len(t, dry.Impacts, 1)
	require.Equal(t, "f_mean", dry.Impacts[0].FactorID)
	require.NotEqual(t, dry.Impacts[0].OldDefinitionHash, dry.Impacts[0].NewDefinitionHash, "切换版本 definition_hash 改变")
	require.Equal(t, []string{testTaskID()}, dry.Tasks)

	// 引擎在运行。
	engine.set("running", false, time.Time{})
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.ErrorIs(t, err, store.ErrConflict)
	require.ErrorContains(t, err, "不是暂停")

	// 已暂停但没有收敛。
	engine.set("paused", false, time.Time{})
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.ErrorContains(t, err, "还没有收敛")

	// 引擎离线。
	engine.set("paused", true, time.Now())
	engine.info.Online = false
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.ErrorContains(t, err, "离线")

	// 已暂停且已收敛，但有已领取的补算作业。
	engine.set("paused", true, time.Now())
	job, err := db.CreateRecalcJob(ctx, store.RecalcJob{RequestID: "req-1", TaskID: testTaskID(), StartTime: 1, EndTime: 100, Status: store.RecalcStatusAccepted})
	require.NoError(t, err)
	_, err = db.UpdateRecalcJob(ctx, job.JobID, func(j *store.RecalcJob) error { j.Status = store.RecalcStatusRunning; return nil })
	require.NoError(t, err)
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.ErrorContains(t, err, "补算作业")
	name, _, _ := db.GetUdf(ctx, "ts_mean_udf")
	require.Equal(t, v1.Hash, name.ActiveHash, "前提不满足时不改数据")

	// 取消作业后切换成功；之后读取这些定义得到新的绑定。
	_, err = db.UpdateRecalcJob(ctx, job.JobID, func(j *store.RecalcJob) error { j.Status = store.RecalcStatusCancelled; return nil })
	require.NoError(t, err)
	res, err := svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.NoError(t, err)
	require.True(t, res.Applied)
	def, err := db.GetFactor(ctx, "f_mean")
	require.NoError(t, err)
	require.Equal(t, v2.Hash, def.UDFs["ts_mean_udf"].Hash)

	// 回滚：指回旧版本（同样的步骤）。
	engine.set("paused", true, time.Now())
	back, err := svc.ActivateUdf(ctx, "ts_mean_udf", v1.Hash, false)
	require.NoError(t, err)
	require.True(t, back.Applied)

	// 已经是启用版本：没有变化。
	same, err := svc.ActivateUdf(ctx, "ts_mean_udf", v1.Hash, false)
	require.NoError(t, err)
	require.False(t, same.Applied)
}

// 提交前复核：引擎在复核期间离开暂停，切换被拒绝。
func TestActivationRecheckDetectsAResume(t *testing.T) {
	svc, db := newUdfService(t)
	ctx := context.Background()
	engine := &fakeEngine{}
	WithEngineView(engine.view)(svc)
	v1, err := svc.PublishUdf(ctx, udfPackage(t, "ts_mean"))
	require.NoError(t, err)
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v1.Hash, false)
	require.NoError(t, err)
	_, err = svc.CreateFactor(ctx, factorWithSpec("f_mean", "output: udf.ts_mean_udf(close, 3)\n"))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "f_mean", "", nil)
	require.NoError(t, err)
	_, err = svc.SetTaskMemberStatus(ctx, testTaskID(), "f_mean", domain.MemberStatusEnabled)
	require.NoError(t, err)
	old, _ := db.GetUdfVersion(ctx, v1.Hash, true)
	v2 := old
	v2.Hash = "00" + old.Hash[2:]
	_, err = db.PutUdfVersion(ctx, v2)
	require.NoError(t, err)

	at := time.Now()
	engine.set("paused", true, at)
	// 第二次心跳（复核用）显示引擎已经恢复。
	go func() {
		time.Sleep(300 * time.Millisecond)
		engine.set("running", false, time.Time{})
		engine.mu.Lock()
		engine.info.LastHeartbeatAt = engine.info.LastHeartbeatAt.Add(time.Minute)
		engine.mu.Unlock()
	}()
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.ErrorIs(t, err, store.ErrConflict)
	name, _, _ := db.GetUdf(ctx, "ts_mean_udf")
	require.Equal(t, v1.Hash, name.ActiveHash)
}

// 新版本编译失败或成员参数越界时拒绝切换。
func TestActivationRejectsIncompatibleVersion(t *testing.T) {
	svc, db := newUdfService(t)
	ctx := context.Background()
	engine := &fakeEngine{beat: true}
	WithEngineView(engine.view)(svc)
	v1, err := svc.PublishUdf(ctx, udfPackage(t, "ts_mean"))
	require.NoError(t, err)
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v1.Hash, false)
	require.NoError(t, err)
	_, err = svc.CreateFactor(ctx, factorWithSpec("f_mean", "output: udf.ts_mean_udf(close, 3000)\n"))
	require.NoError(t, err)
	_, err = svc.AddTaskMember(ctx, testTaskID(), "f_mean", "", nil)
	require.NoError(t, err)

	// 第二个版本把参数范围收窄到 1..10：定义里写死的 3000 在新版本下编译失败。
	old, _ := db.GetUdfVersion(ctx, v1.Hash, true)
	var manifest factorudf.PackageManifest
	require.NoError(t, json.Unmarshal([]byte(old.Manifest), &manifest))
	manifest.Params[0].Max = 10
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	v2 := old
	v2.Hash, v2.Manifest = "11"+old.Hash[2:], string(raw)
	_, err = db.PutUdfVersion(ctx, v2)
	require.NoError(t, err)

	dry, err := svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, true)
	require.NoError(t, err)
	require.Contains(t, dry.Impacts[0].Error, "重新编译失败")
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v2.Hash, false)
	require.ErrorIs(t, err, store.ErrConflict)
	name, _, _ := db.GetUdf(ctx, "ts_mean_udf")
	require.Equal(t, v1.Hash, name.ActiveHash)
}

// 锁协议：引用 UDF 的创建、修改定义与切换串行化（都先取全局 UDF 锁）。
func TestUdfLockSerializesDefinitionChangesWithActivation(t *testing.T) {
	svc, _ := newUdfService(t)
	ctx := context.Background()
	v1, err := svc.PublishUdf(ctx, udfPackage(t, "ts_mean"))
	require.NoError(t, err)
	_, err = svc.ActivateUdf(ctx, "ts_mean_udf", v1.Hash, false)
	require.NoError(t, err)

	unlock, err := svc.locks.LockUdfContext(ctx)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		_, err := svc.CreateFactor(ctx, factorWithSpec("f_mean", "output: udf.ts_mean_udf(close, 3)\n"))
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("引用自定义算子的创建要等全局 UDF 锁")
	case <-time.After(150 * time.Millisecond):
	}
	// 不引用自定义算子的定义不受这把锁影响。
	_, err = svc.CreateFactor(ctx, factorWithSpec("plain", "output: ts_mean(close, 3)\n"))
	require.NoError(t, err)
	unlock()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("释放锁后应继续")
	}
	// 修改：旧文本引用了 UDF、新文本不再引用，也先取锁。
	unlock, err = svc.locks.LockUdfContext(ctx)
	require.NoError(t, err)
	go func() {
		_, err := svc.UpdateFactor(ctx, factorWithSpec("f_mean", "output: ts_mean(close, 3)\n"))
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("旧文本引用自定义算子的修改要等全局 UDF 锁")
	case <-time.After(150 * time.Millisecond):
	}
	unlock()
	require.NoError(t, <-done)
}
