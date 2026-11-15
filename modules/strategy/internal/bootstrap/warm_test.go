package bootstrap

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/warmup"
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
	if gate("plain") != trigger.ModeLive {
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
	if gate("pool") != trigger.ModeLive {
		t.Fatal("预热完成后按生命周期阶段处理")
	}
}

// lifecycle confirm 不再能确认策略实例（确认实盘写在确认列里，见 ConfirmStrategyInstance）：拒绝并指向新命令，
// 对象阶段不变；其他命令照常交给控制器。
func TestConfirmGuardRedirectsInstanceConfirm(t *testing.T) {
	life := runningLifecycle(t, "i1")
	handler := confirmGuard(life.CommandHandler())
	post := func(body string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, lifecycle.CommandPath, bytes.NewBufferString(body))
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	blocked := post(`{"command":"confirm","kind":"strategy_instance","id":"i1"}`)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "strategy instance confirm") {
		t.Fatalf("对策略实例的 confirm 应被拒绝并指向新命令：%d %s", blocked.Code, blocked.Body.String())
	}
	if life.Stage(instanceRef("i1")) != lifecycle.StageWarming {
		t.Fatal("被拒绝的确认不应改变阶段")
	}
	if pause := post(`{"command":"pause","kind":"strategy_instance","id":"i1"}`); pause.Code != http.StatusOK {
		t.Fatalf("其他命令照常：%d %s", pause.Code, pause.Body.String())
	}
	if bad := post(`{`); bad.Code != http.StatusBadRequest {
		t.Fatalf("坏请求交给控制器处理：%d", bad.Code)
	}
}

// 没挂账户的观察实例启用即确认、自动推进到运行，策略池预热没完成时也不推进。
func TestAutoConfirmWaitsForPoolWarmUp(t *testing.T) {
	db := openLifecycleStore(t)
	createAndEnable(t, db, "observer", "ftask_a", nil)
	warm := fakeWarm{}
	deps := lifecycleDeps{db: db, warm: warm}
	if !deps.autoConfirm(instanceRef("observer"), time.Time{}) {
		t.Fatal("没有预热时观察实例自动推进")
	}
	warm["observer"] = warmup.StatusRunning
	if deps.autoConfirm(instanceRef("observer"), time.Time{}) {
		t.Fatal("预热没完成时不推进")
	}
}
