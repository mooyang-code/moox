package replay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	factorpb "github.com/mooyang-code/moox/modules/factor/proto/factorgen"
	"github.com/mooyang-code/moox/modules/strategy/internal/dsl"
	"github.com/mooyang-code/moox/modules/strategy/internal/input"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/client"
)

// fakeEngine 模拟因子引擎的 EngineControl：会话的各期是 [from, to) 的每个整点；不调参时按 fakeClient 的规则出行（与 View 来源
// 读到的行相同），调参时按 tunedValues 出行。参数规则与引擎一致：请求的参数只给这一步的第一期，没给的成员沿用上一期生效的，
// 被拒绝的参数丢弃并沿用上一期，再没有就按成员自己的参数（window 默认 20）。
type fakeEngine struct {
	data *fakeClient
	// tuned 非空时按它出行（标的、小时、生效的 window → 列值，nil 表示这一期没有这个标的的行）。
	tuned func(id string, hour int, window float64) map[string]float64
	// maxPerStep 大于 0 时单步最多返回这么多期（模拟响应超过 3 MiB 被截断）。
	maxPerStep int
	// reject 返回非空时拒绝这个 window。
	reject func(window float64) string
	// factors 是 OpenReplay 返回的成员（为空时按不调参的 mom）。
	factors []*factorpb.ReplayFactor
	// openErr、stepErrAt、lostAt：OpenReplay 转发失败；第 n 次 StepReplay 转发失败；第 n 次 StepReplay 会话不存在。
	openErr   error
	stepErrAt int
	lostAt    int

	open    *factorpb.OpenReplayReq
	periods []int64
	steps   []*factorpb.StepReplayReq
	closed  int
	// closedIDs 是 CloseReplay 收到的回放 ID；closeErr 非空时 CloseReplay 转发失败。
	closedIDs []string
	closeErr  error
	// applied 是各期生效的请求参数（期次 → window）。
	applied map[int64]float64
}

func (e *fakeEngine) OpenReplay(_ context.Context, req *factorpb.OpenReplayReq, _ ...client.Option) (*factorpb.OpenReplayRsp, error) {
	e.open = req
	if e.openErr != nil {
		return nil, e.openErr
	}
	from, _ := time.Parse(time.RFC3339, req.GetFrom())
	to, _ := time.Parse(time.RFC3339, req.GetTo())
	if !from.Equal(from.Truncate(time.Hour)) || !to.Equal(to.Truncate(time.Hour)) || !from.Before(to) {
		return &factorpb.OpenReplayRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "回放区间没有按 1h 对齐"}}, nil
	}
	e.periods = nil
	for at := from; at.Before(to); at = at.Add(time.Hour) {
		e.periods = append(e.periods, at.Unix())
	}
	e.applied = map[int64]float64{}
	factors := e.factors
	if factors == nil {
		factors = []*factorpb.ReplayFactor{{MemberKey: "mom", FactorId: "mom", Kind: "fixed", DefinitionHash: "sha256:mom", Outputs: []string{"mom"}}}
	}
	return &factorpb.OpenReplayRsp{RetInfo: &commonpb.RetInfo{}, ReplayId: req.GetReplayId(), Subjects: []string{"A", "B", "C"}, PeriodTimes: e.periods, StepSeconds: 3600, Factors: factors}, nil
}

func (e *fakeEngine) StepReplay(_ context.Context, req *factorpb.StepReplayReq, _ ...client.Option) (*factorpb.StepReplayRsp, error) {
	e.steps = append(e.steps, req)
	if len(e.steps) == e.stepErrAt {
		return nil, errors.New("dial tcp 127.0.0.1:11003: connection refused")
	}
	if len(e.steps) == e.lostAt {
		return &factorpb.StepReplayRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: "回放会话 r 不存在（引擎重启或已结束），请重新建立会话"}}, nil
	}
	start := slices.Index(e.periods, req.GetPeriodTime())
	if start < 0 || req.GetPeriods() < 1 {
		return &factorpb.StepReplayRsp{RetInfo: &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: "周期不在回放区间内"}}, nil
	}
	count := min(int(req.GetPeriods()), len(e.periods)-start)
	if e.maxPerStep > 0 {
		count = min(count, e.maxPerStep)
	}
	rsp := &factorpb.StepReplayRsp{RetInfo: &commonpb.RetInfo{}}
	for i := start; i < start+count; i++ {
		rsp.Periods = append(rsp.Periods, e.period(req, i, i == start))
	}
	return rsp, nil
}

