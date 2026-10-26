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

// nextParams 在 ok 的结果上求出下一期可变成员的参数，编码成结果里的 next_params_json，与结果同事务落库，之后由投递循环异步提交。
// 没有声明 factor_params、结果不是 ok、或没有任何成员求出参数时返回 nil（因子沿用最近一次提交的参数）。
func (h *Handler) nextParams(p *period, decision engine.Decision, loaded input.Loaded) (json.RawMessage, error) {
	rt := p.runtime
	if rt == nil || rt.program == nil || len(rt.program.Params) == 0 || decision.Status != engine.StatusOK {
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
	for member, params := range outcome.Params {
		members[member] = make(map[string]input.ParamValue, len(params))
		for name, value := range params {
			members[member][name] = input.ParamValue{Default: value.Default, Overrides: value.Overrides}
		}
	}
	raw, err := json.Marshal(members)
	if err != nil {
		return nil, err
	}
	if len(outcome.Notes) > 0 {
		h.logf("实例 %s 周期 %s 的因子参数求值说明：%s", p.instance.InstanceID, p.boundary.BarEnd.Format(time.RFC3339), strings.Join(outcome.Notes, "；"))
	}
	return store.EncodeNextParams(next.StorageStart, raw)
}
