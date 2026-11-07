package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 本文件是策略池的实时求值（设计 3.5）：每个成员各自做就绪判断、读自己的输入、独立求值，再由 pool.Step 分配资金并合成池目标。

// processedThrough 是实例已处理到的那一期：最近处理的一期；策略池再加上预热终点，两者取晚的。
// 补处理与补记都只看它之后的期，预热窗口里等待期间记下的到达事件不会被当作还没处理。
func processedThrough(rt *runtime, latest store.Result, hasLatest bool) time.Time {
	var through time.Time
	if hasLatest {
		through = latest.BarEndTime
	}
	if rt.pool != nil && rt.pool.warmedThrough.After(through) {
		through = rt.pool.warmedThrough
	}
	return through
}

// checkPoolReady 对每个成员各自做就绪判断（只看它自己用到的市场与因子，相邻周期的核对也按成员的 View 与因子），
// 池自己的结论只在所有成员都没通过时才有原因：取需要人工处理的原因优先，否则取第一个成员的原因。
func (h *Handler) checkPoolReady(ctx context.Context, p *period) (mergedReadiness, error) {
	rt := p.runtime
	out := mergedReadiness{Members: make(map[string]mergedReadiness, len(rt.pool.members)), Results: map[string]readiness.Result{}, Unavailable: map[string]string{}}
	calendar, bar := rt.resolved.Calendar, rt.resolved.Bar
	for _, member := range rt.pool.members {
		var adjacent *store.InputRecord
		if len(member.Resolved.PreviousFactors) > 0 {
			previousEnd, err := advance(calendar, bar, p.boundary.BarEnd, -1)
			if err != nil {
				out.Members[member.ID] = mergedReadiness{Reason: input.SkipConfigError, Detail: err.Error()}
				continue
			}
			_, record, found, err := h.Store.AdjacentRecord(ctx, p.instance.InstanceID, member.Resolved.ViewIDs(), bar, calendar, previousEnd, member.Resolved.PreviousFactors, rt.sessionID)
			if err != nil {
				return mergedReadiness{}, err
			}
			if found {
				adjacent = &record
			}
		}
		out.Members[member.ID] = h.checkReadyFor(p, member.Resolved, adjacent)
	}
	var first, degrading *struct{ id, reason, detail string }
	failed := 0
	for _, member := range rt.pool.members {
		ready := out.Members[member.ID]
		if ready.Reason == "" {
			continue
		}
		failed++
		entry := &struct{ id, reason, detail string }{member.ID, ready.Reason, ready.Detail}
		if first == nil {
			first = entry
		}
		if degrading == nil && degradingReasons[ready.Reason] {
			degrading = entry
		}
	}
	if failed == len(rt.pool.members) {
		pick := first
		if degrading != nil {
			pick = degrading
		}
		out.Reason, out.Detail = pick.reason, fmt.Sprintf("成员 %s：%s", pick.id, pick.detail)
	}
	return out, nil
}

// evaluatePool 读取各成员的输入并独立求值，再交给 pool.Step。成员本期没通过（就绪判断没过、输入配置类问题、求值被跳过）时
// 沿用它上一期的目标权重；基础设施错误按（事件, 实例）计尝试预算，与普通策略一致。返回 nil 决策表示本期已作为 skipped 落库。
func (h *Handler) evaluatePool(ctx context.Context, p *period, key string, ready mergedReadiness) (*engine.Decision, input.Loaded, error) {
	rt := p.runtime
	evalStart := h.now()
	defer func() { p.timing.eval = h.now().Sub(evalStart) }()
	previous, ok, err := h.previousState(ctx, p)
	if err != nil || !ok {
		return nil, input.Loaded{}, err
	}
	results := make([]pool.MemberResult, len(rt.pool.members))
	var steps []input.Step
	runStart := h.now()
	for i, member := range rt.pool.members {
		memberReady := ready.Members[member.ID]
		if memberReady.Reason != "" {
			results[i] = pool.MemberResult{ID: member.ID, Reason: memberReady.Reason, Detail: memberReady.Detail}
			continue
		}
		loaded, skip, terminal, err := h.loadInput(ctx, p, key, member.Resolved, rt.pool.programs[i], memberReady)
		if terminal || err != nil {
			return nil, input.Loaded{}, err
		}
		if skip != nil {
			results[i] = pool.MemberResult{ID: member.ID, Reason: skip.Reason, Detail: skip.Detail}
			continue
		}
		steps = append(steps, loaded.Steps...)
		decision, err := engine.Evaluate(rt.pool.programs[i], loaded.Frame, previous.Members[member.ID])
		if err != nil {
			results[i] = pool.MemberResult{ID: member.ID, Reason: input.SkipConfigError, Detail: err.Error()}
			continue
		}
		results[i] = pool.MemberResult{ID: member.ID, Decision: &decision}
	}
	p.timing.evalRun = h.now().Sub(runStart)
	keys, err := pool.PriceKeys(previous, results)
	if err != nil {
		return nil, input.Loaded{}, h.commitSkipped(ctx, p, input.SkipConfigError, err.Error())
	}
	var prices map[string]float64
	skip, terminal, err := h.withReads(ctx, p, key, func() error {
		var readErr error
		prices, readErr = p.loader.LoadPrices(ctx, p.instance.SpaceID, rt.resolved, input.Bar{BarStart: p.boundary.StorageStart}, keys)
		return readErr
	})
	if terminal || err != nil {
		return nil, input.Loaded{}, err
	}
	if skip != nil {
		return nil, input.Loaded{}, h.commitSkipped(ctx, p, skip.Reason, skip.Detail)
	}
	stepStart := h.now()
	decision, err := pool.Step(rt.pool.config, previous, pool.Input{BarIndex: p.boundary.BarIndex, Prices: prices, Members: results})
	p.timing.evalRun += h.now().Sub(stepStart)
	if err != nil {
		return nil, input.Loaded{}, h.commitSkipped(ctx, p, input.SkipConfigError, err.Error())
	}
	return &decision, input.Loaded{Boundary: p.boundary, Steps: steps}, nil
}

// carriedState 是 skipped 记录里写的规则状态：策略池的 skipped 记录也带完整状态（最近一条带状态的结果，没有时是预热完成时的状态），
// 不写空对象，状态才不会因为跳过的期而缺口；普通策略仍写空对象（它的状态从最近一条带状态的结果恢复）。
func (h *Handler) carriedState(ctx context.Context, p *period) (json.RawMessage, error) {
	empty := json.RawMessage(`{}`)
	rt := p.runtime
	if rt.pool == nil {
		return empty, nil
	}
	last, found, err := h.Store.LatestState(ctx, p.instance.InstanceID, rt.sessionID)
	if err != nil {
		return nil, err
	}
	if found {
		return last.RuleStatesJSON, nil
	}
	if len(rt.pool.warmState) > 0 && string(rt.pool.warmState) != "{}" {
		return rt.pool.warmState, nil
	}
	return empty, nil
}

// degradedMembers 返回带需要人工处理的原因的池成员（成员 id：原因）：成员本期没通过的原因在 degradingReasons 里。普通策略与没有这类成员时为空。
func degradedMembers(decision engine.Decision) []string {
	if decision.Summary.Pool == nil {
		return nil
	}
	var degraded []string
	for _, member := range decision.Summary.Pool.Members {
		if member.Status != engine.StatusOK && degradingReasons[member.Reason] {
			degraded = append(degraded, member.ID+"："+member.Reason)
		}
	}
	return degraded
}