// period 算一期：先定生效的参数，再出行。
func (e *fakeEngine) period(req *factorpb.StepReplayReq, i int, first bool) *factorpb.ReplayPeriod {
	at := e.periods[i]
	out := &factorpb.ReplayPeriod{PeriodTime: at}
	window, has := e.applied[at-3600]
	if first {
		for _, member := range req.GetParams() {
			requested := member.GetParams()["window"].GetDefaultValue()
			if reason := ""; e.reject != nil {
				if reason = e.reject(requested); reason != "" {
					out.RejectedParams = append(out.RejectedParams, &factorpb.ReplayParamRejection{MemberKey: member.GetMemberKey(), Reason: reason})
					continue
				}
			}
			window, has = requested, true
		}
	}
	effective := 20.0
	if has {
		e.applied[at] = window
		effective = window
		out.AppliedParams = []*factorpb.MemberParams{{MemberKey: "f__var", Params: map[string]*factorpb.ParamValue{"window": {DefaultValue: window}}}}
	}
	hour := int(time.Unix(at, 0).Sub(origin) / time.Hour)
	for _, id := range []string{"A", "B", "C"} {
		for _, row := range e.rows(id, hour, effective) {
			out.Rows = append(out.Rows, row)
		}
	}
	return out
}

// rows 出一个标的一期的行：只含 OpenReplay 请求的列；年龄是本地数据里这条序列截至本期的根数。
func (e *fakeEngine) rows(id string, hour int, window float64) []*factorpb.ReplayRow {
	columns := e.open.GetColumns()
	pick := func(values map[string]float64) map[string]float64 {
		out := map[string]float64{}
		for name, value := range values {
			if slices.Contains(columns, name) {
				out[name] = value
			}
		}
		return out
	}
	if e.tuned != nil {
		values := e.tuned(id, hour, window)
		if values == nil {
			return nil
		}
		row := &factorpb.ReplayRow{SubjectId: id, SeriesTag: "venue:binance", Values: pick(values), AgeBars: int32(hour + 1)}
		if slices.Contains(columns, "__params") {
			row.Params = map[string]*factorpb.ReplayParamValues{"f__var": {Values: map[string]float64{"window": window}}}
		}
		return []*factorpb.ReplayRow{row}
	}
	values := e.viewValues(id, hour)
	if values == nil {
		return nil
	}
	first := 0
	for e.data.missing[id][first] {
		first++
	}
	row := &factorpb.ReplayRow{SubjectId: id, SeriesTag: "venue:binance", Values: pick(values), AgeBars: int32(hour - first + 1)}
	if previous := e.viewValues(id, hour-1); previous != nil {
		row.Previous = pick(previous)
	}
	rows := []*factorpb.ReplayRow{row}
	if id == "A" && e.data.ambiguous[hour] {
		rows = append(rows, &factorpb.ReplayRow{SubjectId: "A", SeriesTag: "venue:other", Values: pick(map[string]float64{"close": values["close"] * 2, "mom": 0}), AgeBars: row.AgeBars})
	}
	return rows
}

// viewValues 与 fakeClient.QueryRows 的规则相同：没有行时返回 nil。
func (e *fakeEngine) viewValues(id string, hour int) map[string]float64 {
	if hour < 0 || e.data.missing[id][hour] {
		return nil
	}
	value := e.data.priceAt(id, hour)
	values := map[string]float64{"close": value, "mom": value - e.data.priceAt(id, hour-1)}
	if e.data.nullMom[id][hour] {
		delete(values, "mom")
	}
	return values
}

func (e *fakeEngine) CloseReplay(_ context.Context, req *factorpb.CloseReplayReq, _ ...client.Option) (*factorpb.CloseReplayRsp, error) {
	e.closed++
	e.closedIDs = append(e.closedIDs, req.GetReplayId())
	if e.closeErr != nil {
		return nil, e.closeErr
	}
	return &factorpb.CloseReplayRsp{RetInfo: &commonpb.RetInfo{}, Closed: true}, nil
}

// startComputeReplay 登记并认领一个“因子现算”回放：与 startReplay 相同，因子指纹按 client 解析。
func startComputeReplay(t *testing.T, repo *store.Store, client input.Client, id, dslYaml string, hours int) store.Replay {
	t.Helper()
	strategy, err := dsl.Parse([]byte(dslYaml))
	require.NoError(t, err)
	resolved, _, err := input.Resolve(context.Background(), client, "crypto", []string{"ftask_a"}, strategy)
	require.NoError(t, err)
	job := store.Replay{ReplayID: id, DSLYaml: dslYaml, DSLHash: dsl.Hash([]byte(dslYaml)), Calendar: "crypto_24x7", SpaceID: "crypto", Input: store.ReplayInputCompute,
		Tasks: map[string]store.ReplayTask{"spot": {FactorTaskID: "ftask_a", ViewID: "view_factor_a", ViewGeneration: "idx"}}, StartTime: origin.Add(2 * time.Hour), EndTime: origin.Add(time.Duration(2+hours) * time.Hour),
		SpotFeeBps: 10, Factors: resolved.Factors, CreatedAt: origin}
	require.NoError(t, repo.CreateReplay(context.Background(), job))
	claimed, found, err := repo.ClaimNextReplay(context.Background(), origin)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, store.ReplayInputCompute, claimed.Input, "认领时要读到输入来源")
	return claimed
}

