package trigger

import (
	"errors"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

func shadowFlagOf(t *testing.T, result store.Result) bool {
	t.Helper()
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, err := registry.UnmarshalMessage(result.EventData)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.SubjectForMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := events.DecodeRaw(registry, result.EventData, subject, message.GetEventId(), events.ContentType)
	if err != nil {
		t.Fatalf("目标事件不符合契约：%v", err)
	}
	return payload.(*tradeeventpb.LogicalAccountTargetWeightRequested).GetShadow()
}

// 门禁关闭：不求值、不留记录、不读输入；门禁放行：求值，已确认实盘的实例发正式目标。
func TestHandleGateModes(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	rows := map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 1}}
	for n := 1; n <= 2; n++ {
		h.frame(n, rows)
	}
	mode := ModeOff
	h.handler.Gate = func(string) Mode { return mode }

	expectAck(t, h.deliver(1))
	h.noResultAt(1)
	if h.loader.calls != 0 {
		t.Fatalf("门禁关闭时不应读取输入：%d", h.loader.calls)
	}

	mode = ModeLive
	expectAck(t, h.deliver(2))
	live := h.resultAt(2)
	if live.Status != store.StatusOK || shadowFlagOf(t, live) {
		t.Fatalf("已确认实盘的实例目标不应带影子标记：%+v", live)
	}
}

// 模块暂时未就绪（重启后出站 EventBus 还在连接）：事件不确认、不留记录、不读输入，等重投；
// 就绪后重投的同一事件照常求值，这一期不会丢。
func TestHandleWaitsWhileModuleNotReadyThenEvaluatesOnRedelivery(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	h.frame(1, map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 1}})
	mode := ModeWait
	h.handler.Gate = func(string) Mode { return mode }

	err := h.deliver(1)
	expectRetry(t, err)
	if !errors.Is(err, ErrModuleNotReady) {
		t.Fatalf("暂时未就绪应返回 ErrModuleNotReady：%v", err)
	}
	h.noResultAt(1)
	if h.loader.calls != 0 {
		t.Fatalf("暂时未就绪时不应读取输入：%d", h.loader.calls)
	}

	mode = ModeLive
	expectAck(t, h.deliver(1))
	if result := h.resultAt(1); result.Status != store.StatusOK {
		t.Fatalf("就绪后重投的周期应正常求值：%+v", result)
	}
}

// 上游数据集历史补齐中：这一期记 skipped(history_seeding)，不读输入、不产生目标；补齐结束后下一期照常求值。
func TestHandleSkipsWhileUpstreamSeeding(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	rows := map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 1}}
	for n := 1; n <= 2; n++ {
		h.frame(n, rows)
	}
	seeding := true
	h.handler.Seeding = func(string, input.Resolved) bool { return seeding }

	expectAck(t, h.deliver(1))
	skipped := h.resultAt(1)
	if skipped.Status != store.StatusSkipped || skipped.SkipReason != store.SkipHistorySeeding {
		t.Fatalf("补齐中应记 skipped(history_seeding)：%+v", skipped)
	}
	if skipped.PublishStatus != store.PublishNone || string(skipped.TargetsJSON) != "[]" {
		t.Fatalf("补齐中不应产生目标：%+v", skipped)
	}
	if h.loader.calls != 0 {
		t.Fatalf("补齐中不应读取输入：%d", h.loader.calls)
	}

	seeding = false
	expectAck(t, h.deliver(2))
	if result := h.resultAt(2); result.Status != store.StatusOK {
		t.Fatalf("补齐结束后的下一期应正常求值：%+v", result)
	}
}
