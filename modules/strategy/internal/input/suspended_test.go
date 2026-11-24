package input

import (
	"context"
	"strings"
	"testing"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/readiness"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

// 停牌持仓冻结（T5）：停牌标的不在事件名单里、也没有行，经输入层装配后由引擎按 suspended_hold 保持持仓。
// 用例都走 LoadBar + Evaluate，上期状态取上一根真实求值的结果。

// suspendedEquityDSL 是单市场股票策略：默认 max_missing（0.2）、4 个标的的小池子，holding 每根重建唯一的批次。
const suspendedEquityDSL = `name: eq
bar: 1h
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 2}
    weight: {total: 1, method: equal}
    holding: {bars: 1, offsets: [0]}
`

// suspendedBar 是一根的输入：行的 ma_20（市场 → 标的 → 值）、事件名单、就绪结论与不可用的市场。
type suspendedBar struct {
	at          time.Time
	values      map[string]map[string]float64
	events      map[string][]string
	ready       map[string]readiness.Result
	unavailable map[string]string
}

// loadAndEvaluate 装配一根并求值，previous 是上一根的状态。
func loadAndEvaluate(t *testing.T, client *fakeClient, resolved Resolved, program *dsl.Program, bar suspendedBar, previous engine.State) (Loaded, engine.Decision) {
	t.Helper()
	client.rows = func(Query) ([]Row, uint64, error) {
		var rows []Row
		for market, values := range bar.values {
			for id, value := range values {
				tag := "venue:binance"
				switch market {
				case dsl.MarketSpot:
					tag = spotTag
				case dsl.MarketSwap:
					tag = swapTag
				}
				rows = append(rows, Row{SubjectID: id, SeriesTag: tag, DataTime: bar.at, Values: map[string]float64{"ma_20": value, "close": 1}})
			}
		}
		return rows, 1, nil
	}
	loaded, err := Loader{Client: client}.LoadBar(context.Background(), "space", resolved, program, Bar{BarStart: bar.at, EventUniverse: bar.events, Readiness: bar.ready, Unavailable: bar.unavailable, Held: previous.HeldMarkets()})
	if err != nil {
		t.Fatal(err)
	}
	decision, err := engine.Evaluate(program, loaded.Frame, previous)
	if err != nil {
		t.Fatal(err)
	}
	return loaded, decision
}

// targetsOf 把目标写成 键 → 权重（保持目标加后缀 “hold”）。
func targetsOf(decision engine.Decision) map[string]string {
	out := make(map[string]string, len(decision.Targets))
	for _, target := range decision.Targets {
		value := target.Weight.String()
		if target.Hold {
			value += " hold"
		}
		out[engine.TargetKey(target.InstrumentID, target.Market)] = value
	}
	return out
}

func requireSuspendedTargets(t *testing.T, decision engine.Decision, want map[string]string) {
	t.Helper()
	if decision.Status != engine.StatusOK {
		t.Fatalf("本期应正常出目标：%s %s %v", decision.Status, decision.SkipReason, decision.Summary.Notes)
	}
	got := targetsOf(decision)
	if len(got) != len(want) {
		t.Fatalf("目标不符：期望 %v，实际 %v（%v）", want, got, decision.Summary.Notes)
	}
	for key, weight := range want {
		if got[key] != weight {
			t.Fatalf("目标 %s：期望 %s，实际 %q（全部 %v）", key, weight, got[key], got)
		}
	}
}

// itemReason 返回规则里标的的明细原因（没有明细时为空）。
func itemReason(decision engine.Decision, ruleID, id string) string {
	for _, item := range decision.Items {
		if item.RuleID == ruleID && item.InstrumentID == id {
			return item.Stage + ":" + item.Reason
		}
	}
	return ""
}

func suspendedEquityClient(t *testing.T) (*fakeClient, Resolved, *dsl.Program) {
	t.Helper()
	client := newFakeClient("equity")
	client.tagMarkets["binance_equity"] = "equity"
	client.tags["binance_equity"] = []string{"A", "B", "C", "D", "E"}
	client.trading = map[string]map[string]string{"binance_equity": {"E": TradingStatusSuspended}}
	// E 一直停牌、没持有过：不进全集。
	client.subjects["ds_factor"] = []Subject{subject("A", true), subject("B", true), subject("C", true), subject("D", true), {SubjectID: "E", SeriesTag: "venue:binance", Suspended: true}}
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, suspendedEquityDSL))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Market.Select != dsl.MarketEquity {
		t.Fatalf("应按股票策略解析：%+v", resolved.Market)
	}
	return client, resolved, program
}