// 引擎给出与 View 相同的行时，“因子现算”与 View 来源的逐期记录（目标、账本、摘要、收益）完全一致：覆盖缺价清算、现金缓冲、
// 年龄判定（引擎的根数与 View 的探针结论相同）、bars[-1] 与歧义；每步只返回 2 期（截断）不影响结果。
// 这只证明数据通路（行的整理、年龄、上一根、分段与截断）：不覆盖标的全集的差别——引擎只取源数据集当前在册、本地数据覆盖到
// 最后一期的标的，截面因子的全集可能比 View 当时的小，这里的假引擎直接给出 View 的行，不模拟它。
func TestComputeSourceMatchesViewSource(t *testing.T) {
	for _, scenario := range goldenScenarios() {
		for _, truncate := range []int{0, 2} {
			t.Run(fmt.Sprintf("%s/截断%d", scenario.name, truncate), func(t *testing.T) {
				viewRepo := openStore(t)
				viewJob := startReplay(t, viewRepo, "v1", scenario.dsl, scenario.hours)
				(&Runner{Store: viewRepo, Client: scenario.client(), ChunkBars: scenario.chunk, MissingPriceLiquidateBars: scenario.liq, InitialEquity: 100}).Execute(context.Background(), viewJob)

				computeRepo := openStore(t)
				client := scenario.client()
				engine := &fakeEngine{data: client, maxPerStep: truncate}
				job := startComputeReplay(t, computeRepo, client, "c1", scenario.dsl, scenario.hours)
				(&Runner{Store: computeRepo, Client: client, Engine: engine, ChunkBars: scenario.chunk, MissingPriceLiquidateBars: scenario.liq, InitialEquity: 100}).Execute(context.Background(), job)
				done, err := computeRepo.GetReplay(context.Background(), "c1")
				require.NoError(t, err)
				require.Equal(t, store.ReplayDone, done.Status, done.Error)
				require.Equal(t, 1, engine.closed, "结束后关闭引擎上的会话")

				want, got := barsOf(t, viewRepo, "v1"), barsOf(t, computeRepo, "c1")
				require.Len(t, got, len(want))
				for i := range want {
					require.Equal(t, string(want[i].TargetsJSON), string(got[i].TargetsJSON), "第 %d 期目标", i)
					require.Equal(t, string(want[i].PositionsJSON), string(got[i].PositionsJSON), "第 %d 期账本", i)
					require.Equal(t, string(want[i].SummaryJSON), string(got[i].SummaryJSON), "第 %d 期摘要", i)
					require.Equal(t, want[i].Equity, got[i].Equity, "第 %d 期权益", i)
					require.Equal(t, "{}", string(got[i].ParamsJSON), "不调参时没有逐期提交的参数")
				}
				var metrics Metrics
				require.NoError(t, json.Unmarshal(done.MetricsJSON, &metrics))
				require.Contains(t, strings.Join(metrics.Limitations, "\n"), "因子现算")
				require.Contains(t, strings.Join(metrics.Limitations, "\n"), "数据集登记 3 个标的，参与 3 个", "局限说明写明登记数与参与数")

				// 按 OpenReplay 给出的各期推进：请求的期次连续，从不越过区间末尾，截断时从下一期接着请求。
				require.Equal(t, boolInt(scenario.dsl == previousDSL), engine.open.GetExtraPeriods(), "只有用到 bars[-1] 才多预热一根")
				next := 0
				for _, step := range engine.steps {
					require.Equal(t, engine.periods[next], step.GetPeriodTime())
					require.LessOrEqual(t, next+int(step.GetPeriods()), len(engine.periods))
					got := int(step.GetPeriods())
					if truncate > 0 {
						got = min(got, truncate)
					}
					next += got
				}
				require.Equal(t, len(engine.periods), next)
			})
		}
	}
}

func boolInt(value bool) int32 {
	if value {
		return 1
	}
	return 0
}

// tunedComputeDSL：可变成员 f__var 的输出 m 选 1 个；下一期的 window 取固定成员 p 的中位数；filter 用 params() 读本期生效的 window。
const tunedComputeDSL = `name: tuned_compute
factor_params:
  f__var:
    window:
      default: "p"
rules:
  - id: r
    type: rank
    score: "m"
    filter: 'params("f__var", "window") > 0'
    select: {top: 1}
    weight: {total: 1}
portfolio:
  max_missing: 1
`

