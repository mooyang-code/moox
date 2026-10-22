package bootstrap

import (
	"context"
	"path/filepath"
	"testing"

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
