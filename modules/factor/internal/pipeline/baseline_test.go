package pipeline

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/factorlib"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/seriesset"
	"github.com/stretchr/testify/require"
)

// 基线金样（第 0 期）：因子库 modules/factor/factors/*.yaml 的全部定义，加上几条只在测试里用的合成截面定义（严格与允许部分全集各一版），
// 经 pipeline.Compute 在合成计划上的输出值与成员状态。合成计划包含 NaN、零波动段、新上市标的、截面需要的多个标的、
// 上游失败与数据层失败的标的、统一数据集（现货、永续两条序列）及其上游失败序列。金样由当前代码生成，默认只比对；
// 有意改变结果时用 UPDATE_BASELINE=1 重新生成，并在 PR 里说明原因。

const baselineOutputsPath = "testdata/baseline/factor_outputs.csv"

// baselineUpdate 报告本次是否重新生成基线金样。
func baselineUpdate() bool { return os.Getenv("UPDATE_BASELINE") == "1" }

// baselineStart 是第一个目标周期；合成数据从它往前 baselineHistory 根开始。
var baselineStart = time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)

const (
	baselineHistory = 480
	baselineTaskID  = "ftask_baseline"
)

var baselineSubjects = []string{"S1", "S2", "S3", "S4", "S5", "S6"}

// baselineCrossSpecs 是合成截面定义：每条都出严格（partial=false）与允许部分全集（id 带 _partial 后缀）两版。
var baselineCrossSpecs = []struct{ id, spec string }{
	{"cs_rank_bias", "params: {window: {type: int, min: 2, max: 120, default: 20}}\noutput: cs_rank(close / ts_mean(close, window))\n"},
	{"cs_zscore_ret", "output: cs_zscore(pct_change(close, 5))\n"},
	{"cs_mix", "let:\n  vol: ts_std(pct_change(close), 10)\noutputs:\n  demean: cs_demean(vol)\n  scale: cs_scale(cs_demean(volume))\n"},
}

// baselineDefinitions 返回因子库的全部定义（与管理端导入同一条路径）与合成截面定义。
func baselineDefinitions(t *testing.T) (library, cross []domain.FactorDef) {
	t.Helper()
	entries, err := factorlib.LoadDir(filepath.Join("..", "..", "factors"))
	require.NoError(t, err)
	for _, entry := range entries {
		def, err := domain.NormalizeFactorDefinition(domain.FactorDef{
			FactorID: entry.FactorID, Name: entry.Name, CategoryID: entry.CategoryID, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
		})
		require.NoError(t, err, "因子 %s 编译失败", entry.FactorID)
		require.Equal(t, domain.FactorTypeTimeSeries, def.FactorType, "因子库里的 %s 不是时序因子：截面覆盖由合成定义承担，需要补进金样", entry.FactorID)
		library = append(library, def)
	}
	for _, item := range baselineCrossSpecs {
		for _, partial := range []bool{false, true} {
			id := item.id
			if partial {
				id += "_partial"
			}
			def, err := domain.NormalizeFactorDefinition(domain.FactorDef{FactorID: id, Name: id, Spec: item.spec, AllowPartialUniverse: partial})
			require.NoError(t, err)
			require.Equal(t, domain.FactorTypeCrossSection, def.FactorType)
			cross = append(cross, def)
		}
	}
	return library, cross
}

// baselineSeries 描述一条合成序列：first 是第一根相对 baselineStart 的分钟数（新上市的序列晚开始），last 是最后一根；
// nullClose、nullVolume 是收盘价、成交量为 null 的根；flatFrom 起所有价格固定、成交量不变（零波动段）；shift 让同一标的的另一条序列取不同的值。
type baselineSeries struct {
	subject, tag string
	first, last  int
	nullClose    map[int]bool
	nullVolume   map[int]bool
	flatFrom     *int
	shift        int
}

