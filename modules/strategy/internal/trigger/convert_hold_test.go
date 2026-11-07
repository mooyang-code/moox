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
		{InstrumentID: "A", Market: "spot", Weight: quant.Must("0.5"), Hold: true},
		{InstrumentID: "B", Market: "spot", Weight: quant.Must("0.5")},
	})
	if err != nil {
		t.Fatal(err)
	}
	var decoded []TargetJSON
	if err := json.Unmarshal(raw, &decoded); err != nil || len(decoded) != 2 || !decoded[0].Hold || decoded[1].Hold {
		t.Fatalf("targets_json 不符：%s err=%v", raw, err)
	}
	if string(raw) != `[{"instrument_id":"A","market":"spot","target_weight":"0.5","hold":true},{"instrument_id":"B","market":"spot","target_weight":"0.5"}]` {
		t.Fatalf("非保持目标的 JSON 不应出现 hold 字段：%s", raw)
	}

	account := "acc-1"
	now := time.Date(2026, 10, 14, 6, 0, 0, 0, time.UTC)
	instance := store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "crypto", LogicalAccountID: &account}
	result := store.Result{ResultID: "r1", InstanceID: "i1", SessionID: "sess", BarEndTime: now, ValidUntil: now.Add(time.Hour), CreatedAt: now}
	data, err := MarshalTargetEvent(instance, result, items, false)
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

// 同一个币的现货、永续各是一个目标：两项都带各自的 market_type，事件契约校验通过；同一（标的, 市场）重复则被拒绝。
func TestSpotAndSwapTargetsOfSameInstrumentReachTradeEvent(t *testing.T) {
	items, _, err := EncodeTargets([]engine.Target{
		{InstrumentID: "BTC-USDT", Market: "spot", Weight: quant.Must("0.5")},
		{InstrumentID: "BTC-USDT", Market: "swap", Weight: quant.Must("-0.3")},
	})
	if err != nil {
		t.Fatal(err)
	}
	account := "acc-1"
	now := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	instance := store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "crypto", LogicalAccountID: &account}
	result := store.Result{ResultID: "r1", InstanceID: "i1", SessionID: "sess", BarEndTime: now, ValidUntil: now.Add(time.Hour), CreatedAt: now}
	data, err := MarshalTargetEvent(instance, result, items, false)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := events.DefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	message, err := registry.UnmarshalMessage(data)
	if err != nil {
		t.Fatalf("同一个币的现货、永续两项应通过事件契约校验：%v", err)
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
	if len(targets) != 2 || targets[0].GetInstrumentId() != "BTC-USDT" || targets[0].GetMarketType() != "SPOT" || targets[1].GetInstrumentId() != "BTC-USDT" || targets[1].GetMarketType() != "SWAP" || targets[1].GetTargetWeight() != "-0.3" {
		t.Fatalf("事件里的两项目标不符：%v", targets)
	}
	// 同一（标的, 市场）重复仍拒绝；股票映射为 STOCK，未知市场无法生成事件。
	dup := []TargetJSON{{InstrumentID: "BTC-USDT", Market: "spot", TargetWeight: "0.1"}, {InstrumentID: "BTC-USDT", Market: "spot", TargetWeight: "0.2"}}
	if data, err = MarshalTargetEvent(instance, result, dup, false); err == nil {
		if _, err = registry.UnmarshalMessage(data); err == nil {
			t.Fatal("同一（标的, 市场）重复应被事件契约拒绝")
		}
	}
	if _, err := MarshalTargetEvent(instance, result, []TargetJSON{{InstrumentID: "A", Market: "option", TargetWeight: "1"}}, false); err == nil {
		t.Fatal("未知市场应报错")
	}
}
