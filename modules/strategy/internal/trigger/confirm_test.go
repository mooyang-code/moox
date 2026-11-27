package trigger

import (
	"context"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 本文件验证实盘还是影子看实例的确认列（批 4 T4、H7）：挂了账户、还没确认实盘的实例只发影子目标，确认后的下一期就是正式目标；
// 停用再启用回到试运行。门禁（模块状态）只决定求不求值，不决定实盘还是影子。

// rankRows 是两个标的的一期因子值。
var rankRows = map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 1}}

func (h *harness) instance() store.Instance {
	h.t.Helper()
	instance, err := h.repo.GetInstance(context.Background(), "i1")
	if err != nil {
		h.t.Fatal(err)
	}
	return instance
}

func (h *harness) confirm() {
	h.t.Helper()
	if err := h.repo.ConfirmInstance(context.Background(), "i1", h.session, h.now); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) shadowAt(n int) bool {
	h.t.Helper()
	result := h.resultAt(n)
	if result.Status != store.StatusOK {
		h.t.Fatalf("周期 %d 应正常求值：%+v", n, result)
	}
	return shadowFlagOf(h.t, result)
}

// 挂账户未确认只发影子：试运行期间每一期都带影子标记，门禁放行（运行阶段）也不例外；确认之后下一期是正式目标。
func TestTrialInstanceSendsOnlyShadowTargetsUntilConfirmed(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	for n := 1; n <= 4; n++ {
		h.frame(n, rankRows)
	}
	// 新会话从试运行开始（夹具默认已确认，这里停用再启用得到一个没确认的会话）。
	h.reenable("session-trial", "")
	if !h.instance().Trial() {
		t.Fatal("挂了账户的实例启用后应在试运行")
	}
	h.handler.Gate = func(string) Mode { return ModeLive }

	h.run(1, 1)
	if !h.shadowAt(1) {
		t.Fatal("没确认实盘的实例只能发影子目标")
	}
	h.run(2, 2)
	if !h.shadowAt(2) {
		t.Fatal("没确认实盘的实例只能发影子目标")
	}

	h.confirm()
	h.run(3, 3)
	if h.shadowAt(3) {
		t.Fatal("确认实盘之后的目标应是正式目标")
	}
	h.run(4, 4)
	if h.shadowAt(4) {
		t.Fatal("确认实盘之后的目标应是正式目标")
	}
}

// 确认后发实盘、停用再启用回到试运行：新会话的确认列为空，目标重新带影子标记，直到再次确认。
func TestReenabledInstanceReturnsToTrial(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	for n := 1; n <= 3; n++ {
		h.frame(n, rankRows)
	}
	h.run(1, 1)
	if h.shadowAt(1) {
		t.Fatal("已确认实盘的实例应发正式目标")
	}

	h.reenable("session-2", "")
	if instance := h.instance(); !instance.Trial() || instance.ConfirmedAt != nil {
		t.Fatalf("停用再启用应回到试运行：%+v", instance)
	}
	h.run(2, 2)
	if !h.shadowAt(2) {
		t.Fatal("重新启用后没确认之前只能发影子目标")
	}

	h.confirm()
	h.run(3, 3)
	if h.shadowAt(3) {
		t.Fatal("再次确认后应发正式目标")
	}
}