// baselineBar 返回一根合成 K 线：全部由整数运算得出再除以 8，值都能精确表示，生成数据本身不引入舍入。
func baselineBar(s baselineSeries, ordinal, index int) []any {
	at := baselineStart.Add(time.Duration(index) * time.Minute)
	i := index + baselineHistory + s.shift*7
	if s.flatFrom != nil && index >= *s.flatFrom {
		i = *s.flatFrom + baselineHistory + s.shift*7
	}
	closeTicks := 8000 + ordinal*1000 + s.shift*96 + ((i*37+ordinal*53)%61)*4 + i%7
	openTicks := closeTicks - ((i*13+ordinal)%9 - 4)
	highTicks := max(openTicks, closeTicks) + (i*5+ordinal)%5 + 1
	lowTicks := min(openTicks, closeTicks) - (i*3+ordinal)%4 - 1
	volume := float64(100 + (i*29+ordinal*17+s.shift*5)%83)
	if s.flatFrom != nil && index >= *s.flatFrom {
		openTicks, highTicks, lowTicks = closeTicks, closeTicks, closeTicks
	}
	closePrice := float64(closeTicks) / 8
	var closeValue, volumeValue, quoteValue any = closePrice, volume, volume * closePrice
	if s.nullClose[index] {
		closeValue, quoteValue = nil, nil
	}
	if s.nullVolume[index] {
		volumeValue, quoteValue = nil, nil
	}
	return []any{at, s.tag, float64(openTicks) / 8, float64(highTicks) / 8, float64(lowTicks) / 8, closeValue, volumeValue, quoteValue}
}

// baselineFrames 生成各序列的源行（回看窗口加目标区间）。
func baselineFrames(series []baselineSeries) map[string]*storageio.Frame {
	frames := map[string]*storageio.Frame{}
	ordinals := map[string]int{}
	for i, subject := range baselineSubjects {
		ordinals[subject] = i + 1
	}
	for _, s := range series {
		frame := frames[s.subject]
		if frame == nil {
			frame = &storageio.Frame{SubjectID: s.subject, Columns: []string{"data_time", "series_tag", "open", "high", "low", "close", "volume", "quote_volume"}}
			frames[s.subject] = frame
		}
		for index := s.first; index <= s.last; index++ {
			frame.Rows = append(frame.Rows, baselineBar(s, ordinals[s.subject], index))
		}
	}
	return frames
}

// baselineSingleSeries 是单序列数据集的合成数据，覆盖目标区间 [0, last]：
// S2 有 null 的收盘价与成交量（回看窗口里与目标区间里各有），S3 从 -30 起零波动，S5 在 -30 才上市，S6 在目标区间里（第 3 根）才上市。
func baselineSingleSeries(last int) []baselineSeries {
	flat := -30
	return []baselineSeries{
		{subject: "S1", tag: "venue:binance", first: -baselineHistory, last: last},
		{subject: "S2", tag: "venue:binance", first: -baselineHistory, last: last,
			nullClose: map[int]bool{-100: true, -3: true, 2: true}, nullVolume: map[int]bool{-50: true, 4: true}},
		{subject: "S3", tag: "venue:binance", first: -baselineHistory, last: last, flatFrom: &flat},
		{subject: "S4", tag: "venue:binance", first: -baselineHistory, last: last},
		{subject: "S5", tag: "venue:binance", first: -30, last: last},
		{subject: "S6", tag: "venue:binance", first: 3, last: last},
	}
}

// baselineUnifiedSeries 是统一数据集的合成数据：S1～S4 各有现货、永续两条序列，S4 的永续在 -30 才上市，S2 的现货有一根 null 收盘价。
func baselineUnifiedSeries(last int) []baselineSeries {
	var out []baselineSeries
	for _, subject := range baselineSubjects[:4] {
		spot := baselineSeries{subject: subject, tag: tagSpot, first: -baselineHistory, last: last}
		swap := baselineSeries{subject: subject, tag: tagSwap, first: -baselineHistory, last: last, shift: 1}
		if subject == "S2" {
			spot.nullClose = map[int]bool{-5: true}
		}
		if subject == "S4" {
			swap.first = -30
		}
		out = append(out, spot, swap)
	}
	return out
}

// baselineData 把合成源行包装成数据层：failed、failedSeries 是数据层报告的本期取不到的标的与序列。
func baselineData(frames map[string]*storageio.Frame, failed []string, failedSeries []seriesset.Series) *frameData {
	return &frameData{
		read:         func(context.Context, Plan, time.Time) (map[string]*storageio.Frame, error) { return frames, nil },
		failed:       failed,
		failedSeries: failedSeries,
	}
}

func baselineTask() domain.FactorTask {
	return domain.FactorTask{TaskID: baselineTaskID, SpaceID: "crypto", SourceDatasetID: "dataset_baseline", ResultDatasetID: "dataset_factor_baseline",
		Freq: "1m", Status: domain.TaskStatusEnabled}
}

// baselineRecalcPlan 是补算计划：目标区间 [baselineStart, baselineStart+periods)。
func baselineRecalcPlan(members []domain.MemberDef, periods int, expectedSeries []seriesset.Series) Plan {
	plan := Plan{Mode: ModeRecalc, Set: baselineTask(), Members: members, TargetStart: baselineStart,
		TargetEnd: baselineStart.Add(time.Duration(periods) * time.Minute), WriteCarry: true}
	plan.Expected, plan.Available = append([]string(nil), baselineSubjects...), append([]string(nil), baselineSubjects...)
	if len(expectedSeries) > 0 {
		plan.ExpectedSeries = uniqueSeriesSorted(expectedSeries)
		plan.Expected = seriesSubjects(plan.ExpectedSeries)
		plan.Available = append([]string(nil), plan.Expected...)
	}
	return plan
}

