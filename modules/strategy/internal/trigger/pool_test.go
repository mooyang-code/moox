package trigger

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
)

const poolMemberDSL = `name: member
bar: 1h
market: {select: spot}
rules:
  - id: r
    type: rank
    score: "m"
    select: {top: 2, buffer: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

func poolDSL(ratios string) string {
	return `name: pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: s_a}
    - {id: b, strategy: s_b}
  allocation: {method: fixed, ratios: ` + ratios + `}
  fees: {spot_bps: 0, swap_bps: 0}
`
}

// poolHarness 搭一个已启用的策略池实例：成员 a 绑定因子 f，成员 b 绑定因子 g（同一个 View）。
type poolHarness struct {
	*harness
	warmedThrough time.Time
}

func newPoolHarness(t *testing.T, poolYaml string, members []input.PoolMember, account *string) *poolHarness {
	t.Helper()
	pooled, err := dsl.Parse([]byte(poolYaml))
	if err != nil {
		t.Fatal(err)
	}
	if pooled.Pool == nil {
		t.Fatal("测试需要策略池 DSL")
	}
	task := input.TaskBinding{ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}
	resolved := input.Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]input.TaskBinding{"spot": task}, Bar: "1h", Calendar: input.DefaultCalendar,
		Columns: map[string]input.ColumnBinding{}, Factors: map[string]string{"f": "h1|fixed", "g": "hg|fixed"}, ViewColumns: []string{"close", "m"}}
	repo, err := store.Open(filepath.Join(t.TempDir(), "strategy.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.Close() })
	if err := repo.ApplySchema(schema.AllSQL()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash := dsl.Hash([]byte(poolYaml))
	if err := repo.CreateDefinition(ctx, store.Definition{StrategyID: "s1", Name: "pool", DSLYaml: poolYaml, DSLHash: hash, CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateInstance(ctx, store.Instance{InstanceID: "i1", StrategyID: "s1", SpaceID: "space", FactorTasks: map[string]string{"spot": "ftask_a"}, LogicalAccountID: account, CreatedAt: bar0}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(resolved)
	membersJSON, err := json.Marshal(members)
	if err != nil {
		t.Fatal(err)
	}
	session := "session-1"
	if err := repo.OpenSession(ctx, store.Session{SessionID: session, InstanceID: "i1", DSLHash: hash, ResolvedJSON: string(raw), MembersJSON: string(membersJSON), CreatedAt: bar0}, poolYaml); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", false, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetInstanceEnabled(ctx, "i1", true, &session, raw, bar0); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, repo: repo, loader: &fakeLoader{frames: map[int64]map[string]map[string]float64{}, prices: map[int64]map[string]float64{}}, observer: &recordingObserver{}, now: start, session: session}
	h.handler = h.newHandler()
	return &poolHarness{harness: h}
}

func poolMember(id, factor, contract string) input.PoolMember {
	resolved := input.Resolved{Market: dsl.SingleMarket("spot"), Tasks: map[string]input.TaskBinding{"spot": {ViewID: "view_factor_a", FactorTaskID: "ftask_a", DatasetID: "ds"}},
		Bar: "1h", Calendar: input.DefaultCalendar, Columns: map[string]input.ColumnBinding{"m": {Source: input.SourceFactor, Member: factor, FactorID: factor, DefinitionHash: "h"}},
		Factors: map[string]string{factor: contract}, ViewColumns: []string{"close", "m"}}
	return input.PoolMember{ID: id, StrategyID: "s_" + id, DSLHash: dsl.Hash([]byte(poolMemberDSL)), DSLYaml: poolMemberDSL, Resolved: resolved}
}

func standardMembers() []input.PoolMember {
	return []input.PoolMember{poolMember("a", "f", "h1|fixed"), poolMember("b", "g", "hg|fixed")}
}

// warm 把预热标成已完成：状态为空对象，推进到 through。
func (h *poolHarness) warm(through time.Time, state string) {
	h.t.Helper()
	if state == "" {
		state = "{}"
	}
	written, err := h.repo.SetSessionWarmed(context.Background(), h.session, state, through)
	if err != nil || !written {
		h.t.Fatalf("写入预热结果：written=%v err=%v", written, err)
	}
	h.warmedThrough = through
}

// deliverWith 投递第 n 期的就绪事件，hashes 是事件里各因子的定义哈希。
func (h *poolHarness) deliverWith(n int, hashes map[string]string, statuses ...string) error {
	message, payload := h.event(n, hashes, statuses...)
	return h.handler.Handle(context.Background(), message, payload)
}

func (h *poolHarness) health() string {
	h.t.Helper()
	instance, err := h.repo.GetInstance(context.Background(), "i1")
	if err != nil {
		h.t.Fatal(err)
	}
	return instance.Health
}

func (h *poolHarness) state(result store.Result) engine.State {
	h.t.Helper()
	var state engine.State
	if err := json.Unmarshal(result.RuleStatesJSON, &state); err != nil {
		h.t.Fatalf("规则状态无法解析：%s %v", result.RuleStatesJSON, err)
	}
	return state
}

func (h *poolHarness) summary(result store.Result) engine.Summary {
	h.t.Helper()
	var summary engine.Summary
	if err := json.Unmarshal(result.SummaryJSON, &summary); err != nil {
		h.t.Fatal(err)
	}
	return summary
}

var bothFactors = map[string]string{"f": "h1", "g": "hg"}

func abcFrame(a, b, c float64) map[string]map[string]float64 {
	return map[string]map[string]float64{"A": {"m": a}, "B": {"m": b}, "C": {"m": c}}
}

func TestPoolEvaluatesEveryMemberAndAllocates(t *testing.T) {
	account := "acct-1"
	h := newPoolHarness(t, poolDSL("{a: 0.6, b: 0.4}"), standardMembers(), &account)
	h.warm(barStart(1), "")
	h.frame(2, abcFrame(3, 2, 1))
	h.loader.prices[barStart(2).Unix()] = map[string]float64{"A|spot": 10, "B|spot": 10}
	expectAck(t, h.deliverWith(2, bothFactors))
	result := h.resultAt(2)
	if result.Status != store.StatusOK || result.PublishStatus != store.PublishPending {
		t.Fatalf("池应出 ok 且待投递：%+v", result)
	}
	// 两个成员做同样的规则（top 2 等权）：A、B 各 0.5；池目标 = (0.6 + 0.4) × 0.5。
	if got := targetsOf(t, result); got["A"] != "0.5" || got["B"] != "0.5" || len(got) != 2 {
		t.Fatalf("池目标不符：%v", got)
	}
	state := h.state(result)
	if len(state.Members) != 2 || len(state.Members["a"].Rules["r"].Held) != 2 || len(state.Pool) == 0 {
		t.Fatalf("状态要带成员状态与池状态：%s", result.RuleStatesJSON)
	}
	poolState, err := pool.DecodeState(state.Pool)
	if err != nil || !poolState.Started || len(poolState.Ledgers) != 2 || poolState.Ratios["a"] != "0.6" {
		t.Fatalf("池状态不符：%+v %v", poolState, err)
	}
	summary := h.summary(result)
	if summary.Pool == nil || len(summary.Pool.Members) != 2 || summary.Pool.Members[0].Ratio != "0.6" || summary.Pool.Members[0].Status != engine.StatusOK {
		t.Fatalf("摘要要带池明细：%s", result.SummaryJSON)
	}
	items, err := h.repo.ListResultItems(context.Background(), result.ResultID)
	if err != nil || len(items) != 6 {
		t.Fatalf("明细应是两个成员各 3 个标的：%d %v", len(items), err)
	}
	for _, item := range items {
		if item.RuleID != "a/r" && item.RuleID != "b/r" {
			t.Fatalf("明细的规则 id 要加成员前缀：%s", item.RuleID)
		}
	}
	if h.loader.priceCalls != 1 || len(h.loader.priceKeysAt[0]) != 2 {
		t.Fatalf("价格按成员目标的键读一次：%d %v", h.loader.priceCalls, h.loader.priceKeysAt)
	}
	if h.health() != store.HealthOK {
		t.Fatalf("健康状态：%s", h.health())
	}
}

// 一个成员缺因子时池照常出目标；缺因子的成员沿用上一期的目标权重，下一期恢复后重新求值。
func TestPoolMemberMissingFactorDoesNotBlockThePool(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.warm(barStart(1), "")
	h.frame(2, abcFrame(3, 2, 1))
	expectAck(t, h.deliverWith(2, bothFactors))
	first := h.resultAt(2)
	if got := targetsOf(t, first); got["A"] != "0.5" || got["B"] != "0.5" {
		t.Fatalf("首期目标：%v", got)
	}
	// 第 2 期事件里没有因子 g：成员 b 缺因子（factor_missing）没通过，沿用上期权重 {A: 0.5, B: 0.5}；
	// 成员 a 的排名变了（持有的 A、B 掉到第 4、5 名，超出缓冲区），改成 {D: 0.5, E: 0.5}。
	h.now = start.Add(time.Hour)
	h.frame(3, map[string]map[string]float64{"A": {"m": 1}, "B": {"m": 0.5}, "C": {"m": 3}, "D": {"m": 4}, "E": {"m": 5}})
	expectAck(t, h.deliverWith(3, map[string]string{"f": "h1"}))
	second := h.resultAt(3)
	if second.Status != store.StatusOK {
		t.Fatalf("池应照常出 ok：%+v", second)
	}
	if got := targetsOf(t, second); len(got) != 4 || got["A"] != "0.25" || got["B"] != "0.25" || got["D"] != "0.25" || got["E"] != "0.25" {
		t.Fatalf("没通过的成员应沿用上期权重：%v", got)
	}
	summary := h.summary(second)
	if b := summary.Pool.Members[1]; b.Status != engine.StatusSkipped || b.Reason != "factor_missing" {
		t.Fatalf("成员 b 应记没通过的原因：%+v", b)
	}
	if h.health() != store.HealthOK {
		t.Fatalf("缺因子不需要人工处理，不应标 degraded：%s", h.health())
	}
	state := h.state(second)
	if len(state.Members["b"].Rules["r"].Held) != 2 {
		t.Fatalf("没通过的成员状态原样保留：%+v", state.Members["b"])
	}
}

// 成员遇到需要人工处理的原因（因子定义变了）：池照常出 ok，但实例标 degraded，且不会被 ok 周期恢复；原因消失后才恢复。
func TestPoolMemberNeedingManualFixMarksInstanceDegraded(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.warm(barStart(1), "")
	h.frame(2, abcFrame(3, 2, 1))
	h.frame(3, abcFrame(3, 2, 1))
	h.frame(4, abcFrame(3, 2, 1))
	changed := map[string]string{"f": "h1", "g": "hg_changed"}
	expectAck(t, h.deliverWith(2, changed))
	first := h.resultAt(2)
	if first.Status != store.StatusOK {
		t.Fatalf("池应照常出 ok：%+v", first)
	}
	if got := h.summary(first).Pool.Members[1]; got.Reason != "factor_changed" {
		t.Fatalf("成员 b 的原因：%+v", got)
	}
	if h.health() != store.HealthDegraded {
		t.Fatalf("成员需要人工处理时实例应标 degraded：%s", h.health())
	}
	h.now = start.Add(time.Hour)
	expectAck(t, h.deliverWith(3, changed))
	if h.resultAt(3).Status != store.StatusOK || h.health() != store.HealthDegraded {
		t.Fatalf("同一期里有成员带这类原因就不能做 ok 周期的健康恢复：%s", h.health())
	}
	h.now = start.Add(2 * time.Hour)
	expectAck(t, h.deliverWith(4, bothFactors))
	if h.health() != store.HealthOK {
		t.Fatalf("原因消失后的 ok 周期应恢复健康：%s", h.health())
	}
}

// 所有成员都没通过时池 skipped，状态也写完整（预热完成时的状态也算）；之后正常求值时从它接着算。
func TestPoolSkippedRecordCarriesFullState(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	// 预热状态：成员 a 持有 {A: 1}，池目标 {A: 0.5}。
	warmState := engine.State{Targets: map[string]string{"A|spot": "0.5"}, Members: map[string]engine.State{"a": {Targets: map[string]string{"A|spot": "1"}}}}
	raw, _ := json.Marshal(warmState)
	h.warm(barStart(1), string(raw))
	h.frame(2, abcFrame(3, 2, 1))
	expectAck(t, h.deliverWith(2, map[string]string{"f": "h1", "g": "hg"}, "skipped"))
	skipped := h.resultAt(2)
	if skipped.Status != store.StatusSkipped || skipped.SkipReason != "factor_skipped" {
		t.Fatalf("所有成员都没通过：%+v", skipped)
	}
	if string(skipped.RuleStatesJSON) == "{}" {
		t.Fatal("池的 skipped 记录要写完整状态，不能是空对象")
	}
	if got := h.state(skipped); got.Targets["A|spot"] != "0.5" || got.Members["a"].Targets["A|spot"] != "1" {
		t.Fatalf("skipped 记录应带预热状态：%s", skipped.RuleStatesJSON)
	}
	// 摘要也写成员状态（池详情里这些期不缺）：每个成员都记没通过的原因。
	if pooled := h.summary(skipped).Pool; pooled == nil || len(pooled.Members) != 2 || pooled.Members[0].Status != engine.StatusSkipped || pooled.Members[0].Reason != "factor_skipped" || pooled.Members[1].ID != "b" {
		t.Fatalf("skipped 记录的摘要要带池的成员状态：%s", skipped.SummaryJSON)
	}
	// 下一期恢复：从 skipped 记录里的完整状态接着算（成员 a 的上期持有 A 仍在）。
	h.now = start.Add(time.Hour)
	h.frame(3, abcFrame(3, 2, 1))
	expectAck(t, h.deliverWith(3, bothFactors))
	third := h.resultAt(3)
	if third.Status != store.StatusOK || h.state(third).Members["a"].Targets["A|spot"] == "" {
		t.Fatalf("恢复后的第一期：%+v", third)
	}
	if h.health() != store.HealthOK {
		t.Fatalf("健康：%s", h.health())
	}
}

// 预热没完成时事件稍后重投（不丢期）；完成后，不晚于预热终点的事件直接去重，更晚的照常求值，预热状态是第一期的起点。
func TestPoolWaitsForWarmUpAndDedupesUpToWarmedThrough(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.frame(2, abcFrame(3, 2, 1))
	h.frame(3, abcFrame(3, 2, 1))
	expectRetry(t, h.deliverWith(2, bothFactors))
	h.noResultAt(2)
	if h.loader.calls != 0 {
		t.Fatalf("预热没完成时不读取输入：%d", h.loader.calls)
	}
	// 预热推进到第 2 期（bar_end = barStart(2)+1h）：第 2 期的事件去重，不记录；第 3 期照常求值。
	warmState := engine.State{Targets: map[string]string{"B|spot": "0.25"}, Members: map[string]engine.State{"a": {}, "b": {}}}
	raw, _ := json.Marshal(warmState)
	h.warm(barStart(2).Add(time.Hour), string(raw))
	expectAck(t, h.deliverWith(2, bothFactors))
	h.noResultAt(2)
	if h.loader.calls != 0 {
		t.Fatalf("不晚于预热终点的事件直接去重，不读取输入：%d", h.loader.calls)
	}
	h.now = start.Add(time.Hour)
	expectAck(t, h.deliverWith(3, bothFactors))
	third := h.resultAt(3)
	if third.Status != store.StatusOK {
		t.Fatalf("预热终点之后的第一期：%+v", third)
	}
	// 换手按预热状态里的池目标算：B 的上期目标是 0.25。
	if turnover := h.summary(third).Turnover; turnover == "" || turnover == "0" {
		t.Fatalf("换手应以预热状态为起点：%q", turnover)
	}
}

// 预热结果在运行时缓存之后才写入：处理器要重读会话。
func TestPoolRuntimeCachedBeforeWarmUpPicksUpTheResult(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.frame(3, abcFrame(3, 2, 1))
	expectRetry(t, h.deliverWith(3, bothFactors)) // 此时运行时已按“未预热”缓存
	h.warm(barStart(1), "")
	h.now = start.Add(time.Hour)
	expectAck(t, h.deliverWith(3, bothFactors))
	if h.resultAt(3).Status != store.StatusOK {
		t.Fatal("预热完成后应照常求值")
	}
}

// 单成员池（比例 1）与直接运行该策略的目标逐期一致，含换手、缓冲区与因子被跳过的一期。
func TestSingleMemberPoolMatchesRunningTheStrategyDirectly(t *testing.T) {
	pooled := newPoolHarness(t, `name: pool
