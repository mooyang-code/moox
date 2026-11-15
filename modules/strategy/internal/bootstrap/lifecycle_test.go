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
	"github.com/mooyang-code/moox/modules/strategy/internal/warmup"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// switchCheck 是一个结果可切换的模块自检：connected 为假时报“出站 EventBus 未连接”。
func switchCheck(connected *atomic.Bool) *moduleCheck {
	return &moduleCheck{reason: "还没有做第一次检查", run: func(context.Context) string {
		if connected.Load() {
			return ""
		}
		return "出站 EventBus 未连接"
	}}
}

func newTestLifecycle(t *testing.T, catchup func(context.Context, time.Time) (lifecycle.Catchup, error)) *lifecycle.Controller {
	t.Helper()
	life, err := lifecycle.New(lifecycle.Options{Module: "strategy", Hooks: lifecycle.Hooks{Catchup: catchup}})
	if err != nil {
		t.Fatal(err)
	}
	return life
}

// 运行中求值；暂停不求值；恢复追赶只记录周期（ModeCatchup）；放行后回到正式求值。
// 放行时间是模块级的：系统恢复（追平后由编排器 release）同样记下，处理器据此让放行之前收盘的周期只记录、不求值。
func TestTriggerGateFollowsModuleStageAndSystemReleaseSetsTheBoundary(t *testing.T) {
	var caught atomic.Bool
	life := newTestLifecycle(t, func(context.Context, time.Time) (lifecycle.Catchup, error) {
		return lifecycle.Catchup{CaughtUp: caught.Load()}, nil
	})
	gate := triggerGate(life, nil, nil)
	ctx := context.Background()
	if gate("i1") != trigger.ModeLive || gate("any") != trigger.ModeLive {
		t.Fatal("运行中的模块对每个启用实例正式求值")
	}
	if !releasedAt(life)("i1").IsZero() {
		t.Fatal("没有放行过，放行时间为零")
	}

	if _, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause, PauseKind: lifecycle.PauseSystem}); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeOff || paramSubmitAllowed(gate)("i1") {
		t.Fatal("暂停不求值，也不提交参数")
	}
	if _, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdResume, PauseKind: lifecycle.PauseSystem, ManualRelease: true}); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeCatchup {
		t.Fatal("恢复追赶中只记录周期")
	}
	caught.Store(true)
	if _, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdRelease}); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeLive {
		t.Fatal("放行后回到正式求值")
	}
	released := releasedAt(life)
	if released("i1").IsZero() || !released("i1").Equal(released("i2")) {
		t.Fatalf("系统恢复放行后每个实例都看到同一个放行时间：%v %v", released("i1"), released("i2"))
	}
}

// 启动时模块自检还没通过（第一次检查之前、出站 EventBus 还没连上）：事件稍后重投、参数不提交；
// 自检通过后正常处理。启动期快速检查一通过就返回，不用等满常规周期。
func TestModuleCheckHoldsEventsAndParamsAtStartup(t *testing.T) {
	var connected atomic.Bool
	ready := switchCheck(&connected)
	life := newTestLifecycle(t, nil)
	gate := triggerGate(life, ready, nil)
	if gate("i1") != trigger.ModeWait {
		t.Fatal("第一次检查之前事件稍后重投，不能丢弃")
	}
	if paramSubmitAllowed(gate)("i1") {
		t.Fatal("自检没通过时不提交参数")
	}

	go func() {
		time.Sleep(50 * time.Millisecond)
		connected.Store(true)
	}()
	began := time.Now()
	ready.Refresh(context.Background())
	refreshUntilReady(context.Background(), ready, 10*time.Millisecond, 5*time.Second)
	if !ready.Ready() || time.Since(began) > 2*time.Second {
		t.Fatalf("连上后应很快放行：通过=%v 用时=%s", ready.Ready(), time.Since(began))
	}
	if gate("i1") != trigger.ModeLive || !paramSubmitAllowed(gate)("i1") {
		t.Fatal("自检通过后正常求值、提交参数")
	}

	// 窗口内一直没连上：到点退出，交给常规周期。
	connected.Store(false)
	ready.Refresh(context.Background())
	began = time.Now()
	refreshUntilReady(context.Background(), ready, 10*time.Millisecond, 100*time.Millisecond)
	if ready.Ready() || time.Since(began) > time.Second {
		t.Fatalf("超过窗口应退出：通过=%v 用时=%s", ready.Ready(), time.Since(began))
	}
}

