package engine

import (
	"bytes"
	"context"
	"encoding/csv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

// 回放因子帧金样（批 3 T11 的前置 R2）：把现有回放夹具逐期得到的因子值、上一期因子值、年龄根数、成员状态与生效参数存成数据文件。
// T11 会把回放改成 RPC 并重写帧的组装（按（标的, series_tag）出行、按列裁剪、一步多期），之后对着这份数据文件比，
// 单序列数据集上要逐位一致。
//
// 结构：每个值一行，键是（场景、步序、期、记录类型、标的、series_tag、成员、名字），不依赖现有响应结构的字段名与嵌套方式，
// 字段换了名字或位置时按键对应比较即可。步序是会话里第几次请求：同一期可以带不同参数重复请求，所以期本身不够当键。
// 记录类型：
//   - factor、factor_prev：本期、上一期的因子值，成员是成员键，名字是输出列；
//   - age_bars：截至本期的根数（含本期）；
//   - param：本期这个标的各成员实际用的参数（与结果行的 __params 一致），名字是参数名；
//   - state.status、state.kind、state.factor_id、state.definition_hash、state.failed_subjects：成员本期的状态，按期给，
//     标的与 series_tag 为空；
//   - applied.default、applied.override：本期由请求带来（或沿用上一期请求）的生效参数，override 的标的放在标的一栏；
//   - rejected：本期被拒绝的成员；拒绝原因是报错文字，T11 可能改写措辞，不进金样。
//
// 没有某一行表示帧里没有这个值：现在的帧不放算不出的值（没有 NaN 条目），例如 late_listing 第 8 分钟 LATE 有 age_bars
// 而没有 factor，observed 第 5 分钟没有 factor_prev。T11 之后也要这样省略，否则要有意重新生成金样并说明原因。
// 未上市或被排除的标的整行都不出现。
//
// 文件里的记录按（场景、步序（数值）、记录类型、标的、series_tag、成员、名字）排序后写出，与生成记录的先后无关：
// 只要给出同一组值，写出的字节就相同。
//
// 现有夹具全是单序列：每个标的只有一条 series_tag 为 plainTag 的序列，现在的响应里不带 series_tag，金样按 plainTag 照实填。
// 多序列（统一数据集两条序列都出行）由 T11 新增的用例覆盖，不在这份金样里。
// 不进金样的：Close、Exec（T11 删除成交价字段）、Source（T11 改成按请求的列裁剪）、DecisionAt、ComputeMillis 与报错文字。
//
// 浮点按 strconv.FormatFloat(v, 'g', -1, 64) 写，能精确还原，按文字比较就是位级比较。
// 金样由当前代码生成，默认只比对；有意改变结果时用 UPDATE_BASELINE=1 重新生成，并在 PR 里说明原因：
//
//	UPDATE_BASELINE=1 go test -count=1 -run TestReplayBaselineFrames ./internal/engine/

const replayBaselinePath = "testdata/baseline/replay_frames.csv"

var replayBaselineHeader = []string{"scenario", "step", "period", "kind", "subject", "series_tag", "member", "name", "value"}

// replayBaselineStep 是会话里的一次请求：期（相对 rt0 的分钟）与请求带来的参数。
type replayBaselineStep struct {
	minute int
	params map[string]domain.ParamSnapshot
}

// replayBaselineScenario 是一个回放场景：建夹具、打开会话，再按顺序请求每一步。
type replayBaselineScenario struct {
	name  string
	setup func(t *testing.T) *replayManager
	open  ReplayOpenRequest
	steps []replayBaselineStep
}

// replayBaselineMinutes 返回 [from, to) 每分钟一步、不带参数的请求序列。
func replayBaselineMinutes(from, to int) []replayBaselineStep {
	steps := make([]replayBaselineStep, 0, to-from)
	for minute := from; minute < to; minute++ {
		steps = append(steps, replayBaselineStep{minute: minute})
	}
	return steps
}

func replayBaselineOpen(id string, from, to, extra int) ReplayOpenRequest {
	return ReplayOpenRequest{ReplayID: id, TaskID: "ftask_replay", From: rt0.Add(time.Duration(from) * time.Minute).Format(time.RFC3339),
		To: rt0.Add(time.Duration(to) * time.Minute).Format(time.RFC3339), ExtraPeriods: extra}
}

// replayBaselinePmean 是参数用例的定义：窗口可调的均值。
func replayBaselinePmean() domain.FactorDef {
	return domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 6, default: 2}\noutput: ts_mean(close, window)\n")
}

