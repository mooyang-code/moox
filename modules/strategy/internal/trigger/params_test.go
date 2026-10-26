package trigger

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/events"
)

const tunedDSL = `name: tuned
factor_params:
  f__var:
    window:
      default: "m * 10"
      per_subject: "m * 10 + 5"
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

func tunedResolved() input.Resolved {
	return input.Resolved{
		CompletionKind: events.CollectorPeriodCompleted.Name(), ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds", Bar: "1h", Calendar: input.DefaultCalendar, Spot: true,
		Columns:      map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f__var", FactorID: "f", MemberKind: "var", DefinitionHash: "h1"}},
		Factors:      map[string]string{"f__var": "h1|var"},
		ViewColumns:  []string{"close", "m"},
		FactorParams: map[string][]dsl.ParamDecl{"f__var": {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}},
	}
}

// 声明了 factor_params 的实例每期求出下一期的参数，随结果同事务落库；目标期次是下一根 bar，成员快照完整。
func TestHandleWritesNextPeriodParams(t *testing.T) {
	h := newHarnessWith(t, tunedDSL, nil, tunedResolved())
	h.frame(2, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 2}, "C": {"m": 1}})
	message, payload := h.event(2, map[string]string{"f__var": "h1"})
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	result := h.resultAt(2)
	if result.Status != store.StatusOK || len(result.NextParamsJSON) == 0 || result.ParamsDeliveredAt != nil {
		t.Fatalf("应为 ok 且带待投递的下一期参数：%+v", result)
	}
	pending, err := h.repo.PendingParamDeliveries(context.Background(), 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("应有一条待投递的参数：%+v err=%v", pending, err)
	}
	got := pending[0]
	if got.ResultID != result.ResultID || got.InstanceID != "i1" || !got.Period.Equal(barStart(3)) {
		t.Fatalf("记录不符（目标期次应是下一根 bar）：%+v", got)
	}
	var members map[string]map[string]input.ParamValue
	if err := json.Unmarshal(got.Members, &members); err != nil {
		t.Fatal(err)
	}
	// 统一值是 m*10 的中位数 20；per_subject 是 m*10+5：A=35、B=25、C=15，都与统一值不同。
	window := members["f__var"]["window"]
	if window.Default != 20 || window.Overrides["A"] != 35 || window.Overrides["B"] != 25 || window.Overrides["C"] != 15 {
		t.Fatalf("参数快照不符：%+v", members)
	}
}

// 结果是 skipped（缺数守门等）时不产出参数：因子沿用上一期；没有声明 factor_params 的实例不写参数。
func TestHandleSkipsParamsWhenSkippedOrUndeclared(t *testing.T) {
	h := newHarnessWith(t, tunedDSL, nil, tunedResolved())
	message, payload := h.event(2, map[string]string{"f__var": "h9"}) // 指纹变化 → skipped(factor_changed)
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if got := h.resultAt(2); got.Status != store.StatusSkipped || len(got.NextParamsJSON) != 0 {
		t.Fatalf("应为 skipped 且不带参数：%+v", got)
	}

	plain := newHarness(t, rankDSL, nil)
	plain.frame(2, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 2}})
	expectAck(t, plain.deliver(2))
	if got := plain.resultAt(2); len(got.NextParamsJSON) != 0 {
		t.Fatalf("没有 factor_params 的实例不应带参数：%+v", got)
	}
}