// setEquitySubject 改一个标的的绑定状态。
func setEquitySubject(client *fakeClient, id string, active, suspended bool) {
	for i, item := range client.subjects["ds_factor"] {
		if item.SubjectID == id {
			client.subjects["ds_factor"][i].Active, client.subjects["ds_factor"][i].Suspended = active, suspended
		}
	}
}

// 第一根：A、B 入选，各 0.5。
func equityFirstBar(t *testing.T, client *fakeClient, resolved Resolved, program *dsl.Program) engine.State {
	t.Helper()
	_, decision := loadAndEvaluate(t, client, resolved, program, suspendedBar{
		at:     barStart,
		values: map[string]map[string]float64{"equity": {"A": 4, "B": 3, "C": 2, "D": 1}},
		events: map[string][]string{"equity": {"A", "B", "C", "D"}},
	}, engine.State{})
	requireSuspendedTargets(t, decision, map[string]string{"A|equity": "0.5", "B|equity": "0.5"})
	return decision.State
}

// 单市场：持仓 A 停牌（不在事件名单、没有行，停牌当天还在因子失败名单里）→ 按 suspended_hold 冻结，权重 0.5 不分给别人；
// 同批次照常重建（B 换成 C、D）；本期不判 too_many_missing（4 个标的冻结 1 个，计入缺数就是 0.25 > 0.2）。
// 一直停牌、没持有过的 E 不进全集。之后复牌、没被重新选中就按规则卖出。
func TestSuspendedHoldingFrozenOnEquity(t *testing.T) {
	client, resolved, program := suspendedEquityClient(t)
	state := equityFirstBar(t, client, resolved, program)

	setEquitySubject(client, "A", false, true)
	client.trading["binance_equity"]["A"] = TradingStatusSuspended
	second := suspendedBar{
		at:     barStart.Add(time.Hour),
		values: map[string]map[string]float64{"equity": {"B": 1, "C": 3, "D": 2}},
		events: map[string][]string{"equity": {"B", "C", "D"}},
		ready:  map[string]readiness.Result{"equity": {FailedByFactor: map[string][]string{"ma": {"A"}}}},
	}
	loaded, decision := loadAndEvaluate(t, client, resolved, program, second, state)
	if got := strings.Join(loaded.Frame.Universe, ","); got != "A,B,C,D" {
		t.Fatalf("停牌持仓绕过事件名单进全集，没持有的 E 不进：%s", got)
	}
	if reason := loaded.Frame.Markets["equity"].Failed["ma_20"]["A"]; reason != engine.FailedSuspended {
		t.Fatalf("停牌持仓的失败原因应覆盖成 suspended：%q", reason)
	}
	requireSuspendedTargets(t, decision, map[string]string{"A|equity": "0.5 hold", "C|equity": "0.25", "D|equity": "0.25"})
	if reason := itemReason(decision, "r", "A"); reason != engine.StageHeld+":suspended_hold" {
		t.Fatalf("A 的明细应是 suspended_hold：%s", reason)
	}
	if !strings.Contains(strings.Join(decision.Summary.Notes, "；"), "停牌") {
		t.Fatalf("摘要应说明停牌保持：%v", decision.Summary.Notes)
	}

	// 市场整体不可用的一期：停牌持仓也按停牌记，不按 market_unavailable。
	down := second
	down.unavailable = map[string]string{"equity": "因子被跳过"}
	loadedDown, _ := loadAndEvaluate(t, client, resolved, program, down, state)
	if failed := loadedDown.Frame.Markets["equity"].Failed["ma_20"]; failed["A"] != engine.FailedSuspended || failed["B"] != engine.FailedMarket {
		t.Fatalf("市场不可用时停牌持仓仍记 suspended，其余记 market_unavailable：%v", failed)
	}

	// 连续停牌：保持目标经过状态往返后仍算持有，下一根照样冻结、不整期跳过。
	third := second
	third.at, third.ready = barStart.Add(2*time.Hour), nil
	_, still := loadAndEvaluate(t, client, resolved, program, third, decision.State)
	requireSuspendedTargets(t, still, map[string]string{"A|equity": "0.5 hold", "C|equity": "0.25", "D|equity": "0.25"})
	if reason := itemReason(still, "r", "A"); reason != engine.StageHeld+":suspended_hold" {
		t.Fatalf("连续停牌时 A 的明细仍是 suspended_hold：%s", reason)
	}

	// 复牌：A 照常参与，分数最低、没被重新选中，按规则卖出。
	setEquitySubject(client, "A", true, false)
	delete(client.trading["binance_equity"], "A")
	_, resumed := loadAndEvaluate(t, client, resolved, program, suspendedBar{
		at:     barStart.Add(3 * time.Hour),
		values: map[string]map[string]float64{"equity": {"A": 0, "B": 1, "C": 3, "D": 2}},
		events: map[string][]string{"equity": {"A", "B", "C", "D"}},
	}, still.State)
	requireSuspendedTargets(t, resumed, map[string]string{"C|equity": "0.5", "D|equity": "0.5"})
}

