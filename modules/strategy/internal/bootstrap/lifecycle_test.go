package bootstrap

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// 实例要 init 之后才就绪；预热是影子模式，运行是正式模式，其他阶段不求值。
func TestTriggerGateFollowsLifecycleStage(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	life, err := newLifecycle(filepath.Join(dir, "strategy.sqlite"), lifecycleDeps{db: db})
	if err != nil {
		t.Fatal(err)
	}
	gate := triggerGate(life)
	ctx := context.Background()
	// 模块自身还没做第一次检查：暂时未就绪，事件稍后重投，不能丢弃。
	if gate("i1") != trigger.ModeWait {
		t.Fatal("模块还没检查时应让事件稍后重投")
	}
	if paramSubmitAllowed(gate)("i1") {
		t.Fatal("模块暂时未就绪时不提交参数")
	}
	life.Refresh(ctx)
	if gate("i1") != trigger.ModeOff {
		t.Fatal("未初始化的实例不应求值")
	}
	ref := instanceRef("i1")
	// 没有启用的实例：检查不过，实例不会就绪。
	obj, err := life.Command(ctx, lifecycle.CmdInit, ref)
	if err != nil || obj.Stage != lifecycle.StageNotReady {
		t.Fatalf("不存在的实例应是未就绪：%+v err=%v", obj, err)
	}
	if gate("i1") != trigger.ModeOff {
		t.Fatal("未就绪的实例不应求值")
	}
}

