package trigger

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
)

// 本文件验证规则状态跨会话沿用：停用再启用产生新会话，DSL 与绑定解析都没变时状态原样沿用，变了才清空。

const stopReenableDSL = `name: stop_demo
bar: 1h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    stop: {loss: 0.1, cooldown_bars: 3}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

const batchReenableDSL = `name: batch_demo
bar: 1h
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 1}
    holding: {bars: 3, offsets: [0, 1, 2]}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

func reenableResolved() input.Resolved {
	return input.Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]input.TaskBinding{"spot": {ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}}, Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: "f", FactorID: "f", DefinitionHash: "h1"}}, Factors: map[string]string{"f": "h1|fixed"}, ViewColumns: []string{"close", "volume", "m"}}
}

// newStopHarness：A 先领先入场（入场价 10），第 2 期跌到 8 触发止损并进入 3 根冷却；之后 A 的分数一直最高，
// 只有冷却还记着才不会被重新选中。
func newStopHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarnessWith(t, stopReenableDSL, nil, reenableResolved())
	closes := []float64{10, 8, 8, 8, 8}
	for n, closeA := range closes {
		h.frame(n+1, map[string]map[string]float64{
			"A": {"close": closeA, "volume": 100, "m": 9},
			"B": {"close": 10, "volume": 100, "m": 5},
			"C": {"close": 10, "volume": 100, "m": 3},
		})
	}
	return h
}

// newBatchHarness：每期领先的标的轮换，holding 分 3 批持有，每批记着自己的建仓期与标的。
func newBatchHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarnessWith(t, batchReenableDSL, nil, reenableResolved())
	leaders := []string{"A", "B", "C", "A", "B"}
	for n, leader := range leaders {
		rows := map[string]map[string]float64{}
		for _, id := range []string{"A", "B", "C"} {
			m := 1.0
			if id == leader {
				m = 9
			}
			rows[id] = map[string]float64{"close": 10, "volume": 100, "m": m}
		}
		h.frame(n+1, rows)
	}
	return h
}

// reenable 停用实例再启用，产生新会话：dslYaml 为空时定义不变，否则先把定义改成它；绑定解析沿用原会话的。
func (h *harness) reenable(next, dslYaml string) {
	h.t.Helper()
	ctx := context.Background()
	previous, err := h.repo.GetSession(ctx, h.session)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.repo.DisableInstance(ctx, "i1", h.session, h.now); err != nil {
		h.t.Fatal(err)
	}
	hash := previous.DSLHash
	if dslYaml == "" {
		version, err := h.repo.GetDefinitionVersion(ctx, hash)
		if err != nil {
			h.t.Fatal(err)
		}
		dslYaml = version.DSLYaml
	} else {
		hash = dsl.Hash([]byte(dslYaml))
		if err := h.repo.UpdateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "demo", DSLYaml: dslYaml, DSLHash: hash, UpdatedAt: h.now}); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := h.repo.EnableInstance(ctx, store.Session{SessionID: next, InstanceID: "i1", DSLHash: hash, ResolvedJSON: previous.ResolvedJSON, MembersJSON: previous.MembersJSON, CreatedAt: h.now}, dslYaml); err != nil {
		h.t.Fatal(err)
	}
	h.session = next
}

func (h *harness) run(from, to int) {
	h.t.Helper()
	for n := from; n <= to; n++ {
		h.now = barStart(n).Add(90 * time.Minute)
		expectAck(h.t, h.deliver(n))
	}
}

