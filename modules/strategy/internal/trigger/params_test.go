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
factors:
  fast: {factor: f}
factor_params:
  fast:
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
		CompletionKind: events.CollectorPeriodCompleted.Name(), ViewID: "view_a", DatasetID: "ds", Bar: "1h", Calendar: input.DefaultCalendar, Spot: true,
		Columns:     map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "fast", FactorID: "f", DefinitionHash: "h1"}},
		Factors:     map[string]string{"fast": "h1"},
		ViewColumns: []string{"close", "m"}, FactorTaskID: "ftask_i1",
		FactorParams: map[string][]dsl.ParamDecl{"fast": {{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}},
	}
}

// 声明了 factor_params 的实例每期求出下一期的参数，与结果同事务落库；目标期次是下一根 bar，成员快照完整。
func TestHandleWritesNextPeriodParamSubmission(t *testing.T) {
	h := newHarnessWith(t, tunedDSL, nil, tunedResolved())
	h.frame(2, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 2}, "C": {"m": 1}})
	message, payload := h.event(2, map[string]string{"fast": "h1"})
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	result := h.resultAt(2)
	if result.Status != store.StatusOK {
		t.Fatalf("应为 ok：%+v", result)
	}
	submissions, err := h.repo.ListParamSubmissions(context.Background(), "i1", 10)
	if err != nil || len(submissions) != 1 {
		t.Fatalf("应有一条待提交的参数：%+v err=%v", submissions, err)
	}
	got := submissions[0]
	if got.Status != store.ParamPending || got.TaskID != "ftask_i1" || got.ResultID != result.ResultID || got.RequestID != "p-"+result.ResultID {
		t.Fatalf("记录不符：%+v", got)
	}
	if !got.PeriodTime.Equal(barStart(3)) || !got.BasedOnPeriod.Equal(barStart(2)) {
		t.Fatalf("目标期次应是下一根 bar：period=%s based_on=%s", got.PeriodTime, got.BasedOnPeriod)
	}
	var members map[string]map[string]input.ParamValue
	if err := json.Unmarshal(got.ParamsJSON, &members); err != nil {
		t.Fatal(err)
	}
	// 统一值是 m*10 的中位数 20；per_subject 是 m*10+5：A=35、B=25、C=15，都与统一值不同。
	window := members["fast"]["window"]
	if window.Default != 20 || window.Overrides["A"] != 35 || window.Overrides["B"] != 25 || window.Overrides["C"] != 15 {
		t.Fatalf("参数快照不符：%+v", members)
	}
}

// 结果是 skipped（缺数守门等）时不提交参数：因子沿用上一期；没有声明 factor_params 的实例不写待提交记录。
func TestHandleSkipsParamSubmissionWhenSkippedOrUndeclared(t *testing.T) {
	h := newHarnessWith(t, tunedDSL, nil, tunedResolved())
	message, payload := h.event(2, map[string]string{"fast": "h9"}) // 指纹变化 → skipped(factor_changed)
	expectAck(t, h.handler.Handle(context.Background(), message, payload))
	if got := h.resultAt(2); got.Status != store.StatusSkipped {
		t.Fatalf("应为 skipped：%+v", got)
	}
	if submissions, _ := h.repo.ListParamSubmissions(context.Background(), "i1", 10); len(submissions) != 0 {
		t.Fatalf("skipped 不应提交参数：%+v", submissions)
	}

	plain := newHarness(t, rankDSL, nil)
	plain.frame(2, map[string]map[string]float64{"A": {"m": 3}, "B": {"m": 2}})
	expectAck(t, plain.deliver(2))
	if submissions, _ := plain.repo.ListParamSubmissions(context.Background(), "i1", 10); len(submissions) != 0 {
		t.Fatalf("没有 factor_params 的实例不应有待提交记录：%+v", submissions)
	}
}