func replayBaselineScenarios() []replayBaselineScenario {
	return []replayBaselineScenario{
		{
			// TestReplayOpenAndStep 的夹具：bias = ts_mean(close, 2)，GONE 的数据到不了终点被排除。
			name:  "base",
			setup: func(t *testing.T) *replayManager { m, _ := newReplayManagerForTest(t); return m },
			open:  replayBaselineOpen("base", 5, 10, 1),
			steps: replayBaselineMinutes(5, 10),
		},
		{
			// TestReplayUniverseExcludesNotYetListedSubjects 的夹具：LATE 第 8 分钟才上市，之前不出行，上市当期年龄为 1。
			name: "late_listing",
			setup: func(t *testing.T) *replayManager {
				m, _ := newReplayManagerForTest(t)
				src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
				fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
				cols := seriesstore.NewColumns(fields, 12)
				for i := 8; i < 20; i++ {
					cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), plainTag, false, seriesstore.Float(50), seriesstore.Float(50))
				}
				_, err := m.deps.Store.WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: "LATE", From: rt0.Add(8 * time.Minute),
					To: rt0.Add(20 * time.Minute), Rows: cols}, seriesstore.WriteOptions{})
				require.NoError(t, err)
				m.deps.Source = lateSource{}
				return m
			},
			open:  replayBaselineOpen("late_listing", 5, 10, 1),
			steps: replayBaselineMinutes(5, 10),
		},
		{
			// TestReplayStepAppliesRequestedParams 的请求序列：默认参数、同一期 A 覆盖为 4、下一期越界被拒绝并沿用、再给合法的 3。
			name: "tuned",
			setup: func(t *testing.T) *replayManager {
				m, _ := newReplayManagerWithTask(t, uncompiled(domain.EngineTask{
					Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
						ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled, BoundInstanceID: "inst"},
					Members: []domain.MemberDef{{Key: "pmean__var", Factor: replayBaselinePmean(), Kind: domain.MemberKindVar,
						Params: map[string]float64{"window": 2}}}, ResultReady: true,
				}))
				return m
			},
			open: replayBaselineOpen("tuned", 14, 17, 0),
			steps: []replayBaselineStep{
				{minute: 14},
				{minute: 15},
				{minute: 15, params: map[string]domain.ParamSnapshot{"pmean__var": {"window": {Default: 2, Overrides: map[string]float64{"A": 4}}}}},
				{minute: 16, params: map[string]domain.ParamSnapshot{"pmean__var": {"window": {Default: 99}}}},
				{minute: 16, params: map[string]domain.ParamSnapshot{"pmean__var": {"window": {Default: 3}}}},
			},
		},
		{
			// TestReplayFixedParamMemberIgnoresRequestedParams 的夹具：固定成员按固定窗口 4 计算，请求的参数被拒绝。
			name: "fixed",
			setup: func(t *testing.T) *replayManager {
				m, _ := newReplayManagerWithTask(t, uncompiled(domain.EngineTask{
					Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
						ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled},
					Members: []domain.MemberDef{{Key: "pmean__4", Factor: replayBaselinePmean(), Kind: domain.MemberKindFixed,
						Params: map[string]float64{"window": 4}}}, ResultReady: true,
				}))
				return m
			},
			open: replayBaselineOpen("fixed", 14, 17, 0),
			steps: []replayBaselineStep{
				{minute: 14},
				{minute: 15},
				{minute: 15, params: map[string]domain.ParamSnapshot{"pmean__4": {"window": {Default: 2}}}},
				{minute: 16},
			},
		},
		{
			// TestReplayObservedHistoryIncludesDelisted 的夹具：observed 历史截面里有已排除的 GONE。
			name: "observed",
			setup: func(t *testing.T) *replayManager {
				def := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: ts_mean(cs_rank(close), 3)\n")
				def.AllowPartialUniverse = true
				m, _ := newReplayManagerWithTask(t, uncompiled(domain.EngineTask{
					Set: domain.FactorTask{TaskID: "ftask_replay", SpaceID: "crypto", SourceDatasetID: "dataset_a", Freq: "1m",
						ResultDatasetID: "result_dataset_a", Status: domain.TaskStatusEnabled},
					Members: domain.AsMembers(def), ResultReady: true,
				}))
				src := seriesstore.Source{SpaceID: "crypto", DatasetID: "dataset_a", Freq: "1m"}
				fields := []seriesstore.Field{{Name: "close", Type: seriesstore.TypeFloat64}, {Name: "open", Type: seriesstore.TypeFloat64}}
				rewrite := func(subject string, base float64, minutes int) {
					cols := seriesstore.NewColumns(fields, minutes)
					for i := 0; i < minutes; i++ {
						cols.AppendRow(rt0.Add(time.Duration(i)*time.Minute), plainTag, false, seriesstore.Float(base+float64(i)), seriesstore.Float(base+float64(i)-0.5))
					}
					_, err := m.deps.Store.WriteRange(context.Background(), src, seriesstore.SubjectWrite{
						Subject: subject, From: rt0, To: rt0.Add(time.Duration(minutes) * time.Minute), Rows: cols}, seriesstore.WriteOptions{FillGaps: true})
					require.NoError(t, err)
				}
				rewrite("A", 100, 20)
				rewrite("B", 300, 20)
				rewrite("GONE", 200, 8)
				return m
			},
			open:  replayBaselineOpen("observed", 5, 10, 0),
			steps: replayBaselineMinutes(5, 10),
		},
	}
}