// 同一份 DSL 停用再启用：止损入场价与冷却、hold 的入场序号、holding 的分批持有都保留，
// 之后每一期的目标与状态和从未停用过的实例逐字节一致。
func TestReenableWithSameDSLKeepsRuleState(t *testing.T) {
	cases := []struct {
		name  string
		build func(*testing.T) *harness
		// stopAfter 是停用之前处理的最后一期，last 是比较的最后一期；carried 是停用那一刻状态里必须已经有的内容。
		stopAfter, last int
		carried         []string
	}{
		{name: "止损入场价与冷却", build: newStopHarness, stopAfter: 2, last: 5, carried: []string{`"cooldown":{"A":`}},
		{name: "止损入场价", build: newStopHarness, stopAfter: 1, last: 5, carried: []string{`"entries":{"A":`}},
		{name: "hold 入场序号", build: newHoldHarness, stopAfter: 2, last: 5, carried: []string{`"hold":{`}},
		{name: "holding 分批持有", build: newBatchHarness, stopAfter: 2, last: 5, carried: []string{`"batches":[`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			control := tc.build(t)
			control.run(1, tc.last)

			h := tc.build(t)
			h.run(1, tc.stopAfter)
			before := h.stateAt(tc.stopAfter)
			for _, part := range tc.carried {
				if !strings.Contains(before, part) {
					t.Fatalf("停用前的状态里应有 %s：%s", part, before)
				}
			}
			h.reenable("session-2", "")
			// 状态原样挂到新会话：内容与它推进到的那一期都不变。
			if after := h.stateAt(tc.stopAfter); after != before {
				t.Fatalf("重新启用后状态应原样沿用：\n之前 %s\n之后 %s", before, after)
			}
			for n := tc.stopAfter + 1; n <= tc.last; n++ {
				h.run(n, n)
				got, want := h.resultAt(n), control.resultAt(n)
				if got.SessionID != "session-2" || got.Status != want.Status || string(got.TargetsJSON) != string(want.TargetsJSON) {
					t.Fatalf("第 %d 期的结果应与从未停用一致：\n got  %s %s\n want %s %s", n, got.Status, got.TargetsJSON, want.Status, want.TargetsJSON)
				}
			}
			if got, want := h.stateAt(tc.last), control.stateAt(tc.last); got != want {
				t.Fatalf("最后一期的状态应与从未停用一致：\n got  %s\n want %s", got, want)
			}
		})
	}
}

// 停用期间漏掉一期再启用（沿用状态），与“同一期暂停、之后恢复”的对照逐期一致：普通策略不补漏掉的期，
// 持有根数、冷却、批次都按 bar 序号的差计算，两种中断是同一口径。
func TestReenableGapMatchesPause(t *testing.T) {
	cases := map[string]func(*testing.T) *harness{
		"止损": newStopHarness, "holding 分批": newBatchHarness, "hold": newHoldHarness,
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			control := build(t)
			mode := ModeLive
			control.handler.Gate = func(string) Mode { return mode }
			control.run(1, 2)
			mode = ModeOff
			control.run(3, 3)
			control.noResultAt(3)
			mode = ModeLive
			control.run(4, 5)

			h := build(t)
			h.run(1, 2)
			h.reenable("session-2", "")
			h.run(4, 5)
			for n := 4; n <= 5; n++ {
				got, want := h.resultAt(n), control.resultAt(n)
				if got.Status != want.Status || string(got.TargetsJSON) != string(want.TargetsJSON) {
					t.Fatalf("第 %d 期应与暂停后恢复一致：\n got  %s %s\n want %s %s", n, got.Status, got.TargetsJSON, want.Status, want.TargetsJSON)
				}
			}
			if got, want := h.stateAt(5), control.stateAt(5); got != want {
				t.Fatalf("最终状态应与暂停后恢复一致：\n got  %s\n want %s", got, want)
			}
		})
	}
}

