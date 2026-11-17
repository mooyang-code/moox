package engine

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/localdata"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"github.com/stretchr/testify/require"
)

type preloadDataFake struct {
	// mu 保护两个记录：恢复追赶时各任务的本地同步在各自的协程里并发进行。
	mu       sync.Mutex
	prepared []string
	released []domain.FactorTask
	// hold 不为空时 Prepare 等它关闭才返回，用来让恢复追赶的本地同步停在“同步中”。
	hold chan struct{}
}

func (f *preloadDataFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.prepared)
}

func (f *preloadDataFake) ReleaseTask(set domain.FactorTask) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.released = append(f.released, set)
}

func (f *preloadDataFake) Prepare(ctx context.Context, set domain.EngineTask, _ []string) error {
	if f.hold != nil {
		select {
		case <-f.hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, set.Set.TaskID)
	return nil
}

type lifecycleSourceFake struct{}

func (lifecycleSourceFake) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close"}, nil
}

func (lifecycleSourceFake) ListDatasetSubjects(context.Context, string, string) ([]string, error) {
	return []string{"BTCUSDT"}, nil
}

// newLifecycleRuntime 建一个只有生命周期、任务检查与预载的运行时，目录里是一个有成员的任务 ftask_a。
// 预载器用 newGatedPreloader 建：Initialize 用的是同一个构造函数（门禁与“载入后立即重查”都接在里面）；
// Initialize 本身要 tRPC 服务、网关与 EventBus，这里不走它。
func newLifecycleRuntime(t *testing.T, tasks ...domain.EngineTask) (*Runtime, *preloadDataFake) {
	t.Helper()
	return newLifecycleRuntimeAt(t, filepath.Join(t.TempDir(), "lifecycle.json"), tasks...)
}

// newLifecycleRuntimeAt 用给定的生命周期状态文件建运行时：同一个文件建第二个运行时就是模拟引擎重启（预载记录只在内存里，重启后清空）。
func newLifecycleRuntimeAt(t *testing.T, stateFile string, tasks ...domain.EngineTask) (*Runtime, *preloadDataFake) {
	t.Helper()
	cache, _ := newTestCache(t)
	if len(tasks) == 0 {
		tasks = []domain.EngineTask{engineTask("ftask_a", "dataset_a", true, "bias")}
	}
	sets := make([]domain.EngineTask, 0, len(tasks))
	for _, task := range tasks {
		sets = append(sets, uncompiled(task))
	}
	_, err := cache.Apply(CatalogSnapshot{Hash: "h", Sets: sets})
	require.NoError(t, err)
	fake := new(preloadDataFake)
	data := localdata.New(localdata.Config{})
	r := &Runtime{
		cfg:     &Config{Lifecycle: LifecycleConfig{StateFile: stateFile}},
		catalog: cache, lease: &leaseState{}, data: data, sources: data,
	}
	r.preload = r.newGatedPreloader(lifecycleSourceFake{}, fake, nil)
	r.life, err = r.newLifecycle()
	require.NoError(t, err)
	return r, fake
}

func checkStatus(checks []domain.TaskCheck, name string) string {
	for _, check := range checks {
		if check.Name == name {
			return check.Status
		}
	}
	return ""
}

// 窗口没载入不计算：检查写明“本地窗口”没过并唤醒预载；预载完成后立即重查、自动开始计算，不需要任何命令。
func TestTaskGateWaitsForTheWindowThenStartsAutomatically(t *testing.T) {
	ctx := context.Background()
	r, fake := newLifecycleRuntime(t)
	require.False(t, r.gate("ftask_a"), "还没检查过的任务不计算")

	r.refreshTaskChecks(ctx)
	require.False(t, r.gate("ftask_a"), "窗口没载入不计算")
	checks, ok := r.checks.get("ftask_a")
	require.True(t, ok)
	require.Equal(t, domain.TaskCheckFail, checkStatus(checks, "本地窗口"))
	select {
	case <-r.preload.kick:
	default:
		t.Fatal("窗口没载入时应唤醒预载器")
	}

	r.preload.once(ctx) // 预载循环把窗口载入
	require.Equal(t, []string{"ftask_a"}, fake.prepared)
	require.True(t, r.gate("ftask_a"), "载入后立即重查，不等下一轮就开始计算")
	checks, _ = r.checks.get("ftask_a")
	require.Equal(t, domain.TaskCheckPass, checkStatus(checks, "本地窗口"))

	// 目录变化触发重新预载：窗口一失效就不计算，不等下一轮重查。
	r.preload.unmark("ftask_a")
	require.False(t, r.gate("ftask_a"))
	require.NoError(t, r.preload.PrepareSet(ctx, "ftask_a"))
	require.True(t, r.gate("ftask_a"))

	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	require.False(t, r.gate("ftask_a"), "模块暂停后不计算")
}