// 运行中自检失败（例如出站 EventBus 断开）：事件稍后重投、参数不提交；自检恢复后正常处理。
// 模块暂停时不按自检处理：不求值、不让事件重投。
func TestModuleCheckHoldsEventsAndParamsWhileRunning(t *testing.T) {
	var connected atomic.Bool
	connected.Store(true)
	ready := switchCheck(&connected)
	ctx := context.Background()
	ready.Refresh(ctx)
	life := newTestLifecycle(t, nil)
	gate := triggerGate(life, ready, nil)
	if gate("i1") != trigger.ModeLive || !paramSubmitAllowed(gate)("i1") {
		t.Fatal("自检通过时正式求值")
	}

	connected.Store(false)
	ready.Refresh(ctx)
	if gate("i1") != trigger.ModeWait {
		t.Fatal("运行中自检失败：事件稍后重投")
	}
	if paramSubmitAllowed(gate)("i1") {
		t.Fatal("运行中自检失败：暂停提交参数")
	}
	if ready.Reason() != "出站 EventBus 未连接" {
		t.Fatalf("自检不通过的原因：%q", ready.Reason())
	}

	connected.Store(true)
	ready.Refresh(ctx)
	if gate("i1") != trigger.ModeLive || !paramSubmitAllowed(gate)("i1") {
		t.Fatal("自检恢复后正常处理")
	}

	connected.Store(false)
	ready.Refresh(ctx)
	if _, err := life.Execute(ctx, lifecycle.Request{Command: lifecycle.CmdPause}); err != nil {
		t.Fatal(err)
	}
	if gate("i1") != trigger.ModeOff {
		t.Fatal("模块暂停时不求值，也不让事件重投")
	}
}

// 模块自检用真实的数据库：库可用、两个连接都在时通过，任一项失败时给出原因。
func TestModuleCheckCoversDatabaseAndConnections(t *testing.T) {
	db := openLifecycleStore(t)
	var event, consumer atomic.Bool
	check := newModuleCheck(db, event.Load, consumer.Load)
	ctx := context.Background()
	if check.Ready() {
		t.Fatal("初值是未通过")
	}
	check.Refresh(ctx)
	if check.Ready() || check.Reason() != "出站 EventBus 未连接" {
		t.Fatalf("出站 EventBus 未连接：%q", check.Reason())
	}
	event.Store(true)
	check.Refresh(ctx)
	if check.Ready() || check.Reason() != "就绪事件消费者未连接" {
		t.Fatalf("就绪事件消费者未连接：%q", check.Reason())
	}
	consumer.Store(true)
	check.Refresh(ctx)
	if !check.Ready() {
		t.Fatalf("全部通过：%q", check.Reason())
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	check.Refresh(ctx)
	if check.Ready() || check.Reason() != "数据库不可用" {
		t.Fatalf("数据库不可用：%q", check.Reason())
	}
}

// 恢复追赶的进度是模块级的：自检没通过不算追平；每个启用、有会话的实例都要追平，第一个没追平的实例挡住放行；
// 策略池预热失败的实例与停用的实例不参与。
func TestCatchupWaitsForEveryEnabledInstance(t *testing.T) {
	db := openLifecycleStore(t)
	createAndEnable(t, db, "a", "ftask_a", nil)
	createAndEnable(t, db, "b", "ftask_b", nil)
	createAndEnable(t, db, "off", "ftask_c", nil)
	if err := db.DisableInstance(context.Background(), "off", "session-off", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var connected atomic.Bool
	ready := switchCheck(&connected)
	var handler atomic.Pointer[trigger.Handler]
	handler.Store(&trigger.Handler{Store: db})
	warm := fakeWarm{"a": warmup.StatusFailed}
	deps := lifecycleDeps{db: db, handler: &handler, warm: warm, ready: ready}
	ctx := context.Background()

	got, err := deps.catchup(ctx, time.Now())
	if err != nil || got.CaughtUp || got.BlockedBy != "模块自检" {
		t.Fatalf("自检没通过不算追平：%+v err=%v", got, err)
	}
	connected.Store(true)
	ready.Refresh(ctx)
	// 测试库里的绑定快照没有周期，读不出进度：按没追平处理，原因写进进度里。
	got, err = deps.catchup(ctx, time.Now())
	if err != nil || got.CaughtUp || got.BlockedBy != "实例 b" || got.Total != 1 || !strings.Contains(got.Message, "读取追赶进度失败") {
		t.Fatalf("被没追平的实例 b 挡住，预热失败的 a 与停用的实例不参与：%+v err=%v", got, err)
	}
	warm["b"] = warmup.StatusFailed
	got, err = deps.catchup(ctx, time.Now())
	if err != nil || !got.CaughtUp {
		t.Fatalf("没有要追的实例时追平：%+v err=%v", got, err)
	}
}

// openLifecycleStore 打开一个空的策略库并建好定义 s。
func openLifecycleStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.CreateDefinition(context.Background(), store.Definition{StrategyID: "s", Name: "s", DSLYaml: "name: s", DSLHash: "sha256:s", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return db
}

// createAndEnable 建一个绑定现货任务 taskID 的实例并启用（会话 session-<实例>）；account 为空表示观察实例。
func createAndEnable(t *testing.T, db *store.Store, instanceID, taskID string, account *string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.CreateInstance(ctx, store.Instance{InstanceID: instanceID, StrategyID: "s", SpaceID: "crypto", FactorTasks: map[string]string{"spot": taskID}, LogicalAccountID: account, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	resolved := `{"tasks":{"spot":{"factor_task_id":"` + taskID + `"}}}`
	if err := db.EnableInstance(ctx, store.Session{SessionID: "session-" + instanceID, InstanceID: instanceID, DSLHash: "sha256:s", ResolvedJSON: resolved, CreatedAt: now}, "name: s"); err != nil {
		t.Fatal(err)
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