// DSL 变了再启用：状态清空，新会话从头开始（这里是止损的冷却不再生效，A 重新被选中）。
func TestReenableWithChangedDSLClearsRuleState(t *testing.T) {
	h := newStopHarness(t)
	h.run(1, 2)
	if got := targetsOf(t, h.resultAt(2)); got["A"] != "" {
		t.Fatalf("第 2 期 A 应已止损出场：%v", got)
	}
	h.reenable("session-2", strings.Replace(stopReenableDSL, "name: stop_demo", "name: stop_demo_v2", 1))
	if _, found, err := h.repo.GetState(context.Background(), "i1"); err != nil || found {
		t.Fatalf("DSL 变了应清空状态：found=%v err=%v", found, err)
	}
	h.run(3, 3)
	if got := targetsOf(t, h.resultAt(3)); got["A"] != "1" {
		t.Fatalf("状态清空后冷却不再生效，A 应重新入选：%v", got)
	}
	var state engine.State
	if err := json.Unmarshal([]byte(h.stateAt(3)), &state); err != nil || len(state.Rules["r"].Cooldown) != 0 || state.Rules["r"].Entries["A"].Price != "8" {
		t.Fatalf("新会话的状态应从这一期重新开始（入场价 8、没有冷却）：%s err=%v", h.stateAt(3), err)
	}

	// 对照：DSL 没变时冷却沿用，第 3 期 A 不会被重新选中。
	kept := newStopHarness(t)
	kept.run(1, 2)
	kept.reenable("session-2", "")
	kept.run(3, 3)
	if got := targetsOf(t, kept.resultAt(3)); got["A"] != "" || got["B"] != "1" {
		t.Fatalf("DSL 没变时冷却应沿用：%v", got)
	}
}

// 沿用状态的新会话收到状态已经推进过的期（上一个会话里求值过）：只记 out_of_order，不重复推进状态。
func TestReenabledSessionDoesNotReevaluateBarsAlreadyInState(t *testing.T) {
	h := newStopHarness(t)
	h.run(1, 2)
	before := h.stateAt(2)
	h.reenable("session-2", "")
	calls := h.loader.calls
	h.now = barStart(2).Add(100 * time.Minute)
	expectAck(t, h.deliver(2))
	replayed := h.resultAt(2)
	if replayed.Status != store.StatusSkipped || replayed.SkipReason != store.SkipOutOfOrder {
		t.Fatalf("状态已推进过的期应记 out_of_order：%+v", replayed)
	}
	if h.loader.calls != calls {
		t.Fatalf("不应再装配输入：%d → %d", calls, h.loader.calls)
	}
	if after := h.stateAt(2); after != before {
		t.Fatalf("状态不应变化：\n之前 %s\n之后 %s", before, after)
	}
}

// 状态无法解析：清掉、本期记 config_error，下一期从头开始，不需要人工处理。
func TestUnparsableRuleStateIsClearedAndRestarts(t *testing.T) {
	h := newStopHarness(t)
	h.run(1, 1)
	if err := h.repo.ApplySchema(`UPDATE t_strategy_states SET c_state_json = '{"rules": 1}' WHERE c_instance_id = 'i1';`); err != nil {
		t.Fatal(err)
	}
	h.run(2, 2)
	if got := h.resultAt(2); got.Status != store.StatusSkipped || got.SkipReason != input.SkipConfigError {
		t.Fatalf("状态无法解析的那一期应记 config_error：%+v", got)
	}
	if _, found, _ := h.repo.GetState(context.Background(), "i1"); found {
		t.Fatal("无法解析的状态应被清掉")
	}
	h.run(3, 3)
	if got := h.resultAt(3); got.Status != store.StatusOK || targetsOf(t, got)["A"] != "1" {
		t.Fatalf("下一期应从头开始正常求值：%+v", got)
	}
}

// 策略池没有规则状态（正常不会出现：预热的终点与状态同事务写入）：不从空状态起算，记 config_error 等人工重新启用。
func TestPoolWithoutRuleStateIsNotEvaluatedFromScratch(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.warm(barStart(1), "")
	if err := h.repo.ApplySchema(`DELETE FROM t_strategy_states WHERE c_instance_id = 'i1';`); err != nil {
		t.Fatal(err)
	}
	h.frame(2, abcFrame(3, 2, 1))
	h.loader.prices[barStart(2).Unix()] = map[string]float64{"A|spot": 10, "B|spot": 10}
	expectAck(t, h.deliverWith(2, bothFactors))
	if got := h.resultAt(2); got.Status != store.StatusSkipped || got.SkipReason != input.SkipConfigError {
		t.Fatalf("池没有状态时应记 config_error：%+v", got)
	}
	if _, found, _ := h.repo.GetState(context.Background(), "i1"); found {
		t.Fatal("不应凭空写出状态")
	}
}