type baselineScenario struct {
	name string
	plan Plan
	data *frameData
}

func baselineScenarios(t *testing.T, members []domain.MemberDef) []baselineScenario {
	t.Helper()
	var unifiedSeries []seriesset.Series
	for _, subject := range baselineSubjects[:4] {
		unifiedSeries = append(unifiedSeries, series(subject, tagSpot), series(subject, tagSwap))
	}
	// 实时、单序列：事件里 S4 上游失败，数据层又报 S2 本期取不到。
	live, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{Set: baselineTask(), Members: members, PeriodTime: baselineStart.Add(5 * time.Minute),
		Universe: baselineSubjects, UpstreamFailed: []string{"S4"}})
	require.NoError(t, err)
	// 实时、统一数据集：事件里 S2 的永续序列上游失败（本地仍有它的行）。
	unifiedLive, err := BuildLivePlan(periodclock.Continuous{}, LiveInput{Set: baselineTask(), Members: members, PeriodTime: baselineStart.Add(2 * time.Minute),
		Universe: baselineSubjects[:4], UniverseSeries: unifiedSeries, UpstreamFailedSeries: []seriesset.Series{series("S2", tagSwap)}})
	require.NoError(t, err)
	return []baselineScenario{
		{name: "single_recalc", plan: baselineRecalcPlan(members, 6, nil), data: baselineData(baselineFrames(baselineSingleSeries(5)), nil, nil)},
		{name: "single_live_failed", plan: live, data: baselineData(baselineFrames(baselineSingleSeries(5)), []string{"S2"}, nil)},
		{name: "unified_recalc", plan: baselineRecalcPlan(members, 3, unifiedSeries), data: baselineData(baselineFrames(baselineUnifiedSeries(2)), nil, nil)},
		{name: "unified_live_failed_series", plan: unifiedLive, data: baselineData(baselineFrames(baselineUnifiedSeries(2)), nil, nil)},
	}
}

// baselineRun 按场景执行 Load 与 Compute。
func baselineRun(t *testing.T, scenario baselineScenario, workers int) Computation {
	t.Helper()
	runner := NewRunner(&fakeStore{}, scenario.data, periodclock.Continuous{}, Config{ComputeWorkers: workers})
	loaded, err := runner.Load(context.Background(), scenario.plan)
	require.NoError(t, err)
	computation, err := runner.Compute(context.Background(), scenario.plan, loaded)
	require.NoError(t, err)
	return computation
}

// baselineValue 把一个输出值写成文本：null 写 null，浮点按最短的精确表示（逐位比较）。
func baselineValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func baselineSeriesText(items []seriesset.Series) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, item.SubjectID+"@"+item.SeriesTag)
	}
	return strings.Join(parts, ";")
}