// 暂停时不求值；恢复追赶时只记录周期（ModeCatchup）；放行后回到原模式，并记下放行时间供处理器划定边界。
func TestTriggerGateCatchupModeAndReleaseBoundary(t *testing.T) {
	ref := instanceRef("i1")
	life, err := lifecycle.New(lifecycle.Options{Module: "strategy", Hooks: lifecycle.Hooks{
		Objects:     func() []lifecycle.Ref { return []lifecycle.Ref{ref} },
		CheckObject: func(context.Context, lifecycle.Ref) []lifecycle.Check { return nil },
		Catchup: func(context.Context, lifecycle.Ref, time.Time) (lifecycle.Catchup, error) {
			return lifecycle.Catchup{CaughtUp: caughtUpFlag.Load()}, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	life.Refresh(ctx)
	for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm} {
		if _, err := life.Command(ctx, cmd, ref); err != nil {
			t.Fatal(err)
		}
	}
	gate := triggerGate(life)
	if gate("i1") != trigger.ModeLive {
		t.Fatal("运行中是正式模式")
	}
	if !releasedAt(life)("i1").IsZero() {
		t.Fatal("没有放行过，放行时间为零")
	}

	if _, err := life.Command(ctx, lifecycle.CmdPause, ref); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeOff {
		t.Fatal("暂停不求值")
	}
	caughtUpFlag.Store(false)
	if _, err := life.Command(ctx, lifecycle.CmdResume, ref); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeCatchup {
		t.Fatal("恢复追赶中只记录周期")
	}
	caughtUpFlag.Store(true)
	life.Refresh(ctx)
	if gate("i1") != trigger.ModeLive {
		t.Fatal("追平放行后回到正式模式")
	}
	if releasedAt(life)("i1").IsZero() {
		t.Fatal("放行后应记录放行时间")
	}
}

var caughtUpFlag atomic.Bool

// 没挂账户的观察实例不会真实下单，试运行后自动确认进入运行；挂了账户的实例仍由用户确认。
func TestAutoConfirmOnlyObserverInstances(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	account := "acc-1"
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.CreateDefinition(ctx, store.Definition{StrategyID: "s", Name: "s", DSLYaml: "name: s", DSLHash: "sha256:s", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, instance := range []store.Instance{
		{InstanceID: "observer", StrategyID: "s", SpaceID: "crypto", FactorTaskID: "ftask_a", CreatedAt: now, UpdatedAt: now},
		{InstanceID: "trader", StrategyID: "s", SpaceID: "crypto", FactorTaskID: "ftask_b", LogicalAccountID: &account, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.CreateInstance(ctx, instance); err != nil {
			t.Fatal(err)
		}
	}
	deps := lifecycleDeps{db: db}
	if !deps.autoConfirm(instanceRef("observer"), time.Time{}) {
		t.Fatal("没挂账户的实例应自动确认")
	}
	if deps.autoConfirm(instanceRef("trader"), time.Time{}) {
		t.Fatal("挂了账户的实例不应自动确认")
	}
	if deps.autoConfirm(instanceRef("missing"), time.Time{}) || deps.autoConfirm(lifecycle.Ref{Kind: "factor_task", ID: "observer"}, time.Time{}) {
		t.Fatal("不存在的实例与别的对象类型都不自动确认")
	}
}

type fakeOutbox struct {
	counts []int
	calls  int
}

func (f *fakeOutbox) PendingOutboxStats(context.Context) (store.OutboxStats, error) {
	count := f.counts[min(f.calls, len(f.counts)-1)]
	f.calls++
	return store.OutboxStats{PendingCount: int64(count)}, nil
}

// 暂停等 outbox 排空：待发布的目标发完就返回；超时没发完要报出还有几个，不能当作已经停下。
func TestWaitOutboxDrainedWaitsForPublishAndReportsTimeout(t *testing.T) {
	draining := &fakeOutbox{counts: []int{3, 2, 0}}
	if err := waitOutboxDrained(context.Background(), draining); err != nil || draining.calls != 3 {
		t.Fatalf("应轮询到排空：calls=%d err=%v", draining.calls, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := waitOutboxDrained(ctx, &fakeOutbox{counts: []int{2}})
	if err == nil || !strings.Contains(err.Error(), "还有 2 个目标没有发布到 EventBus") {
		t.Fatalf("超时应报出剩余数量：%v", err)
	}
}

// 模块自身检查失败（例如重启后出站 EventBus 还没连上）：事件稍后重投而不是丢弃；检查恢复后照常求值。
// 启动期快速检查在模块就绪后立即返回，不用等满常规周期。
func TestTriggerGateWaitsWhileModuleNotReadyAndStartupRefreshReleasesQuickly(t *testing.T) {
	ref := instanceRef("i1")
	var connected atomic.Bool
	life, err := lifecycle.New(lifecycle.Options{Module: "strategy", Hooks: lifecycle.Hooks{
		CheckModule: func(context.Context) []lifecycle.Check {
			if connected.Load() {
				return []lifecycle.Check{{Name: "EventBus", Status: lifecycle.CheckPass}}
			}
			return []lifecycle.Check{{Name: "EventBus", Status: lifecycle.CheckFail, Message: "出站 EventBus 未连接"}}
		},
		Objects:     func() []lifecycle.Ref { return []lifecycle.Ref{ref} },
		CheckObject: func(context.Context, lifecycle.Ref) []lifecycle.Check { return nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	connected.Store(true)
	life.Refresh(ctx)
	for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart, lifecycle.CmdConfirm} {
		if _, err := life.Command(ctx, cmd, ref); err != nil {
			t.Fatal(err)
		}
	}
	gate := triggerGate(life)
	if gate("i1") != trigger.ModeLive {
		t.Fatal("模块与实例都就绪时正式求值")
	}

	connected.Store(false)
	life.Refresh(ctx)
	if gate("i1") != trigger.ModeWait {
		t.Fatal("模块检查失败时应让事件稍后重投")
	}

	// 一秒后连上：启动期快速检查应在窗口内放行。
	go func() {
		time.Sleep(50 * time.Millisecond)
		connected.Store(true)
	}()
	began := time.Now()
	refreshUntilReady(ctx, life, 10*time.Millisecond, 5*time.Second)
	if life.ModuleWaiting() || time.Since(began) > 2*time.Second {
		t.Fatalf("连上后应很快放行：仍在等待=%v 用时=%s", life.ModuleWaiting(), time.Since(began))
	}
	if gate("i1") != trigger.ModeLive {
		t.Fatal("模块恢复后实例回到正式求值")
	}

	// 窗口内一直没连上：到点退出，交给常规周期。
	connected.Store(false)
	life.Refresh(ctx)
	began = time.Now()
	refreshUntilReady(ctx, life, 10*time.Millisecond, 100*time.Millisecond)
	if !life.ModuleWaiting() || time.Since(began) > time.Second {
		t.Fatalf("超过窗口应退出：仍在等待=%v 用时=%s", life.ModuleWaiting(), time.Since(began))
	}

	// 人工暂停模块：不是暂时未就绪，按暂停处理（不求值、不重投）。
	connected.Store(true)
	life.Refresh(ctx)
	if _, err := life.Command(ctx, lifecycle.CmdPause, lifecycle.Ref{}); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeOff {
		t.Fatal("模块暂停时不求值，也不让事件重投")
	}
}
