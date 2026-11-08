package bootstrap

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/warmup"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"github.com/mooyang-code/moox/packages/lifecycle"
)

// fakeWarm 按实例返回预热状态。
type fakeWarm map[string]warmup.Status

func (f fakeWarm) State(instanceID string) (warmup.Status, string) {
	if status := f[instanceID]; status == warmup.StatusFailed {
		return status, "保留期不足"
	} else {
		return status, ""
	}
}

func (f fakeWarm) Blocked(instanceID string) string {
	switch f[instanceID] {
	case warmup.StatusRunning:
		return "策略池预热未完成"
	case warmup.StatusFailed:
		return "策略池预热失败，请修复后重新启用实例：保留期不足"
	}
	return ""
}

func runningLifecycle(t *testing.T, ids ...string) *lifecycle.Controller {
	t.Helper()
	refs := make([]lifecycle.Ref, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, instanceRef(id))
	}
	life, err := lifecycle.New(lifecycle.Options{Module: "strategy", Hooks: lifecycle.Hooks{
		Objects:     func() []lifecycle.Ref { return refs },
		CheckObject: func(context.Context, lifecycle.Ref) []lifecycle.Check { return nil },
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	life.Refresh(ctx)
	for _, ref := range refs {
		for _, cmd := range []lifecycle.Command{lifecycle.CmdInit, lifecycle.CmdStart} {
			if _, err := life.Command(ctx, cmd, ref); err != nil {
				t.Fatal(err)
			}
		}
	}
	return life
}

// 预热没完成时返回等待（事件稍后重投，不丢期），不管实例本身处于什么阶段；预热失败不再等；完成后照常。
func TestTriggerGateWaitsForPoolWarmUp(t *testing.T) {
	life := runningLifecycle(t, "pool", "plain")
	warm := fakeWarm{"pool": warmup.StatusRunning}
	gate := triggerGate(life, warm)
	if gate("pool") != trigger.ModeWait {
		t.Fatal("预热中应返回等待")
	}
	if gate("plain") != trigger.ModeShadow {
		t.Fatal("没有预热的实例不受影响")
	}
	if paramSubmitAllowed(gate)("pool") {
		t.Fatal("预热中不提交参数")
	}
	warm["pool"] = warmup.StatusFailed
	if gate("pool") != trigger.ModeOff {
		t.Fatal("预热失败不再等待，也不求值")
	}
	warm["pool"] = warmup.StatusNone
	if gate("pool") != trigger.ModeShadow {
		t.Fatal("预热完成后按生命周期阶段处理")
	}
}

// 预热没完成时拒绝 confirm；其他命令与其他实例不受影响；请求体照常交给控制器。
func TestConfirmGuardRejectsConfirmDuringWarmUp(t *testing.T) {
	life := runningLifecycle(t, "pool", "plain")
	warm := fakeWarm{"pool": warmup.StatusRunning}
	handler := confirmGuard(life.CommandHandler(), warm.Blocked)
	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, lifecycle.CommandPath, bytes.NewBufferString(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	blocked := post(`{"command":"confirm","kind":"strategy_instance","id":"pool"}`)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "策略池预热未完成") {
		t.Fatalf("预热中的确认应被拒绝：%d %s", blocked.Code, blocked.Body.String())
	}
	if life.Stage(instanceRef("pool")) != lifecycle.StageWarming {
		t.Fatal("被拒绝的确认不应改变阶段")
	}
	if ok := post(`{"command":"confirm","kind":"strategy_instance","id":"plain"}`); ok.Code != http.StatusOK {
		t.Fatalf("其他实例的确认照常：%d %s", ok.Code, ok.Body.String())
	}
	if life.Stage(instanceRef("plain")) != lifecycle.StageRunning {
		t.Fatal("没有预热的实例应进入运行")
	}
	if pause := post(`{"command":"pause","kind":"strategy_instance","id":"pool"}`); pause.Code != http.StatusOK {
		t.Fatalf("预热中的暂停不受限制：%d %s", pause.Code, pause.Body.String())
	}
	// 预热完成：可以确认（先恢复再确认）。
	warm["pool"] = warmup.StatusNone
	if _, err := life.Command(context.Background(), lifecycle.CmdResume, instanceRef("pool")); err != nil {
		t.Fatal(err)
	}
	if done := post(`{"command":"confirm","kind":"strategy_instance","id":"pool"}`); done.Code != http.StatusOK {
		t.Fatalf("预热完成后可以确认：%d %s", done.Code, done.Body.String())
	}
	if bad := post(`{`); bad.Code != http.StatusBadRequest {
		t.Fatalf("坏请求交给控制器处理：%d", bad.Code)
	}
}

// 没挂账户的观察实例本来自动确认，预热没完成时也不能。
func TestAutoConfirmWaitsForPoolWarmUp(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now().UTC()
	if err := db.CreateDefinition(ctx, store.Definition{StrategyID: "s", Name: "s", DSLYaml: "name: s", DSLHash: "sha256:s", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInstance(ctx, store.Instance{InstanceID: "observer", StrategyID: "s", SpaceID: "crypto", FactorTasks: map[string]string{"spot": "ftask_a"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	warm := fakeWarm{}
	deps := lifecycleDeps{db: db, warm: warm}
	if !deps.autoConfirm(instanceRef("observer"), time.Time{}) {
		t.Fatal("没有预热时观察实例自动确认")
	}
	warm["observer"] = warmup.StatusRunning
	if deps.autoConfirm(instanceRef("observer"), time.Time{}) {
		t.Fatal("预热没完成时不自动确认")
	}
}