// tunedComputeClient 是调参任务的结果 View：close、可变成员 f__var 的输出 m、固定成员 p 的输出 p 与系统列 __params；
// f 声明了整型参数 window（5～60，默认 20）。
type tunedComputeClient struct{ *fakeClient }

func (c tunedComputeClient) GetView(ctx context.Context, spaceID, viewID string) (input.ViewInfo, error) {
	view, err := c.fakeClient.GetView(ctx, spaceID, viewID)
	view.Columns = []input.ViewColumn{
		{Name: "close", Attributes: map[string]string{}},
		{Name: "m", Attributes: map[string]string{"column_kind": "output", "member_key": "f__var", "factor_id": "f", "member_kind": "var", "output_name": "m"}},
		{Name: "p", Attributes: map[string]string{"column_kind": "output", "member_key": "p", "factor_id": "p", "member_kind": "fixed", "output_name": "p"}},
		{Name: "__params", Attributes: map[string]string{"column_kind": "params"}},
	}
	return view, err
}

func (tunedComputeClient) GetFactor(_ context.Context, factorID string) (input.FactorInfo, error) {
	info := input.FactorInfo{FactorID: factorID, DefinitionHash: "h-" + factorID}
	if factorID == "f" {
		info.Params = []input.ParamDecl{{Name: "window", Type: "int", Min: 5, Max: 60, Default: 20}}
	}
	return info, nil
}

var tunedFactors = []*factorpb.ReplayFactor{
	{MemberKey: "f__var", FactorId: "f", Kind: "var", DefinitionHash: "h-f", Outputs: []string{"m"}},
	{MemberKey: "p", FactorId: "p", Kind: "fixed", DefinitionHash: "h-p", Outputs: []string{"p"}},
}

// tunedSchedule 按第几期（回放从第 2 小时起）给 p：缺省的期次没有 p（本期 ok 但求不出参数）；skip 里的期次没有任何行（本期跳过）。
// m：A 是生效的 window，B 恒为 25，C 恒为 1，所以 window 超过 25 时选 A，否则选 B。
func tunedSchedule(p map[int]float64, skip map[int]bool) func(id string, hour int, window float64) map[string]float64 {
	return func(id string, hour int, window float64) map[string]float64 {
		i := hour - 2
		if skip[i] {
			return nil
		}
		values := map[string]float64{"close": price(id, hour)}
		switch id {
		case "A":
			values["m"] = window
		case "B":
			values["m"] = 25
		default:
			values["m"] = 1
		}
		if value, ok := p[i]; ok {
			values["p"] = value
		}
		return values
	}
}

// runTuned 跑一次调参策略的“因子现算”回放，返回逐期记录与引擎。
func runTuned(t *testing.T, hours int, p map[int]float64, skip map[int]bool, reject func(float64) string) ([]store.ReplayBar, *fakeEngine) {
	t.Helper()
	repo := openStore(t)
	client := tunedComputeClient{&fakeClient{marketType: "spot"}}
	engine := &fakeEngine{data: client.fakeClient, tuned: tunedSchedule(p, skip), reject: reject, factors: tunedFactors}
	job := startComputeReplay(t, repo, client, "t1", tunedComputeDSL, hours)
	(&Runner{Store: repo, Client: client, Engine: engine, ChunkBars: 50, InitialEquity: 100}).Execute(context.Background(), job)
	done, err := repo.GetReplay(context.Background(), "t1")
	require.NoError(t, err)
	require.Equal(t, store.ReplayDone, done.Status, done.Error)
	bars := barsOf(t, repo, "t1")
	require.Len(t, bars, hours)
	require.Len(t, engine.steps, hours, "声明了 factor_params 时每步一期")
	require.Contains(t, engine.open.GetColumns(), "__params", "用到 params() 时请求 __params")
	return bars, engine
}

// requested 返回第 i 步请求里 f__var 的 window（没带参数时为 0）。
func requested(engine *fakeEngine, i int) float64 {
	for _, member := range engine.steps[i].GetParams() {
		if member.GetMemberKey() == "f__var" {
			return member.GetParams()["window"].GetDefaultValue()
		}
	}
	return 0
}

// tunedPick 返回一期选中的标的（跳过的期为空串）。
func tunedPick(t *testing.T, bar store.ReplayBar) string {
	t.Helper()
	targets := decodeTargets(t, bar)
	require.LessOrEqual(t, len(targets), 1)
	for key := range targets {
		return strings.TrimSuffix(key, "|spot")
	}
	return ""
}