// baselineRecords 把一次计算转成 CSV 记录：每个成员先是状态（status、失败标的与序列、跳过的 tag、截面各周期结果、整体失败原因），
// 再是逐行的输出值，按（标的、series_tag、时间）排序，同一行的各输出按定义顺序。没有源行的时刻不产生记录，null 写 null。
func baselineRecords(name string, plan Plan, computation Computation) [][]string {
	var records [][]string
	keys := make([]string, 0, len(plan.Members))
	for _, member := range plan.Members {
		keys = append(keys, member.Key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		state := computation.FactorStates[key]
		add := func(column, value string) {
			records = append(records, []string{name, key, "state", "", "", "", column, value})
		}
		add("status", state.Status)
		add("failed_subjects", strings.Join(state.FailedSubjects, ";"))
		add("failed_series", baselineSeriesText(state.FailedSeries))
		add("skipped_series_tags", strings.Join(state.SkippedSeriesTags, ";"))
		periods := make([]int64, 0, len(computation.CrossPeriods[key]))
		for period := range computation.CrossPeriods[key] {
			periods = append(periods, period)
		}
		sort.Slice(periods, func(i, j int) bool { return periods[i] < periods[j] })
		for _, period := range periods {
			add("cross_period@"+time.Unix(period, 0).UTC().Format(time.RFC3339), string(computation.CrossPeriods[key][period]))
		}
		if err := computation.Failures[key]; err != nil {
			add("failure", err.Error())
		}
		subjects := make([]string, 0, len(computation.Results[key]))
		for subject := range computation.Results[key] {
			subjects = append(subjects, subject)
		}
		sort.Strings(subjects)
		type valueRow struct {
			subject, tag string
			at           time.Time
			columns      []string
			values       []any
		}
		var rows []valueRow
		for _, subject := range subjects {
			item := computation.Results[key][subject]
			if item.Err != nil {
				records = append(records, []string{name, key, "error", subject, "", "", "error", item.Err.Error()})
				continue
			}
			first := 2
			if subject == "" {
				first = 3 // 截面因子的结果合在一起，第 3 列是标的
			}
			for _, row := range item.Rows {
				id := subject
				if subject == "" {
					id = row[2].(string)
				}
				rows = append(rows, valueRow{subject: id, tag: row[1].(string), at: row[0].(time.Time), columns: item.Columns[first:], values: row[first:]})
			}
		}
		sort.SliceStable(rows, func(i, j int) bool {
			a, b := rows[i], rows[j]
			if a.subject != b.subject {
				return a.subject < b.subject
			}
			if a.tag != b.tag {
				return a.tag < b.tag
			}
			return a.at.Before(b.at)
		})
		for _, row := range rows {
			for c, column := range row.columns {
				records = append(records, []string{name, key, "value", row.subject, row.tag, row.at.UTC().Format(time.RFC3339), column, baselineValue(row.values[c])})
			}
		}
	}
	return records
}

// baselineNonNull 数出一个成员在一次计算里的非空输出值个数。
func baselineNonNull(computation Computation, key string) int {
	count := 0
	for _, item := range computation.Results[key] {
		first := 2
		if len(item.Columns) > 2 && item.Columns[2] == "subject_id" {
			first = 3
		}
		for _, row := range item.Rows {
			for _, value := range row[first:] {
				if value != nil {
					count++
				}
			}
		}
	}
	return count
}

// baselineValueAt 取一个成员在（标的、tag、时刻）的第一个输出值；第二个返回值报告有没有这一行。
func baselineValueAt(computation Computation, key, subject, tag string, at time.Time) (any, bool) {
	for resultSubject, item := range computation.Results[key] {
		first := 2
		if resultSubject == "" {
			first = 3
		}
		for _, row := range item.Rows {
			id := resultSubject
			if resultSubject == "" {
				id = row[2].(string)
			}
			if id == subject && row[1] == tag && row[0].(time.Time).Equal(at) {
				return row[first], true
			}
		}
	}
	return nil, false
}

// baselineCheckScenarios 是与金样无关的硬断言：确认每个场景标注的特性真的发生了。
func baselineCheckScenarios(t *testing.T, library, cross []domain.FactorDef, plans map[string]Plan, results map[string]Computation) {
	t.Helper()
	keyOf := func(plan Plan, factorID string) string {
		for _, member := range plan.Members {
			if member.Factor.FactorID == factorID {
				return member.Key
			}
		}
		t.Fatalf("计划里没有因子 %s", factorID)
		return ""
	}
	single := results["single_recalc"]
	plan := plans["single_recalc"]
	for _, def := range append(append([]domain.FactorDef(nil), library...), cross...) {
		require.Positive(t, baselineNonNull(single, keyOf(plan, def.FactorID)), "因子 %s 在 single_recalc 里没有任何非空值：合成数据不够长", def.FactorID)
		require.Equal(t, "complete", single.FactorStates[keyOf(plan, def.FactorID)].Status)
	}
	ma := keyOf(plan, "ma")
	if value, ok := baselineValueAt(single, ma, "S2", "venue:binance", baselineStart.Add(2*time.Minute)); !ok || value != nil {
		t.Fatalf("S2 在第 2 根收盘价为 null：ma 应有行且为 null，实际 %v（有行 %v）", value, ok)
	}
	if _, ok := baselineValueAt(single, ma, "S6", "venue:binance", baselineStart.Add(2*time.Minute)); ok {
		t.Fatal("S6 第 3 根才上市：第 2 根不应有行")
	}
	if value, ok := baselineValueAt(single, keyOf(plan, "ema"), "S5", "venue:binance", baselineStart); !ok || value != nil {
		t.Fatalf("S5 新上市，ema 的严格窗口不完整应为 null：%v（有行 %v）", value, ok)
	}
	if value, ok := baselineValueAt(single, keyOf(plan, "min_max"), "S3", "venue:binance", baselineStart); !ok || value != nil {
		t.Fatalf("S3 零波动段的 min_max 是 0/0，应为 null：%v（有行 %v）", value, ok)
	}

	live, livePlan := results["single_live_failed"], plans["single_live_failed"]
	for _, def := range library {
		state := live.FactorStates[keyOf(livePlan, def.FactorID)]
		require.Equal(t, "degraded", state.Status, def.FactorID)
		require.Equal(t, []string{"S2", "S4"}, state.FailedSubjects, def.FactorID)
	}
	for _, item := range baselineCrossSpecs {
		require.Equal(t, "skipped", live.FactorStates[keyOf(livePlan, item.id)].Status, "严格截面因子在缺数时应整体跳过：%s", item.id)
		require.Equal(t, "degraded", live.FactorStates[keyOf(livePlan, item.id+"_partial")].Status, "允许部分全集的截面因子应在可用标的上计算：%s", item.id)
		require.Positive(t, baselineNonNull(live, keyOf(livePlan, item.id+"_partial")))
	}

	unified, unifiedPlan := results["unified_live_failed_series"], plans["unified_live_failed_series"]
	for _, item := range baselineCrossSpecs {
		strict := unified.FactorStates[keyOf(unifiedPlan, item.id)]
		require.Equal(t, "degraded", strict.Status, item.id)
		require.Equal(t, []string{tagSwap}, strict.SkippedSeriesTags, "严格截面因子只跳过永续：%s", item.id)
		partial := unified.FactorStates[keyOf(unifiedPlan, item.id+"_partial")]
		require.Empty(t, partial.SkippedSeriesTags, item.id)
		if _, ok := baselineValueAt(unified, keyOf(unifiedPlan, item.id+"_partial"), "S1", tagSwap, baselineStart.Add(2*time.Minute)); !ok {
			t.Fatalf("允许部分全集的 %s 应在永续的其余序列上算出结果", item.id)
		}
	}
	if _, ok := baselineValueAt(unified, keyOf(unifiedPlan, "ma"), "S2", tagSwap, baselineStart.Add(2*time.Minute)); ok {
		t.Fatal("上游失败的 S2 永续序列不进面板，不应有结果行")
	}
	recalc, recalcPlan := results["unified_recalc"], plans["unified_recalc"]
	spot, okSpot := baselineValueAt(recalc, keyOf(recalcPlan, "ma"), "S1", tagSpot, baselineStart)
	swap, okSwap := baselineValueAt(recalc, keyOf(recalcPlan, "ma"), "S1", tagSwap, baselineStart)
	require.True(t, okSpot && okSwap && spot != nil && swap != nil && spot != swap, "统一数据集的两条序列应各自计算：%v / %v", spot, swap)
}

func TestBaselineFactorOutputs(t *testing.T) {
	library, cross := baselineDefinitions(t)
	members := domain.AsMembers(append(append([]domain.FactorDef(nil), library...), cross...)...)
	plans := map[string]Plan{}
	results := map[string]Computation{}
	var records [][]string
	for _, scenario := range baselineScenarios(t, members) {
		computation := baselineRun(t, scenario, 1)
		parallel := baselineRun(t, scenario, 8)
		require.Equal(t, computation.Results, parallel.Results, "场景 %s 的结果与并行度有关", scenario.name)
		require.Equal(t, computation.FactorStates, parallel.FactorStates, "场景 %s 的成员状态与并行度有关", scenario.name)
		plans[scenario.name], results[scenario.name] = scenario.plan, computation
		records = append(records, baselineRecords(scenario.name, scenario.plan, computation)...)
	}
	baselineCheckScenarios(t, library, cross, plans, results)
	for _, record := range records {
		if record[2] == "value" && record[7] != "null" {
			value, err := strconv.ParseFloat(record[7], 64)
			require.NoError(t, err)
			require.False(t, math.IsNaN(value), "NaN 应写成 null：%v", record)
		}
	}
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	require.NoError(t, writer.Write([]string{"scenario", "member", "kind", "subject", "series_tag", "data_time", "column", "value"}))
	require.NoError(t, writer.WriteAll(records))
	got := buffer.Bytes()
	if baselineUpdate() {
		require.NoError(t, os.MkdirAll(filepath.Dir(baselineOutputsPath), 0o755))
		require.NoError(t, os.WriteFile(baselineOutputsPath, got, 0o644))
		return
	}
	want, err := os.ReadFile(baselineOutputsPath)
	require.NoError(t, err, "没有金样 %s：用 UPDATE_BASELINE=1 生成", baselineOutputsPath)
	if !bytes.Equal(want, got) {
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
				t.Fatalf("因子输出与基线金样不一致（第 %d 行）\n金样：%s\n现在：%s", i+1, w, g)
			}
		}
		t.Fatal("因子输出与基线金样的字节不一致")
	}
}