// 停牌后被移出标签、或已下架（delisted）的持仓：Storage 不再标 suspended，照常清零，权重分给别人。
func TestSuspendedHoldingRemovedOrDelistedIsDropped(t *testing.T) {
	for name, change := range map[string]func(client *fakeClient){
		"移出标签": func(client *fakeClient) {
			client.tags["binance_equity"] = []string{"B", "C", "D", "E"}
		},
		"已下架": func(client *fakeClient) {
			client.trading["binance_equity"]["A"] = TradingStatusDelisted
		},
	} {
		client, resolved, program := suspendedEquityClient(t)
		state := equityFirstBar(t, client, resolved, program)
		setEquitySubject(client, "A", false, false)
		change(client)
		_, decision := loadAndEvaluate(t, client, resolved, program, suspendedBar{
			at:     barStart.Add(time.Hour),
			values: map[string]map[string]float64{"equity": {"B": 1, "C": 3, "D": 2}},
			events: map[string][]string{"equity": {"B", "C", "D"}},
		}, state)
		requireSuspendedTargets(t, decision, map[string]string{"C|equity": "0.5", "D|equity": "0.5"})
		if reason := itemReason(decision, "r", "A"); reason != engine.StageDropped+":not_expected" {
			t.Fatalf("%s：A 应按不在预期集合清零：%s", name, reason)
		}
	}
}

// suspendedUnifiedDSL 是统一数据集上没写规则级市场的策略：持有哪个市场就只取那个市场的行。
const suspendedUnifiedDSL = `name: unified
market: {select: both, prefer: swap}
rules:
  - id: r
    type: rank
    score: "ma_20"
    select: {top: 2}
    weight: {total: 1, method: equal}
portfolio: {max_missing: 1}
`

func unifiedSuspendedClient(t *testing.T, raw string) (*fakeClient, Resolved, *dsl.Program) {
	t.Helper()
	client := newUnifiedClient()
	client.trading = map[string]map[string]string{"binance_spot": {}, "binance_swap": {}}
	resolved, program, err := Resolve(context.Background(), client, "space", []string{"ftask_1h"}, parseStrategy(t, raw))
	if err != nil {
		t.Fatal(err)
	}
	return client, resolved, program
}

var unifiedEvents = map[string][]string{"spot": {"BTC-USDT", "ETH-USDT", "SOL-USDT"}, "swap": {"BTC-USDT", "ETH-USDT", "DOGE-USDT"}}