// paramsWindow 返回一期记录里生效的 window（没有逐期提交的参数时为 0）。
func paramsWindow(t *testing.T, bar store.ReplayBar) float64 {
	t.Helper()
	var params map[string]map[string]input.ParamValue
	require.NoError(t, json.Unmarshal(bar.ParamsJSON, &params))
	return params["f__var"]["window"].Default
}

// 照搬闭环回放的参数沿用规则（seriesreplay TestClosedLoopCarriesParamsIntoNextStep）：每期产出下一期的参数，下一期的因子步进带着它；
// 没有产出的期次（跳过、或本期求不出参数）下一步不带参数，引擎沿用上一期生效的；第一期没有上一期，参数交给引擎（成员自己的参数）。
// 逐期比较请求、记录里的生效参数与目标。
func TestComputeTunedCarriesParamsIntoNextStep(t *testing.T) {
	// 第 0 期产出 10，第 1、3 期跳过，第 2 期产出 30，第 4 期 ok 但求不出参数，第 5 期产出 40（之后没有下一期）。
	bars, engine := runTuned(t, 6, map[int]float64{0: 10, 2: 30, 5: 40}, map[int]bool{1: true, 3: true}, nil)
	wantRequested := []float64{0, 10, 0, 30, 0, 0}
	wantApplied := []float64{0, 10, 10, 30, 30, 30}
	// 生效 window：默认 20、10 时选 B，30 时选 A；跳过的期没有目标。
	wantPick := []string{"B", "", "B", "", "A", "A"}
	for i := range bars {
		require.Equal(t, wantRequested[i], requested(engine, i), "第 %d 步请求的参数", i)
		require.Equal(t, wantApplied[i], paramsWindow(t, bars[i]), "第 %d 期记录的生效参数", i)
		require.Equal(t, wantPick[i], tunedPick(t, bars[i]), "第 %d 期的目标", i)
	}
	require.Equal(t, "{}", string(bars[0].ParamsJSON), "第一期按成员自己的参数，记录里没有逐期提交的参数")
}

// 照搬闭环回放的拒绝规则（seriesreplay TestClosedLoopDropsParamsRejectedByFactorEngine）：引擎拒绝的参数不留在记录里，
// 这一期沿用上一期生效的（第一期之后还没有，按成员自己的参数）；之后产出合法的参数照常生效。
func TestComputeTunedDropsParamsRejectedByEngine(t *testing.T) {
	reject := func(window float64) string {
		if window == 45 {
			return "参数 window 不满足联合约束"
		}
		return ""
	}
	bars, engine := runTuned(t, 4, map[int]float64{0: 45, 1: 30}, nil, reject)
	require.Equal(t, 45.0, requested(engine, 1))
	require.Equal(t, "{}", string(bars[1].ParamsJSON), "被拒绝的参数不留在记录里")
	require.Equal(t, "B", tunedPick(t, bars[1]), "被拒绝后按成员自己的参数（20）")
	require.Equal(t, 30.0, requested(engine, 2))
	var summary struct {
		Decision struct {
			Notes []string `json:"notes"`
		} `json:"decision"`
	}
	require.NoError(t, json.Unmarshal(bars[1].SummaryJSON, &summary))
	require.Contains(t, strings.Join(summary.Decision.Notes, "\n"), "因子引擎拒绝了成员 f__var 这一期的参数（参数 window 不满足联合约束）", "拒绝原因写进这一期的说明")
	for i, want := range []float64{0, 0, 30, 30} {
		require.Equal(t, want, paramsWindow(t, bars[i]), "第 %d 期记录的生效参数", i)
	}
	require.Equal(t, []string{"B", "B", "A", "A"}, []string{tunedPick(t, bars[0]), tunedPick(t, bars[1]), tunedPick(t, bars[2]), tunedPick(t, bars[3])})
}

// 照搬 trigger TestReplayTunedStrategyEmitsNextParams：ok 的期输出下一期的参数（整型四舍五入、限制在声明的范围内），
// params() 读本期生效的参数；跳过的期不输出。
func TestComputeTunedEmitsFittedParams(t *testing.T) {
	bars, engine := runTuned(t, 4, map[int]float64{0: 12.6, 1: 100, 2: 3}, map[int]bool{3: true}, nil)
	require.Equal(t, 13.0, requested(engine, 1), "整型参数四舍五入")
	require.Equal(t, 60.0, requested(engine, 2), "超出上限按上限")
	require.Equal(t, 5.0, requested(engine, 3), "低于下限按下限")
	require.Equal(t, []string{"B", "B", "A", ""}, []string{tunedPick(t, bars[0]), tunedPick(t, bars[1]), tunedPick(t, bars[2]), tunedPick(t, bars[3])})
	var summary struct {
		Decision struct {
			Notes []string `json:"notes"`
		} `json:"decision"`
	}
	require.NoError(t, json.Unmarshal(bars[1].SummaryJSON, &summary))
	require.Contains(t, strings.Join(summary.Decision.Notes, "\n"), "已限制在范围内", "限幅的说明写进这一期的摘要")
}

