package replay

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/pool"
)

const memberRank = `rules:
  - id: r
    type: rank
    score: "mom"
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

func memberDef(t *testing.T, id, market string) input.PoolMemberDef {
	t.Helper()
	raw := "name: " + id + "\nbar: 1h\nmarket: {select: " + market + "}\n" + memberRank
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return input.PoolMemberDef{ID: id, StrategyID: "s_" + id, DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Strategy: strategy}
}

// buildPool 解析一个策略池：allocation 是 strategy_pool 里 allocation 之后的部分（缩进两格）。
func buildPool(t *testing.T, client input.Client, tasks []string, allocation string, members ...input.PoolMemberDef) (input.PoolResolved, pool.Config) {
	t.Helper()
	var list strings.Builder
	for _, member := range members {
		list.WriteString("    - {id: " + member.ID + ", strategy: " + member.StrategyID + "}\n")
	}
	raw := "name: pool\nbar: 1h\nstrategy_pool:\n  members:\n" + list.String() + allocation
	strategy, err := dsl.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	pooled, err := input.ResolvePool(context.Background(), client, "crypto", tasks, strategy, members)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := pool.NewConfig(strategy, pooled.Resolved.Market, input.MemberStrategies(pooled.Members, pooled.Programs), engine.PoolClock{})
	if err != nil {
		t.Fatal(err)
	}
	return pooled, cfg
}

func runOf(client input.Client, pooled input.PoolResolved, cfg pool.Config, startHour, endHour int) PoolRun {
	return PoolRun{Client: client, SpaceID: "crypto", Resolved: pooled.Resolved, Members: pooled.Members, Programs: pooled.Programs, Config: cfg,
		Start: origin.Add(time.Duration(startHour) * time.Hour), End: origin.Add(time.Duration(endHour) * time.Hour), ChunkBars: 4,
		Now: func() time.Time { return origin.Add(2000 * time.Hour) }}
}

func ratiosOf(decision engine.Decision) map[string]string {
	out := map[string]string{}
	if decision.Summary.Pool != nil {
		for _, member := range decision.Summary.Pool.Members {
			out[member.ID] = member.Ratio
		}
	}
	return out
}

func targetMap(decision engine.Decision) map[string]string {
	out := map[string]string{}
	for _, target := range decision.Targets {
		out[engine.TargetKey(target.InstrumentID, target.Market)] = target.Weight.String()
	}
	return out
}

// 单成员池（比例 1）每期的目标与成员净值，与研究回放直接跑该成员的结果一致。
func TestRunPoolSingleMemberMatchesResearchReplay(t *testing.T) {
	client := &fakeClient{marketType: "spot"}
	member := memberDef(t, "a", "spot")
	pooled, cfg := buildPool(t, client, []string{"ftask_a"}, "  allocation: {method: fixed, ratios: {a: 1}}\n  fees: {spot_bps: 10, swap_bps: 5}\n", member)
	var decisions []engine.Decision
	run := runOf(client, pooled, cfg, 2, 14)
	run.OnBar = func(_ input.PeriodBoundaries, decision engine.Decision) { decisions = append(decisions, decision) }
	result, err := RunPool(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if result.Bars != 12 || !result.Through.Equal(origin.Add(14*time.Hour)) {
		t.Fatalf("推进了 %d 期，终点 %v", result.Bars, result.Through)
	}
	repo := openStore(t)
	runner := &Runner{Store: repo, Client: &fakeClient{marketType: "spot"}, ChunkBars: 4, InitialEquity: 1}
	job := startReplay(t, repo, "p1", member.DSLYaml, 12)
	runner.Execute(context.Background(), job)
	replayBars := barsOf(t, repo, "p1")
	if len(replayBars) != len(decisions) {
		t.Fatalf("期数不一致：回放 %d，池 %d", len(replayBars), len(decisions))
	}
	for i, bar := range replayBars {
		var items []trigger2Target
		if err := json.Unmarshal(bar.TargetsJSON, &items); err != nil {
			t.Fatal(err)
		}
		want := map[string]string{}
		for _, item := range items {
			want[item.InstrumentID+"|"+item.Market] = item.TargetWeight
		}
		got := targetMap(decisions[i])
		if len(got) != len(want) {
			t.Fatalf("第 %d 期目标 %v，回放 %v", i, got, want)
		}
		for key, weight := range want {
			if got[key] != weight {
				t.Fatalf("第 %d 期目标 %v，回放 %v", i, got, want)
			}
		}
		equity := decisions[i].Summary.Pool.Members[0].Equity
		if math.Abs(equity-bar.Equity) > 1e-4 {
			t.Fatalf("第 %d 期成员净值 %v，回放权益 %v", i, equity, bar.Equity)
		}
	}
}

type trigger2Target struct {
	InstrumentID string `json:"instrument_id"`
	Market       string `json:"market"`
	TargetWeight string `json:"target_weight"`
}

// 启用后第一期正式目标的分配比例，与从同一起点一路推进到那一期得到的比例逐位一致：预热状态经 JSON 往返后接着推进。
func TestRunPoolWarmStateContinuesLikeOneLongRun(t *testing.T) {
	client := newDualClient()
	members := []input.PoolMemberDef{memberDef(t, "a", "spot"), memberDef(t, "b", "swap"), memberDef(t, "c", "spot")}
	allocation := "  allocation:\n    method: rotation\n    rotation: {top: 1, factors: [{name: return, window: 3}, {name: drawdown, window: 4}]}\n  timing: {window: 3}\n  step: 0.5\n"
	pooled, cfg := buildPool(t, client, []string{"ftask_a", "ftask_b"}, allocation, members...)
	long := map[int]engine.Decision{}
	run := runOf(client, pooled, cfg, 2, 40)
	run.OnBar = func(bar input.PeriodBoundaries, decision engine.Decision) {
		long[int(bar.BarEnd.Sub(origin)/time.Hour)] = decision
	}
	if _, err := RunPool(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	// 预热推进到第 30 期（bar_end = 第 30 小时），状态写成 JSON 再读回；之后第 31 期起接着推进。
	warm, err := RunPool(context.Background(), runOf(client, pooled, cfg, 2, 30))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(warm.State)
	if err != nil {
		t.Fatal(err)
	}
	var restored engine.State
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	continued := runOf(client, pooled, cfg, 30, 40)
	continued.State = restored
	hours := 0
	continued.OnBar = func(bar input.PeriodBoundaries, decision engine.Decision) {
		hour := int(bar.BarEnd.Sub(origin) / time.Hour)
		want := long[hour]
		got, wantRatios := ratiosOf(decision), ratiosOf(want)
		for id := range wantRatios {
			if got[id] != wantRatios[id] {
				t.Fatalf("第 %d 小时的比例 %v，一路推进的是 %v", hour, got, wantRatios)
			}
		}
		for i, member := range decision.Summary.Pool.Members {
			other := want.Summary.Pool.Members[i]
			if member.Equity != other.Equity || member.Base != other.Base || member.RatioBefore != other.RatioBefore || member.Timing != other.Timing {
				t.Fatalf("第 %d 小时成员 %s 的明细不逐位一致：%+v %+v", hour, member.ID, member, other)
			}
		}
		if len(targetMap(decision)) != len(targetMap(want)) {
			t.Fatalf("第 %d 小时的目标不一致", hour)
		}
		hours++
	}
	if _, err := RunPool(context.Background(), continued); err != nil {
		t.Fatal(err)
	}
	if hours != 10 {
		t.Fatalf("比较了 %d 期", hours)
	}
}

// 两个成员读同一个 View 的同一段数据：只向 Storage 读一次。
func TestRunPoolSharesReadsBetweenMembers(t *testing.T) {
	count := func(members ...input.PoolMemberDef) int {
		client := &fakeClient{marketType: "spot"}
		ratios := ""
		for _, member := range members {
			ratios += member.ID + ": 0.5, "
		}
		pooled, cfg := buildPool(t, client, []string{"ftask_a"}, "  allocation: {method: fixed, ratios: {"+strings.TrimSuffix(ratios, ", ")+"}}\n", members...)
		before := client.queries
		result, err := RunPool(context.Background(), runOf(client, pooled, cfg, 2, 14))
		if err != nil {
			t.Fatal(err)
		}
		if client.queries-before != result.Reads {
			t.Fatalf("读取次数 %d 与统计 %d 不符", client.queries-before, result.Reads)
		}
		return result.Reads
	}
	one := count(memberDef(t, "a", "spot"))
	three := count(memberDef(t, "a", "spot"), memberDef(t, "b", "spot"), memberDef(t, "c", "spot"))
	if one == 0 || three != one {
		t.Fatalf("三个成员的读取次数 %d 应与一个成员的 %d 相同", three, one)
	}
}

func TestRunPoolRejectsWindowBeforeCoverage(t *testing.T) {
	client := &fakeClient{marketType: "spot", indexedFrom: origin.Add(10 * time.Hour)}
	pooled, cfg := buildPool(t, client, []string{"ftask_a"}, "  allocation: {method: fixed, ratios: {a: 1}}\n", memberDef(t, "a", "spot"))
	_, err := RunPool(context.Background(), runOf(client, pooled, cfg, 2, 14))
	var early *input.EarlyStartError
	if !errors.As(err, &early) {
		t.Fatalf("起点早于数据覆盖范围应返回 EarlyStartError：%v", err)
	}
	if !strings.Contains(err.Error(), "成员 a") {
		t.Fatalf("错误应说明是哪个成员：%v", err)
	}
}

func TestRunPoolStopsWhenAborted(t *testing.T) {
	client := &fakeClient{marketType: "spot"}
	pooled, cfg := buildPool(t, client, []string{"ftask_a"}, "  allocation: {method: fixed, ratios: {a: 1}}\n", memberDef(t, "a", "spot"))
	run := runOf(client, pooled, cfg, 2, 14)
	stop := errors.New("会话已停用")
	bars := 0
	run.Abort = func(context.Context) error {
		if bars >= 3 {
			return stop
		}
		return nil
	}
	run.OnBar = func(input.PeriodBoundaries, engine.Decision) { bars++ }
	if _, err := RunPool(context.Background(), run); !errors.Is(err, stop) || bars != 3 {
		t.Fatalf("应在第 3 期之后停止：%v bars=%d", err, bars)
	}
}

// bigClient 生成 symbols 个标的的 1h 现货 View：close 按标的与小时确定，mom 是同期涨幅；用来测预热的耗时与读取次数。
type bigClient struct {
	fakeClient
	symbols []string
	reads   int
}

func (c *bigClient) ListDatasetSubjects(context.Context, string, string) ([]input.Subject, error) {
	subjects := make([]input.Subject, 0, len(c.symbols))
	for _, id := range c.symbols {
		subjects = append(subjects, input.Subject{SubjectID: id, Active: true})
	}
	return subjects, nil
}

func (c *bigClient) QueryRows(_ context.Context, _ string, query input.Query) ([]input.Row, uint64, error) {
	c.reads++
	var rows []input.Row
	for at := query.Start.Truncate(time.Hour); at.Before(query.End); at = at.Add(time.Hour) {
		if at.Before(query.Start) {
			continue
		}
		hour := int(at.Sub(origin) / time.Hour)
		for _, subject := range query.Subjects {
			value := bigPrice(subject.SubjectID, hour)
			rows = append(rows, input.Row{SubjectID: subject.SubjectID, DataTime: at, SeriesTag: "venue:binance", Values: map[string]float64{"close": value, "mom": value/bigPrice(subject.SubjectID, hour-1) - 1}})
		}
	}
	return rows, 1, nil
}

func bigPrice(id string, hour int) float64 {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "S"))
	return 100 + float64((n*7+hour*(n%11+1))%53) + float64(hour%7)
}

// 风险 3：10 个成员 × 预热 168 期（每期 500 个标的）的耗时与读取次数。用假客户端，没有 Storage 的网络与查询开销，
// 测出的是求值、账本与池合成的耗时；读取次数是向 Storage 发出的查询数（成员之间共享）。
func TestRunPoolTenMembersOneWeekOfHourlyBars(t *testing.T) {
	if testing.Short() {
		t.Skip("耗时测量")
	}
	symbols := make([]string, 500)
	for i := range symbols {
		symbols[i] = "S" + strconv.Itoa(i)
	}
	client := &bigClient{fakeClient: fakeClient{marketType: "spot"}, symbols: symbols}
	var members []input.PoolMemberDef
	for i := 0; i < 10; i++ {
		id := "m" + strconv.Itoa(i)
		raw := "name: " + id + "\nbar: 1h\nmarket: {select: spot}\nrules:\n  - id: r\n    type: rank\n    score: \"mom\"\n    select: {top: " + strconv.Itoa(5+i) + ", buffer: 2}\n    weight: {total: 1}\nportfolio: {max_weight: 0.3, max_missing: 1}\n"
		strategy, err := dsl.Parse([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, input.PoolMemberDef{ID: id, StrategyID: "s_" + id, DSLYaml: raw, DSLHash: dsl.Hash([]byte(raw)), Strategy: strategy})
	}
	pooled, cfg := buildPool(t, client, []string{"ftask_a"}, "  allocation:\n    method: rotation\n    rotation: {top: 3, factors: [{name: return, window: 24}, {name: ma_ratio, window: 72}]}\n  timing: {window: 24}\n  step: 0.2\n", members...)
	run := runOf(client, pooled, cfg, 200, 200+168)
	run.ChunkBars = 50
	started := time.Now()
	result, err := RunPool(context.Background(), run)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("10 个成员 × %d 期 × %d 个标的：耗时 %s（每期 %s），向 Storage 读取 %d 次，推进到 %s", result.Bars, len(symbols), elapsed.Round(time.Millisecond), (elapsed / time.Duration(result.Bars)).Round(time.Microsecond), result.Reads, result.Through.Format(time.RFC3339))
	if result.Bars != 168 {
		t.Fatalf("推进了 %d 期", result.Bars)
	}
	if elapsed > 2*time.Minute {
		t.Fatalf("预热耗时 %s 太长", elapsed)
	}
	raw, _ := json.Marshal(result.State)
	t.Logf("预热完成后的状态大小 %d 字节", len(raw))
}