// 统一数据集：只持有 BTC 永续，永续停牌、现货照常 → 只冻结永续；现货 BTC 分数再高也不开仓，现货市场照常。
// 之后 BTC 永续下架（delisted）→ 照常清零。
func TestSuspendedHoldingUnifiedSwapOnly(t *testing.T) {
	client, resolved, program := unifiedSuspendedClient(t, suspendedUnifiedDSL)
	_, first := loadAndEvaluate(t, client, resolved, program, suspendedBar{
		at:     barStart,
		values: map[string]map[string]float64{"spot": {"BTC-USDT": 1, "ETH-USDT": 2, "SOL-USDT": 3}, "swap": {"BTC-USDT": 9, "ETH-USDT": 1, "DOGE-USDT": 2}},
		events: unifiedEvents,
	}, engine.State{})
	requireSuspendedTargets(t, first, map[string]string{"BTC-USDT|swap": "0.5", "SOL-USDT|spot": "0.5"})

	client.trading["binance_swap"]["BTC-USDT"] = TradingStatusSuspended
	second := suspendedBar{
		at:     barStart.Add(time.Hour),
		values: map[string]map[string]float64{"spot": {"BTC-USDT": 100, "ETH-USDT": 2, "SOL-USDT": 3}, "swap": {"ETH-USDT": 1, "DOGE-USDT": 5}},
		events: map[string][]string{"spot": {"BTC-USDT", "ETH-USDT", "SOL-USDT"}, "swap": {"ETH-USDT", "DOGE-USDT"}},
	}
	loaded, decision := loadAndEvaluate(t, client, resolved, program, second, first.State)
	if reason := loaded.Frame.Markets["swap"].Failed["ma_20"]["BTC-USDT"]; reason != engine.FailedSuspended {
		t.Fatalf("永续停牌的 BTC 应记 suspended：%q", reason)
	}
	if _, failed := loaded.Frame.Markets["spot"].Failed["ma_20"]["BTC-USDT"]; failed {
		t.Fatalf("现货不受影响：%v", loaded.Frame.Markets["spot"].Failed)
	}
	requireSuspendedTargets(t, decision, map[string]string{"BTC-USDT|swap": "0.5 hold", "DOGE-USDT|swap": "0.25", "SOL-USDT|spot": "0.25"})
	if reason := itemReason(decision, "r", "BTC-USDT"); reason != engine.StageHeld+":suspended_hold" {
		t.Fatalf("BTC 的明细应是 suspended_hold：%s", reason)
	}

	client.trading["binance_swap"]["BTC-USDT"] = TradingStatusDelisted
	_, delisted := loadAndEvaluate(t, client, resolved, program, second, decision.State)
	if _, held := targetsOf(delisted)["BTC-USDT|swap"]; held {
		t.Fatalf("下架的永续持仓应清零：%v", targetsOf(delisted))
	}
}

// 统一数据集上持有 BTC 永续空头，永续停牌：只取永续的证据冻结，不借用现货的行重新排名、把空头平掉。
// 第一根按 prefer: spot 取行，空头在永续可用的标的里按现货行的分数选中 BTC；第二根 BTC 现货分数最低，借用现货行就会被平掉。
func TestSuspendedHoldingUnifiedShort(t *testing.T) {
	raw := `name: unified
market: {select: both, prefer: spot}
rules:
  - id: l
    type: rank
    score: "ma_20"
    select: {top: 1}
    weight: 0.5
  - id: s
    type: rank
    side: short
    score: "ma_20"
    select: {top: 1}
    weight: 0.5
portfolio: {max_missing: 1}
`
	client, resolved, program := unifiedSuspendedClient(t, raw)
	_, first := loadAndEvaluate(t, client, resolved, program, suspendedBar{
		at:     barStart,
		values: map[string]map[string]float64{"spot": {"BTC-USDT": 9, "ETH-USDT": 2, "SOL-USDT": 10}, "swap": {"BTC-USDT": 1, "ETH-USDT": 1, "DOGE-USDT": 2}},
		events: unifiedEvents,
	}, engine.State{})
	weight, held := targetsOf(first)["BTC-USDT|swap"]
	if !held || !strings.HasPrefix(weight, "-") {
		t.Fatalf("第一根应在永续做空 BTC：%v", targetsOf(first))
	}

	client.trading["binance_swap"]["BTC-USDT"] = TradingStatusSuspended
	_, decision := loadAndEvaluate(t, client, resolved, program, suspendedBar{
		at:     barStart.Add(time.Hour),
		values: map[string]map[string]float64{"spot": {"BTC-USDT": 0, "ETH-USDT": 2, "SOL-USDT": 3}, "swap": {"ETH-USDT": 1, "DOGE-USDT": 5}},
		events: map[string][]string{"spot": {"BTC-USDT", "ETH-USDT", "SOL-USDT"}, "swap": {"ETH-USDT", "DOGE-USDT"}},
	}, first.State)
	if got := targetsOf(decision)["BTC-USDT|swap"]; got != weight+" hold" {
		t.Fatalf("停牌的永续空头应保持 %s：%v %v", weight, targetsOf(decision), decision.Summary.Notes)
	}
	if reason := itemReason(decision, "s", "BTC-USDT"); reason != engine.StageHeld+":suspended_hold" {
		t.Fatalf("空头规则里 BTC 的明细应是 suspended_hold：%s", reason)
	}
}