// 调参策略“因子现算”回放的金样：逐期记录（含 params_json）与最终绩效。
func TestComputeTunedBaseline(t *testing.T) {
	bars, _ := runTuned(t, 6, map[int]float64{0: 10, 2: 30, 5: 40}, map[int]bool{1: true, 3: true}, nil)
	records := baselineBarRecords(t, bars)
	for i, bar := range bars {
		records[i].(map[string]any)["params"] = baselineDecode(t, bar.ParamsJSON)
	}
	baselineCompare(t, filepath.Join("testdata", "baseline", "compute_tuned.json"), baselineJSON(t, map[string]any{"dsl": tunedComputeDSL, "bars": records}))
}

// 会话失效与引擎不在线：回放失败并说明原因，不静默重试；已经建立的会话在结束时关闭。
func TestComputeSourceEngineFailures(t *testing.T) {
	run := func(t *testing.T, engine *fakeEngine) store.Replay {
		t.Helper()
		repo := openStore(t)
		client := &fakeClient{marketType: "spot"}
		engine.data = client
		job := startComputeReplay(t, repo, client, "f1", replayDSL, 6)
		(&Runner{Store: repo, Client: client, Engine: engine, ChunkBars: 2, InitialEquity: 100}).Execute(context.Background(), job)
		done, err := repo.GetReplay(context.Background(), "f1")
		require.NoError(t, err)
		require.Equal(t, store.ReplayFailed, done.Status)
		return done
	}
	t.Run("引擎重启", func(t *testing.T) {
		engine := &fakeEngine{lostAt: 2}
		done := run(t, engine)
		require.Equal(t, "因子引擎重启，回放会话已失效，请重新发起", done.Error)
		require.Len(t, engine.steps, 2, "会话失效不重试")
		require.Equal(t, 1, engine.closed)
	})
	t.Run("步进转发失败", func(t *testing.T) {
		engine := &fakeEngine{stepErrAt: 1}
		done := run(t, engine)
		require.Contains(t, done.Error, "因子引擎不在线")
		require.NotContains(t, done.Error, "127.0.0.1", "原始错误只进日志")
		require.Len(t, engine.steps, 1)
	})
	t.Run("建会话转发失败", func(t *testing.T) {
		engine := &fakeEngine{openErr: errors.New("gateway forward failed"), closeErr: errors.New("gateway forward failed")}
		done := run(t, engine)
		require.Contains(t, done.Error, "因子引擎不在线")
		require.Equal(t, []string{"f1"}, engine.closedIDs, "结果未知：引擎可能已登记会话，尽力关一次（关不上只记日志）")
	})
	t.Run("引擎的成员定义与发起时不一致", func(t *testing.T) {
		engine := &fakeEngine{factors: []*factorpb.ReplayFactor{{MemberKey: "mom", FactorId: "mom", Kind: "fixed", DefinitionHash: "sha256:other"}}}
		done := run(t, engine)
		require.Contains(t, done.Error, "与发起回放时")
		require.Empty(t, engine.steps)
		require.Equal(t, 1, engine.closed)
	})
	t.Run("没有配置引擎客户端", func(t *testing.T) {
		repo := openStore(t)
		client := &fakeClient{marketType: "spot"}
		job := startComputeReplay(t, repo, client, "f1", replayDSL, 3)
		(&Runner{Store: repo, Client: client}).Execute(context.Background(), job)
		done, err := repo.GetReplay(context.Background(), "f1")
		require.NoError(t, err)
		require.Equal(t, store.ReplayFailed, done.Status)
		require.Contains(t, done.Error, "因子引擎客户端未配置")
	})
}

// 每步期数：按“序列数 × 列数 × 48 字节”控制在 3 MiB 以内、不超过分段根数；调参固定 1 期；一期就超过上限时拒绝。
func TestComputeStepPeriods(t *testing.T) {
	for _, tc := range []struct {
		series, columns, limit int
		tuned                  bool
		want                   int
	}{
		{500, 10, 1000, false, 13},
		{3, 3, 50, false, 50},
		{1000, 10, 50, false, 6},
		{3, 3, 50, true, 1},
		{6000, 10, 50, false, 1},
	} {
		got, err := ComputeStepPeriods(tc.series, tc.columns, tc.tuned, tc.limit)
		require.NoError(t, err)
		require.Equal(t, tc.want, got, "%+v", tc)
	}
	_, err := ComputeStepPeriods(5000, 40, false, 50)
	require.ErrorContains(t, err, "减少策略引用的列")
}