bar: 1h
strategy_pool:
  members:
    - {id: a, strategy: s_a}
  allocation: {method: fixed, ratios: {a: 1}}
  fees: {spot_bps: 0, swap_bps: 0}
`, []input.PoolMember{poolMember("a", "f", "h1|fixed")}, nil)
	pooled.warm(barStart(1), "")
	direct := &poolHarness{harness: newHarness(t, poolMemberDSL, nil)}
	frames := []map[string]map[string]float64{
		abcFrame(3, 2, 1), abcFrame(1, 2, 3), abcFrame(2, 2.5, 3), abcFrame(3, 2, 1), abcFrame(0, 1, 5),
	}
	for i, frame := range frames {
		n := i + 2
		pooled.now, direct.now = start.Add(time.Duration(i)*time.Hour), start.Add(time.Duration(i)*time.Hour)
		pooled.frame(n, frame)
		direct.frame(n, frame)
		status := "complete"
		if n == 4 {
			status = "skipped"
		}
		expectAck(t, pooled.deliverWith(n, map[string]string{"f": "h1"}, status))
		expectAck(t, direct.deliverWith(n, map[string]string{"f": "h1"}, status))
		a, b := pooled.resultAt(n), direct.resultAt(n)
		if a.Status != b.Status || a.SkipReason != b.SkipReason {
			t.Fatalf("第 %d 期状态不一致：池 %s/%s，直接 %s/%s", n, a.Status, a.SkipReason, b.Status, b.SkipReason)
		}
		if string(a.TargetsJSON) != string(b.TargetsJSON) {
			t.Fatalf("第 %d 期目标不一致：\n池   %s\n直接 %s", n, a.TargetsJSON, b.TargetsJSON)
		}
		if a.Status == store.StatusOK {
			sa, sb := pooled.summary(a), direct.summary(b)
			if sa.Gross != sb.Gross || sa.Net != sb.Net || sa.Turnover != sb.Turnover || sa.Cash != sb.Cash {
				t.Fatalf("第 %d 期摘要不一致：池 %+v，直接 %+v", n, sa, sb)
			}
		}
	}
}

// 预热期间到达的事件不丢，也不乱序：等待期间记下的到达事件，在预热完成后按 bar 顺序补处理，即使重投的先后是倒过来的。
func TestPoolEventsArrivingDuringWarmUpAreProcessedInOrder(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.frame(2, abcFrame(3, 2, 1))
	h.frame(3, abcFrame(1, 2, 3))
	h.frame(4, abcFrame(2, 3, 1))
	for _, n := range []int{2, 3, 4} {
		expectRetry(t, h.deliverWith(n, bothFactors))
	}
	h.warm(barStart(1), "")
	h.now = start.Add(time.Hour)
	// 重投倒序：先到第 4 期，处理它之前先补处理更早的第 2、3 期。
	expectAck(t, h.deliverWith(4, bothFactors))
	for _, n := range []int{2, 3, 4} {
		if got := h.resultAt(n); got.Status != store.StatusOK {
			t.Fatalf("第 %d 期应补处理为 ok：%+v", n, got)
		}
	}
	expectAck(t, h.deliverWith(2, bothFactors))
	expectAck(t, h.deliverWith(3, bothFactors))
}

// 处理器发现池实例没有预热结果、也没人在预热它时，补启动一次预热（事件仍然重投）；预热完成后不再触发。
func TestPoolEnsuresWarmUpWhenNothingIsWarmingIt(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	var ensured []string
	h.handler.EnsureWarm = func(instanceID, sessionID string) { ensured = append(ensured, instanceID+"@"+sessionID) }
	h.frame(2, abcFrame(3, 2, 1))
	expectRetry(t, h.deliverWith(2, bothFactors))
	if len(ensured) != 1 || ensured[0] != "i1@session-1" {
		t.Fatalf("应补启动预热：%v", ensured)
	}
	h.warm(barStart(1), "")
	expectAck(t, h.deliverWith(2, bothFactors))
	if len(ensured) != 1 {
		t.Fatalf("预热完成后不再补启动：%v", ensured)
	}
}

// 追赶进度把策略池的预热终点算作已处理：预热推进过的期不会再有记录，只看结果表会让追赶永远追不平。
func TestCatchupProgressCountsPoolWarmedThrough(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	warmedThrough := barStart(3).Add(time.Hour)
	h.warm(warmedThrough, "")
	progress, err := h.handler.CatchupProgress(context.Background(), "i1", barStart(4).Add(time.Hour+time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !progress.Latest.Equal(warmedThrough) || !progress.Expected.Equal(barStart(4).Add(time.Hour)) || progress.CaughtUp() {
		t.Fatalf("没有结果时已处理到预热终点：%+v", progress)
	}
	h.now = start.Add(2 * time.Hour)
	h.frame(5, abcFrame(3, 2, 1))
	expectAck(t, h.deliverWith(5, bothFactors))
	progress, err = h.handler.CatchupProgress(context.Background(), "i1", barStart(5).Add(time.Hour+time.Minute), time.Minute)
	if err != nil || !progress.Latest.Equal(barStart(5).Add(time.Hour)) || !progress.CaughtUp() {
		t.Fatalf("有结果后取更晚的一期：%+v %v", progress, err)
	}
	// 预热终点比已处理的更晚时（预热期间没有结果）也取预热终点；预热还没完成时进度是零。
	other := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	progress, err = other.handler.CatchupProgress(context.Background(), "i1", barStart(4), time.Minute)
	if err != nil || !progress.Latest.IsZero() {
		t.Fatalf("预热没完成时没有已处理的期：%+v %v", progress, err)
	}
}

// 所有成员在就绪判断阶段都没通过：池的跳过原因优先取需要人工处理的原因（实例标 degraded），摘要里每个成员各记各的原因。
func TestPoolSkipPrefersReasonNeedingManualFix(t *testing.T) {
	h := newPoolHarness(t, poolDSL("{a: 0.5, b: 0.5}"), standardMembers(), nil)
	h.warm(barStart(1), "")
	h.frame(2, abcFrame(3, 2, 1))
	// 成员 a 的因子 f 缺失（factor_missing），成员 b 的因子 g 定义变了（factor_changed）：排在前面的 a 不是需要人工处理的原因。
	expectAck(t, h.deliverWith(2, map[string]string{"g": "hg_changed"}))
	skipped := h.resultAt(2)
	if skipped.Status != store.StatusSkipped || skipped.SkipReason != "factor_changed" {
		t.Fatalf("池的跳过原因应取需要人工处理的原因：%+v", skipped)
	}
	if h.health() != store.HealthDegraded {
		t.Fatalf("应标 degraded：%s", h.health())
	}
	members := h.summary(skipped).Pool.Members
	if members[0].Reason != "factor_missing" || members[1].Reason != "factor_changed" {
		t.Fatalf("各成员各记各的原因：%+v", members)
	}
}
