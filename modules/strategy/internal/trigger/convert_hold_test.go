package trigger

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/quant"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
	"github.com/mooyang-code/moox/packages/tradeeventpb"
)

// 保持标记要贯穿 targets_json 与发往 Trade 的目标事件，Trade 才不会把保持标的当作目标权重去调仓。
func TestHoldFlagReachesTargetsJSONAndTradeEvent(t *testing.T) {
	items, raw, err := EncodeTargets([]engine.Target{
		{InstrumentID: "A", Weight: quant.Must("0.5"), Hold: true},
		{InstrumentID: "B", Weight: quant.Must("0.5")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []TargetJSON
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) != 2 || !decoded[0].Hold || decoded[1].Hold {
		t.Fatalf("targets_json 不符：%s err=%v", raw, err)
	}
	if string(raw) != `[{"instrument_id":"A","target_weight":"0.5","hold":true},{"instrument_id":"B","target_weight":"0.5"}]` {
		t.Fatalf("非保持目标的 JSON 不应出现 hold 字段：%s", raw)
	}

	account := "acc-1"
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	instance := store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "crypto", LogicalAccountID: &account}
	result := store.Result{ResultID: "r1", InstanceID: "i1", SessionID: "sess", BarEndTime: now, ValidUntil: now.Add(time.Hour), CreatedAt: now}
	data, err := MarshalTargetEvent(instance, result, items)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, err := registry.UnmarshalMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	subject, err := registry.SubjectForMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := events.DecodeRaw(registry, data, subject, message.GetEventId(), events.ContentType)
	if err != nil {
		t.Fatal(err)
	}
	targets := payload.(*tradeeventpb.LogicalAccountTargetWeightRequested).GetTargets()
	if len(targets) != 2 || !targets[0].GetHold() || targets[1].GetHold() || targets[0].GetTargetWeight() != "0.5" {
		t.Fatalf("事件里的保持标记不符：%v", targets)
	}
}
