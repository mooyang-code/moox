package bootstrap

import (
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/trigger"
	"github.com/mooyang-code/moox/modules/strategy/internal/warmup"
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

// 预热没完成时返回等待（事件稍后重投，不丢期）；预热失败不再等；完成后照常。
func TestTriggerGateWaitsForPoolWarmUp(t *testing.T) {
	life := newTestLifecycle(t, nil)
	warm := fakeWarm{"pool": warmup.StatusRunning}
	gate := triggerGate(life, nil, warm)
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
		t.Fatal("预热完成后按模块状态处理")
	}
}