// viewEngine 是按 View 已有的行出帧的因子引擎：每期按 OpenReplay 请求的列读这个 View 同一根的行（series_tag 原样，统一数据集上
// 现货、永续两条序列各一行），上一期的同名列读上一根，年龄是（标的, series_tag）在 View 里最早一行起的根数。引擎与 View 给出相同的
// 行时，因子现算与 View 来源的回放结果应当一致。
type viewEngine struct {
	client  input.Client
	viewID  string
	columns []string
	periods []int64
	first   map[string]int64
	steps   int
	closed  int
}

func (e *viewEngine) query(at time.Time, columns []string) []input.Row {
	subjects, _ := e.client.ListDatasetSubjects(context.Background(), "crypto", "")
	view, _ := e.client.GetView(context.Background(), "crypto", e.viewID)
	rows, _, _ := e.client.QueryRows(context.Background(), "crypto", input.Query{ViewID: e.viewID, DatasetID: view.DatasetID, Frequency: "1h", Subjects: subjects, Start: at, End: at.Add(time.Nanosecond), Columns: columns})
	return rows
}

func (e *viewEngine) OpenReplay(_ context.Context, req *factorpb.OpenReplayReq, _ ...client.Option) (*factorpb.OpenReplayRsp, error) {
	from, _ := time.Parse(time.RFC3339, req.GetFrom())
	to, _ := time.Parse(time.RFC3339, req.GetTo())
	e.columns, e.periods, e.first = req.GetColumns(), nil, map[string]int64{}
	for at := from; at.Before(to); at = at.Add(time.Hour) {
		e.periods = append(e.periods, at.Unix())
	}
	for at := origin; at.Before(to); at = at.Add(time.Hour) {
		for _, row := range e.query(at, []string{"close"}) {
			if _, ok := e.first[row.SubjectID+"|"+row.SeriesTag]; !ok {
				e.first[row.SubjectID+"|"+row.SeriesTag] = at.Unix()
			}
		}
	}
	subjects, _ := e.client.ListDatasetSubjects(context.Background(), "crypto", "")
	ids := make([]string, 0, len(subjects))
	for _, subject := range subjects {
		ids = append(ids, subject.SubjectID)
	}
	return &factorpb.OpenReplayRsp{RetInfo: &commonpb.RetInfo{}, ReplayId: req.GetReplayId(), Subjects: ids, PeriodTimes: e.periods, StepSeconds: 3600}, nil
}

func (e *viewEngine) StepReplay(_ context.Context, req *factorpb.StepReplayReq, _ ...client.Option) (*factorpb.StepReplayRsp, error) {
	e.steps++
	start := slices.Index(e.periods, req.GetPeriodTime())
	rsp := &factorpb.StepReplayRsp{RetInfo: &commonpb.RetInfo{}}
	for i := start; i < min(start+int(req.GetPeriods()), len(e.periods)); i++ {
		at := time.Unix(e.periods[i], 0).UTC()
		previous := map[string]map[string]float64{}
		for _, row := range e.query(at.Add(-time.Hour), e.columns) {
			previous[row.SubjectID+"|"+row.SeriesTag] = row.Values
		}
		period := &factorpb.ReplayPeriod{PeriodTime: at.Unix()}
		for _, row := range e.query(at, e.columns) {
			key := row.SubjectID + "|" + row.SeriesTag
			period.Rows = append(period.Rows, &factorpb.ReplayRow{SubjectId: row.SubjectID, SeriesTag: row.SeriesTag, Values: row.Values, Previous: previous[key],
				AgeBars: int32((at.Unix()-e.first[key])/3600 + 1)})
		}
		rsp.Periods = append(rsp.Periods, period)
	}
	return rsp, nil
}

func (e *viewEngine) CloseReplay(context.Context, *factorpb.CloseReplayReq, ...client.Option) (*factorpb.CloseReplayRsp, error) {
	e.closed++
	return &factorpb.CloseReplayRsp{RetInfo: &commonpb.RetInfo{}, Closed: true}, nil
}