// 统一数据集上用规则级市场同时持有 BTC 现货与永续：只停一边只冻结那一边，另一边照常求值；两边都停就两边都冻结。
func TestSuspendedHoldingUnifiedPerMarket(t *testing.T) {
	raw := `name: unified
market: {select: both, prefer: swap}
rules:
  - {id: s, type: rank, market: spot, score: "ma_20", select: {top: 1}, weight: 0.5}
  - {id: w, type: rank, market: swap, score: "ma_20", select: {top: 1}, weight: 0.5}
portfolio: {max_missing: 1}
`
	first := func(t *testing.T) (*fakeClient, Resolved, *dsl.Program, engine.State) {
		client, resolved, program := unifiedSuspendedClient(t, raw)
		_, decision := loadAndEvaluate(t, client, resolved, program, suspendedBar{
			at:     barStart,
			values: map[string]map[string]float64{"spot": {"BTC-USDT": 9, "ETH-USDT": 1, "SOL-USDT": 2}, "swap": {"BTC-USDT": 9, "ETH-USDT": 1, "DOGE-USDT": 2}},
			events: unifiedEvents,
		}, engine.State{})
		requireSuspendedTargets(t, decision, map[string]string{"BTC-USDT|spot": "0.5", "BTC-USDT|swap": "0.5"})
		return client, resolved, program, decision.State
	}
	t.Run("只停永续", func(t *testing.T) {
		client, resolved, program, state := first(t)
		client.trading["binance_swap"]["BTC-USDT"] = TradingStatusSuspended
		_, decision := loadAndEvaluate(t, client, resolved, program, suspendedBar{
			at:     barStart.Add(time.Hour),
			values: map[string]map[string]float64{"spot": {"BTC-USDT": 1, "ETH-USDT": 2, "SOL-USDT": 9}, "swap": {"ETH-USDT": 1, "DOGE-USDT": 9}},
			events: map[string][]string{"spot": {"BTC-USDT", "ETH-USDT", "SOL-USDT"}, "swap": {"ETH-USDT", "DOGE-USDT"}},
		}, state)
		// 现货规则照常求值换成 SOL；永续规则停牌期间多持一只 DOGE（普通 rank 规则，计划里接受），其余目标按比例缩进剩余预算。
		requireSuspendedTargets(t, decision, map[string]string{"BTC-USDT|swap": "0.5 hold", "SOL-USDT|spot": "0.25", "DOGE-USDT|swap": "0.25"})
		if reason := itemReason(decision, "s", "BTC-USDT"); strings.HasPrefix(reason, engine.StageHeld) {
			t.Fatalf("现货规则里 BTC 不冻结：%s", reason)
		}
		if reason := itemReason(decision, "w", "BTC-USDT"); reason != engine.StageHeld+":suspended_hold" {
			t.Fatalf("永续规则里 BTC 的明细应是 suspended_hold：%s", reason)
		}
	})
	t.Run("两边都停", func(t *testing.T) {
		client, resolved, program, state := first(t)
		client.trading["binance_spot"]["BTC-USDT"] = TradingStatusSuspended
		client.trading["binance_swap"]["BTC-USDT"] = TradingStatusSuspended
		// 两边都停：Storage 的绑定不再活跃，只标 suspended。
		for i, item := range client.subjects["ds_factor"] {
			if item.SubjectID == "BTC-USDT" {
				client.subjects["ds_factor"][i].Active, client.subjects["ds_factor"][i].Suspended = false, true
			}
		}
		_, decision := loadAndEvaluate(t, client, resolved, program, suspendedBar{
			at:     barStart.Add(time.Hour),
			values: map[string]map[string]float64{"spot": {"ETH-USDT": 2, "SOL-USDT": 9}, "swap": {"ETH-USDT": 1, "DOGE-USDT": 9}},
			events: map[string][]string{"spot": {"ETH-USDT", "SOL-USDT"}, "swap": {"ETH-USDT", "DOGE-USDT"}},
		}, state)
		requireSuspendedTargets(t, decision, map[string]string{"BTC-USDT|spot": "0.5 hold", "BTC-USDT|swap": "0.5 hold"})
	})
}

