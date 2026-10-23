package bootstrap

import (
	"context"
	"path/filepath"
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
	if gate("i1") != trigger.ModeOff {
		t.Fatal("未初始化的实例不应求值")
	}
	ctx := context.Background()
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