// 永续（标签黑白名单、显式 pool、新上市的年龄剔除、缺价冻结、逐期结算）、统一数据集（一个任务按 series_tag 分到现货、永续，年龄按市场判断）
// 与永续计资金费（引擎多返回资金费列）三个场景上，因子现算与 View 来源的逐期记录完全一致。
func TestComputeSourceMatchesViewSourceOnMarkets(t *testing.T) {
	type scenario struct {
		name    string
		dsl     string
		client  func() input.Client
		task    store.ReplayTask
		hours   int
		funding string
	}
	funded := func() input.Client {
		client := newDualClient()
		client.swap.fundingRate = 0.001
		return client
	}
	for _, tc := range []scenario{
		{name: "swap_universe_pool_rank", dsl: baselineSwapDSL, client: func() input.Client { return newBaselineClient(baselineSwapClient()) }, task: swapTask, hours: 16},
		{name: "dual_unified", dsl: baselineDualDSL, client: func() input.Client { return newBaselineClient(&unifiedClient{dualClient: baselineDualClient()}) }, task: uniTask, hours: 16},
		{name: "swap_funding", dsl: swapOnlyDSL, client: funded, task: swapTask, hours: 10, funding: "funding_rate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			market := "swap"
			if tc.task == uniTask {
				market = "multi"
			}
			tasks := map[string]store.ReplayTask{market: tc.task}
			viewRepo := openStore(t)
			viewClient := tc.client()
			viewJob := startMarketReplay(t, viewRepo, viewClient, "v1", tc.dsl, tc.hours, tasks, 10, 5)
			viewJob.FundingColumn = tc.funding
			(&Runner{Store: viewRepo, Client: viewClient, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), viewJob)

			computeRepo := openStore(t)
			client := tc.client()
			engine := &viewEngine{client: client, viewID: tc.task.ViewID}
			job := startMarketReplay(t, computeRepo, client, "c1", tc.dsl, tc.hours, tasks, 10, 5)
			job.Input, job.FundingColumn = store.ReplayInputCompute, tc.funding
			(&Runner{Store: computeRepo, Client: client, Engine: engine, ChunkBars: 5, MissingPriceLiquidateBars: 3, InitialEquity: 100}).Execute(context.Background(), job)
			done, err := computeRepo.GetReplay(context.Background(), "c1")
			require.NoError(t, err)
			require.Equal(t, store.ReplayDone, done.Status, done.Error)
			require.Equal(t, 1, engine.closed)
			if tc.funding != "" {
				require.Contains(t, engine.columns, tc.funding, "计资金费时请求资金费列")
			}

			want, got := barsOf(t, viewRepo, "v1"), barsOf(t, computeRepo, "c1")
			require.Len(t, want, tc.hours)
			require.Len(t, got, len(want))
			for i := range want {
				require.Equal(t, string(want[i].TargetsJSON), string(got[i].TargetsJSON), "第 %d 期目标", i)
				require.Equal(t, string(want[i].PositionsJSON), string(got[i].PositionsJSON), "第 %d 期账本", i)
				require.Equal(t, string(want[i].SummaryJSON), string(got[i].SummaryJSON), "第 %d 期摘要", i)
				require.Equal(t, want[i].Equity, got[i].Equity, "第 %d 期权益", i)
				require.Equal(t, want[i].SwapValue, got[i].SwapValue, "第 %d 期永续名义价值", i)
			}
		})
	}
}

// 进程重启：先逐个关掉“因子现算”运行中回放留在因子引擎上的会话（View 来源的不用关，关不上只记日志），再标成 interrupted。
func TestCloseInterruptedSessionsBeforeMarkingInterrupted(t *testing.T) {
	repo := openStore(t)
	ctx := context.Background()
	for i, item := range []struct{ id, input string }{{"c1", store.ReplayInputCompute}, {"v1", store.ReplayInputView}, {"c2", store.ReplayInputCompute}, {"c3", store.ReplayInputCompute}} {
		require.NoError(t, repo.CreateReplay(ctx, store.Replay{ReplayID: item.id, DSLYaml: replayDSL, DSLHash: dsl.Hash([]byte(replayDSL)), Calendar: "crypto_24x7", SpaceID: "crypto", Input: item.input,
			Tasks: map[string]store.ReplayTask{"spot": spotTask}, StartTime: origin, EndTime: origin.Add(time.Hour), CreatedAt: origin.Add(time.Duration(i) * time.Second)}))
	}
	// 按登记先后认领：c1、v1、c2 运行中，c3 还在排队。
	for range 3 {
		_, found, err := repo.ClaimNextReplay(ctx, origin)
		require.NoError(t, err)
		require.True(t, found)
	}
	engine := &fakeEngine{closeErr: errors.New("connection refused")}
	var logs []string
	require.NoError(t, CloseInterruptedSessions(ctx, repo, engine, func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }))
	require.Equal(t, []string{"c1", "c2"}, engine.closedIDs, "只关运行中的因子现算回放，逐个关闭")
	require.Len(t, logs, 2, "关不上只记日志")
	interrupted, err := repo.MarkRunningReplaysInterrupted(ctx, origin)
	require.NoError(t, err)
	require.Equal(t, int64(3), interrupted)
}