func replayBaselineFloat(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func replayBaselineSortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// replayBaselineRecords 把一步的响应展开成记录：每个值一行。
func replayBaselineRecords(scenario string, step int, rsp ReplayStepResponse) [][]string {
	var records [][]string
	period := rsp.Period
	add := func(kind, subject, tag, member, name, value string) {
		records = append(records, []string{scenario, strconv.Itoa(step), period, kind, subject, tag, member, name, value})
	}
	addValues := func(kind, subject string, values map[string]map[string]float64) {
		for _, member := range replayBaselineSortedKeys(values) {
			for _, name := range replayBaselineSortedKeys(values[member]) {
				add(kind, subject, plainTag, member, name, replayBaselineFloat(values[member][name]))
			}
		}
	}
	for _, subject := range replayBaselineSortedKeys(rsp.Subjects) {
		frame := rsp.Subjects[subject]
		addValues("factor", subject, frame.Factors)
		addValues("factor_prev", subject, frame.FactorsPrev)
		addValues("param", subject, frame.Params)
		add("age_bars", subject, plainTag, "", "", strconv.Itoa(frame.AgeBars))
	}
	for _, member := range replayBaselineSortedKeys(rsp.States) {
		state := rsp.States[member]
		failed := append([]string(nil), state.FailedSubjects...)
		sort.Strings(failed)
		add("state.status", "", "", member, "", state.Status)
		add("state.kind", "", "", member, "", state.Kind)
		add("state.factor_id", "", "", member, "", state.FactorID)
		add("state.definition_hash", "", "", member, "", state.DefinitionHash)
		add("state.failed_subjects", "", "", member, "", strings.Join(failed, ";"))
	}
	for _, member := range replayBaselineSortedKeys(rsp.AppliedParams) {
		snapshot := rsp.AppliedParams[member]
		for _, name := range replayBaselineSortedKeys(snapshot) {
			value := snapshot[name]
			add("applied.default", "", "", member, name, replayBaselineFloat(value.Default))
			for _, subject := range replayBaselineSortedKeys(value.Overrides) {
				add("applied.override", subject, "", member, name, replayBaselineFloat(value.Overrides[subject]))
			}
		}
	}
	rejected := make([]string, 0, len(rsp.RejectedParams))
	for _, rejection := range rsp.RejectedParams {
		rejected = append(rejected, rejection.Member)
	}
	sort.Strings(rejected)
	for _, member := range rejected {
		add("rejected", "", "", member, "", "1")
	}
	return records
}

// replayBaselineRun 跑一个场景，返回它的全部记录。
func replayBaselineRun(t *testing.T, scenario replayBaselineScenario) [][]string {
	t.Helper()
	m := scenario.setup(t)
	ctx := context.Background()
	_, _, err := m.Open(ctx, scenario.open)
	require.NoError(t, err, scenario.name)
	var records [][]string
	for i, step := range scenario.steps {
		rsp, _, err := m.Step(ctx, ReplayStepRequest{ReplayID: scenario.open.ReplayID,
			Period: rt0.Add(time.Duration(step.minute) * time.Minute).Format(time.RFC3339), Params: step.params})
		require.NoError(t, err, "场景 %s 第 %d 步", scenario.name, i+1)
		records = append(records, replayBaselineRecords(scenario.name, i+1, rsp)...)
	}
	return records
}

// replayBaselineFind 取一条记录的值；没有时第二个返回值为 false。
func replayBaselineFind(records [][]string, scenario string, step int, kind, subject, member, name string) (string, bool) {
	for _, record := range records {
		if record[0] == scenario && record[1] == strconv.Itoa(step) && record[3] == kind && record[4] == subject && record[6] == member && record[7] == name {
			return record[8], true
		}
	}
	return "", false
}

func TestReplayBaselineFrames(t *testing.T) {
	var records [][]string
	for _, scenario := range replayBaselineScenarios() {
		records = append(records, replayBaselineRun(t, scenario)...)
	}

	// 与金样无关的硬断言：确认场景标注的特性真的发生了，防止金样把错误的结果固定下来。
	find := func(scenario string, step int, kind, subject, member, name string) string {
		text, ok := replayBaselineFind(records, scenario, step, kind, subject, member, name)
		require.True(t, ok, "%s 第 %d 步缺 %s %s %s %s", scenario, step, kind, subject, member, name)
		return text
	}
	for _, record := range records {
		require.NotEqual(t, "GONE", record[4], "GONE 的数据到不了终点，不应出行：%v", record)
	}
	require.Equal(t, "6", find("base", 1, "age_bars", "A", "", ""), "第 5 分钟 A 是第 6 根")
	require.Equal(t, "104.5", find("base", 1, "factor", "A", "bias", "bias"), "(104+105)/2")
	require.Equal(t, "103.5", find("base", 1, "factor_prev", "A", "bias", "bias"), "上一期 (103+104)/2")
	require.Equal(t, "complete", find("base", 1, "state.status", "", "bias", ""))
	for step := 1; step <= 3; step++ {
		_, ok := replayBaselineFind(records, "late_listing", step, "age_bars", "LATE", "", "")
		require.False(t, ok, "第 %d 步 LATE 还没上市，不应出行", step+4)
	}
	require.Equal(t, "1", find("late_listing", 4, "age_bars", "LATE", "", ""), "LATE 上市当期是第 1 根")
	require.Equal(t, "2", find("tuned", 2, "param", "A", "pmean__var", "window"), "没有请求时用成员自己的参数")
	require.Equal(t, "113.5", find("tuned", 3, "factor", "A", "pmean__var", "pmean__var"), "A 的窗口是 4")
	require.Equal(t, "4", find("tuned", 3, "applied.override", "A", "pmean__var", "window"))
	require.Equal(t, "1", find("tuned", 4, "rejected", "", "pmean__var", ""), "越界的参数被拒绝")
	require.Equal(t, "4", find("tuned", 4, "param", "A", "pmean__var", "window"), "被拒绝时沿用上一期生效的参数")
	require.Equal(t, "3", find("tuned", 5, "applied.default", "", "pmean__var", "window"))
	require.Equal(t, "1", find("fixed", 3, "rejected", "", "pmean__4", ""), "固定成员不接受请求的参数")
	require.Equal(t, "113.5", find("fixed", 3, "factor", "A", "pmean__4", "pmean__4"), "仍按固定窗口 4 计算")
	obs7, err := strconv.ParseFloat(find("observed", 3, "factor", "A", "obs", "obs"), 64)
	require.NoError(t, err)
	require.Equal(t, math.Float64bits(1.0/3), math.Float64bits(obs7), "第 7 分钟的历史截面里有 GONE")

	replayBaselineCompare(t, replayBaselinePath, records)
}

// replayBaselineSort 按（场景、步序（数值）、记录类型、标的、series_tag、成员、名字）排序；期由步序决定，不参与排序。
func replayBaselineSort(t *testing.T, records [][]string) [][]string {
	t.Helper()
	sorted := append([][]string(nil), records...)
	step := func(record []string) int {
		n, err := strconv.Atoi(record[1])
		require.NoError(t, err)
		return n
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		if sa, sb := step(a), step(b); sa != sb {
			return sa < sb
		}
		for k := 3; k <= 7; k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return false
	})
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		require.False(t, prev[0] == cur[0] && prev[1] == cur[1] && strings.Join(prev[3:8], "\x00") == strings.Join(cur[3:8], "\x00"),
			"金样里有重复的键：%v", cur)
	}
	return sorted
}

// replayBaselineCompare 把记录排序后写成 CSV 与金样文件比较；UPDATE_BASELINE=1 时改为写入。排序与生成先后无关，
// 字节相同即值相同；不同时报出第一处不同的行。
func replayBaselineCompare(t *testing.T, path string, records [][]string) {
	t.Helper()
	records = replayBaselineSort(t, records)
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	require.NoError(t, writer.Write(replayBaselineHeader))
	require.NoError(t, writer.WriteAll(records))
	got := buffer.Bytes()
	if os.Getenv("UPDATE_BASELINE") == "1" {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, got, 0o644))
		return
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err, "没有金样 %s：用 UPDATE_BASELINE=1 生成", path)
	if bytes.Equal(want, got) {
		return
	}
	wantLines, gotLines := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	for i := 0; i < max(len(wantLines), len(gotLines)); i++ {
		var w, g string
		if i < len(wantLines) {
			w = wantLines[i]
		}
		if i < len(gotLines) {
			g = gotLines[i]
		}
		if w != g {
			t.Fatalf("%s 与金样不一致（第 %d 行）\n金样：%s\n现在：%s", path, i+1, w, g)
		}
	}
	t.Fatalf("%s 与金样的字节不一致", path)
}
