package trigger

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// paramSubmission 在 ok 的结果上求出下一期的因子参数，作为待提交记录与结果同事务落库。
// 没有声明 factor_params、结果不是 ok、或没有任何成员求出参数时返回 nil（因子沿用上一期的参数）。
func (h *Handler) paramSubmission(p *period, result store.Result, decision engine.Decision, loaded input.Loaded) (*store.ParamSubmission, error) {
	rt := p.runtime
	if rt == nil || rt.program == nil || len(rt.program.Params) == 0 || rt.resolved.FactorTaskID == "" || decision.Status != engine.StatusOK {
		return nil, nil
	}
	outcome := engine.EvaluateParams(rt.program, loaded.Frame, rt.resolved.FactorParams)
	if len(outcome.Params) == 0 {
		if len(outcome.Notes) > 0 {
			h.logf("实例 %s 周期 %s 没有求出可提交的因子参数：%s", p.instance.InstanceID, p.boundary.BarEnd.Format(time.RFC3339), strings.Join(outcome.Notes, "；"))
		}
		return nil, nil
	}
	nextEnd, err := advance(rt.resolved.Calendar, rt.resolved.Bar, p.boundary.BarEnd, 1)
	if err != nil {
		return nil, fmt.Errorf("推算参数生效的下一期失败：%w", err)
	}
	next, err := input.FromBarEnd(rt.resolved.Calendar, rt.resolved.Bar, nextEnd)
	if err != nil {
		return nil, fmt.Errorf("推算参数生效的下一期失败：%w", err)
	}
	members := make(map[string]map[string]input.ParamValue, len(outcome.Params))
	for alias, params := range outcome.Params {
		members[alias] = make(map[string]input.ParamValue, len(params))
		for name, value := range params {
			members[alias][name] = input.ParamValue{Default: value.Default, Overrides: value.Overrides}
		}
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return nil, err
	}
	return &store.ParamSubmission{
		RequestID: "p-" + result.ResultID, TaskID: rt.resolved.FactorTaskID, PeriodTime: next.StorageStart, BasedOnPeriod: p.boundary.StorageStart,
		ParamsJSON: raw, Notes: strings.Join(outcome.Notes, "；"),
	}, nil
}