// 没有启用成员的任务不计算，检查里写明原因；离开目录的任务不再留检查结果。
func TestTaskWithoutMembersIsNotComputed(t *testing.T) {
	ctx := context.Background()
	r, _ := newLifecycleRuntime(t, engineTask("ftask_empty", "dataset_a", true))
	require.NoError(t, r.preload.PrepareSet(ctx, "ftask_empty"))
	r.refreshTaskChecks(ctx)
	checks, _ := r.checks.get("ftask_empty")
	require.Equal(t, domain.TaskCheckFail, checkStatus(checks, "启用成员"))
	require.False(t, r.gate("ftask_empty"))

	_, err := r.catalog.Apply(CatalogSnapshot{Hash: "h2", Sets: []domain.EngineTask{uncompiled(engineTask("ftask_a", "dataset_a", true, "bias"))}})
	require.NoError(t, err)
	r.refreshTaskChecks(ctx)
	_, ok := r.checks.get("ftask_empty")
	require.False(t, ok, "离开目录的任务不再留检查结果")
}

// 目录未载入不领取补算作业：模块在运行也不领取；目录载入后领取，模块暂停后又不领取。
func TestRecalcIsNotLeasedBeforeTheCatalogIsLoaded(t *testing.T) {
	ctx := context.Background()
	cache, _ := newTestCache(t)
	r := &Runtime{cfg: &Config{Lifecycle: LifecycleConfig{StateFile: filepath.Join(t.TempDir(), "lifecycle.json")}}, catalog: cache, lease: &leaseState{}}
	var err error
	r.life, err = r.newLifecycle()
	require.NoError(t, err)
	require.True(t, r.life.Running())
	require.False(t, r.catalog.Loaded())
	require.False(t, r.leaseAllowed(), "目录未载入不领取")

	client := &jobClientFake{job: testJob("BTC"), found: true}
	loop := &recalcLoop{client: client, executor: recalcexec.NewExecutor(&planRecorder{}), source: &sourceFake{}, poll: time.Millisecond,
		leaseAllowed: r.leaseAllowed, moduleOpen: r.moduleRunning}
	found, err := loop.RunOnce(ctx)
	require.NoError(t, err)
	require.False(t, found)
	require.Zero(t, client.pulled, "目录未载入时不向管理端领取作业")

	_, err = cache.Apply(CatalogSnapshot{Hash: "h", Sets: []domain.EngineTask{uncompiled(engineTask("ftask_a", "dataset_a", true, "bias"))}})
	require.NoError(t, err)
	require.True(t, r.leaseAllowed(), "目录载入后领取")
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	require.False(t, r.leaseAllowed(), "模块暂停后不领取")
}

// 模块暂停、恢复追赶期间预载循环不运行，不会自己把数据拉回本地；放行后恢复。
func TestPreloaderIsBlockedWhileTheModuleIsHalted(t *testing.T) {
	r, fake := newLifecycleRuntime(t)
	ctx := context.Background()

	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.True(t, r.preloadBlocked())
	r.preload.once(ctx)
	require.Empty(t, fake.prepared, "暂停时预载循环不运行")

	// 恢复追赶自己载入窗口（不靠预载循环），追平后自动放行。
	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		r.life.Refresh(ctx)
		return !r.life.Halted()
	}, 3*time.Second, 20*time.Millisecond)
	require.False(t, r.preloadBlocked())
	r.preload.unmark("ftask_a") // 让窗口失效，看预载循环自己会不会再载入
	before := fake.count()
	r.preload.once(ctx)
	require.Greater(t, fake.count(), before, "放行后预载循环恢复")
}

// 已预载且回看够长的任务再次预载不重复做。
func TestPrepareSetSkipsWhenAlreadyPrepared(t *testing.T) {
	r, fake := newLifecycleRuntime(t)
	require.NoError(t, r.preload.PrepareSet(context.Background(), "ftask_a"))
	require.NoError(t, r.preload.PrepareSet(context.Background(), "ftask_a"))
	require.Len(t, fake.prepared, 1)
	require.Error(t, r.preload.PrepareSet(context.Background(), "missing"))
}

// 心跳里的任务：暂停标记跟着模块（暂停、放行前的恢复追赶），检查不通过的任务不标暂停；检查结果随心跳上报。
func TestMarkRunnersFollowsTheModuleAndCarriesChecks(t *testing.T) {
	ctx := context.Background()
	r, fake := newLifecycleRuntime(t)
	r.refreshTaskChecks(ctx)
	runners := []domain.RunnerStatus{{TaskID: "ftask_a"}}
	r.markRunners(runners)
	require.False(t, runners[0].Paused, "检查不通过不算暂停：照常做新鲜度检查")
	require.Equal(t, domain.TaskCheckFail, checkStatus(runners[0].Checks, "本地窗口"))

	require.NoError(t, r.preload.PrepareSet(ctx, "ftask_a"))
	// 恢复追赶的本地同步在后台进行；让它停在“同步中”，否则同步抢先完成时模块会直接追平放行。
	fake.hold = make(chan struct{})
	t.Cleanup(func() { close(fake.hold) })
	_, err := r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause})
	require.NoError(t, err)
	r.markRunners(runners)
	require.True(t, runners[0].Paused)
	require.Equal(t, domain.TaskCheckPass, checkStatus(runners[0].Checks, "本地窗口"))

	_, err = r.life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume})
	require.NoError(t, err)
	require.Equal(t, lifecycle.StageResuming, r.life.Stage())
	r.markRunners(runners)
	require.True(t, runners[0].Paused, "恢复追赶中、放行前仍算有意停下，Monitor 不对它做新鲜度检查")
}