// tradingMetadataStub 返回带停牌标记的绑定与带交易状态的标签成员。
type tradingMetadataStub struct {
	metadataStub
	bindings   []*storagepb.DatasetSubject
	members    []*storagepb.TagMember
	memberReqs []*storagepb.ListTagMembersReq
}

func (s *tradingMetadataStub) ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error) {
	return &storagepb.ListDatasetSubjectsRsp{RetInfo: success(), DatasetSubjects: s.bindings, PageResult: &commonpb.PageResult{}}, nil
}

func (s *tradingMetadataStub) ListTagMembers(_ context.Context, req *storagepb.ListTagMembersReq, _ ...client.Option) (*storagepb.ListTagMembersRsp, error) {
	s.memberReqs = append(s.memberReqs, req)
	return &storagepb.ListTagMembersRsp{RetInfo: success(), Members: s.members, PageResult: &commonpb.PageResult{}}, nil
}

// RPC 客户端：绑定的 suspended 映射到 Subject.Suspended（标的本身停用时不算）；标签成员带出交易状态，仍只读活跃成员。
func TestRPCClientCarriesTradingStatus(t *testing.T) {
	stub := &tradingMetadataStub{
		metadataStub: metadataStub{subjects: []*storagepb.Subject{{SubjectId: "A", Status: "active"}, {SubjectId: "B", Status: "active"}, {SubjectId: "C", Status: "inactive"}}},
		bindings: []*storagepb.DatasetSubject{
			{SubjectId: "A", Status: "active"}, {SubjectId: "B", Status: "inactive", Suspended: true}, {SubjectId: "C", Status: "inactive", Suspended: true},
		},
		members: []*storagepb.TagMember{
			{Subject: &storagepb.Subject{SubjectId: "B"}, TradingStatus: "suspended"}, {Subject: &storagepb.Subject{SubjectId: "A"}},
		},
	}
	rpc := &RPCClient{Metadata: stub}
	subjects, err := rpc.ListDatasetSubjects(context.Background(), "space", "ds")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]bool{"A": {true, false}, "B": {false, true}, "C": {false, false}}
	for _, item := range subjects {
		if got := [2]bool{item.Active, item.Suspended}; got != want[item.SubjectID] {
			t.Fatalf("%s 的活跃、停牌标记：期望 %v，实际 %v", item.SubjectID, want[item.SubjectID], got)
		}
	}
	members, err := rpc.ListTagMembers(context.Background(), "space", "tag")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0] != (TagMember{SubjectID: "A"}) || members[1] != (TagMember{SubjectID: "B", TradingStatus: TradingStatusSuspended}) {
		t.Fatalf("成员应按 ID 排序并带出交易状态：%+v", members)
	}
	if len(stub.memberReqs) != 1 || stub.memberReqs[0].GetStatus() != "active" {
		t.Fatalf("只读活跃成员：%+v", stub.memberReqs)
	}
}
