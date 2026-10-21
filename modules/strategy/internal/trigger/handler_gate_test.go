package trigger

import (
	"testing"

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

// 门禁关闭：不求值、不留记录、不读输入；预热：求值并带影子标记；运行：正常目标。
func TestHandleGateModes(t *testing.T) {
	account := "acct-1"
	h := newHarness(t, rankDSL, &account)
	rows := map[string]map[string]float64{"A": {"m": 2}, "B": {"m": 1}}
	for n := 1; n <= 3; n++ {
		h.frame(n, rows)
	}
	mode := ModeOff
	h.handler.Gate = func(string) Mode { return mode }

	expectAck(t, h.deliver(1))
	h.noResultAt(1)
	if h.loader.calls != 0 {
		t.Fatalf("门禁关闭时不应读取输入：%d", h.loader.calls)
	}

	mode = ModeShadow
	expectAck(t, h.deliver(2))
	shadow := h.resultAt(2)
	if shadow.Status != store.StatusOK || !shadowFlagOf(t, shadow) {
		t.Fatalf("预热期的目标应带影子标记：%+v", shadow)
	}

	mode = ModeLive
	expectAck(t, h.deliver(3))
	live := h.resultAt(3)
	if live.Status != store.StatusOK || shadowFlagOf(t, live) {
		t.Fatalf("运行期的目标不应带影子标记：%+v", live)
	}
}
