package localdata

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/recalcexec"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/stretchr/testify/require"
)

// 补算金样（批 3 T0 第 2 项）：单序列数据集（所有行同一个非空 series_tag）上，经
// 补算执行器、管线与本地数据层（与引擎的正式补算同一条路径）跑一段补算，把写出的结果行（含 __params）与各分块的结果存成数据文件。
// 输入按序列给（期望序列是“标的 × 唯一的 series_tag”），走按序列的分支；结果要对着这份数据文件逐位一致，
// 这份文件最初是按标的处理时生成的，所以它同时证明单序列数据集上两种口径的结果相同。
// 场景：全部成员、只补可变成员、只补固定成员、只填充、只选数据齐全的标的；数据里有收盘价为 null 的根、中途断档的标的、中途停止的标的、区间内才上市的标的、
// 本地完全没有数据的标的，已有结果行里记着与默认值不同的 __params（补算按它分参数段并合并写回）。
// __params 经真实的 recalcexec.StorageParamReader 从结果数据集读（批 3 T9）：它只记可变成员，被选成员里没有可变成员时不读、不按参数分段、
// 不写这一列，固定成员也不再记“按默认参数新补”。
// 按序列的名单（failed_series）不进金样：按标的处理时它们是空的，按序列处理时有内容；
// 按标的的名单、状态、逐项结果与写出的行必须不变。
// 金样由当前代码生成，默认只比对；有意改变结果时用 UPDATE_BASELINE=1 重新生成。

const (
	baselineRecalcPath = "testdata/baseline/single_series_recalc.csv"
	// baselineRecalcTag 是这个数据集唯一的 series_tag。
	baselineRecalcTag = "venue:binance|market:spot|source:spot_http"
	// 补算区间 [baselineRecalcFrom, baselineRecalcTo)，分钟序号相对 t0。
	baselineRecalcFrom = 40
	baselineRecalcTo   = 52
	// baselineRecalcVarKey 是可变成员 pmean 的成员键。
	baselineRecalcVarKey = "pmean__var"
)

// baselineRecalcSubjects 是补算选中的标的：S5 在本地没有任何数据。
var baselineRecalcSubjects = []string{"S1", "S2", "S3", "S4", "S5", "S6"}

// baselineRecalcBars 是各标的在本地有数据的分钟：S3 在区间中途断档三根（本地是填充行），S4 在区间内才上市，
// S6 的数据在区间中途停止（之后没有行）。
func baselineRecalcBars() map[string][]int {
	return map[string][]int{
		"S1": minutesBetween(0, 59),
		"S2": minutesBetween(0, 59),
		"S3": append(minutesBetween(0, 44), minutesBetween(48, 59)...),
		"S4": minutesBetween(46, 59),
		"S6": minutesBetween(0, 47),
	}
}

// baselineRecalcNullClose 是收盘价为 null 的根：S2 在回看窗口里与目标区间里各一根。
var baselineRecalcNullClose = map[string]map[int]bool{"S2": {37: true, 43: true}}

// baselineRecalcValue 返回一根合成 K 线的收盘价与成交量：全部由整数运算得出再除以 8，值都能精确表示。
func baselineRecalcValue(ordinal, minute int) (closePrice, volume float64) {
	ticks := 8000 + ordinal*1000 + ((minute*37+ordinal*53)%61)*4 + minute%7
	return float64(ticks) / 8, float64(100 + (minute*29+ordinal*17)%83)
}

// baselineRecalcPutLocal 把一个标的的合成数据直接写进本地时序存储。断档处分开写，并与同步器一样允许补缺：
// 中间缺的周期在本地是填充行（is_filled），不是没有行。
func baselineRecalcPutLocal(t *testing.T, fx *fixture, subject string, ordinal int, minutes []int) {
	t.Helper()
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields, ok, err := fx.data.Store().Schema(src)
	require.NoError(t, err)
	require.True(t, ok)
	for start := 0; start < len(minutes); {
		end := start + 1
		for end < len(minutes) && minutes[end] == minutes[end-1]+1 {
			end++
		}
		run := minutes[start:end]
		rows := seriesstore.NewColumns(fields, len(run))
		for _, m := range run {
			closePrice, volume := baselineRecalcValue(ordinal, m)
			values := make([]seriesstore.Value, len(fields))
			for i, field := range fields {
				switch {
				case field.Name == "close" && baselineRecalcNullClose[subject][m]:
					values[i] = seriesstore.Value{Null: true}
				case field.Name == "close":
					values[i] = seriesstore.Float(closePrice)
				case field.Name == "volume":
					values[i] = seriesstore.Float(volume)
				default:
					t.Fatalf("本地存储里有没预料到的字段 %s", field.Name)
				}
			}
			rows.AppendRow(at(m), baselineRecalcTag, false, values...)
		}
		_, err = fx.data.Store().WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: subject, From: at(run[0]), To: at(run[len(run)-1] + 1), Rows: rows}, seriesstore.WriteOptions{FillGaps: true})
		require.NoError(t, err)
		start = end
	}
}

// baselineRecalcMembers 返回任务的成员：固定的时序因子、可变成员（默认 window 为 4）、严格与允许部分全集的截面因子。
func baselineRecalcMembers(t *testing.T) (fixed []domain.FactorDef, variable domain.MemberDef) {
	t.Helper()
	partial, err := domain.NormalizeFactorDefinition(domain.FactorDef{FactorID: "rank_partial", Name: "rank_partial",
		Spec: "output: cs_rank(ts_mean(close, 3))\n", AllowPartialUniverse: true})
	require.NoError(t, err)
	fixed = []domain.FactorDef{mean3(), strictRank(), partial}
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 20, default: 4}\noutput: ts_mean(close, window)\n")
	return fixed, domain.MemberDef{Key: baselineRecalcVarKey, Factor: def, Kind: domain.MemberKindVar, Params: map[string]float64{"window": 4}}
}

// baselineRecalcFixture 建好本地数据与已有的结果行。已有行（都带旧的 mean 值）：
// 第 41～44 分钟 S1、S2 的 pmean 记着 window=6；第 45 分钟 S1 记着 window=8，S2 的行里没有 pmean；
// 每一行还记着一个已经不在任务里的可变成员 gone__var，补算只替换本次计算的可变成员，其余记录保留。__params 只记可变成员，已有行里没有固定成员的条目。
func baselineRecalcFixture(t *testing.T) (*fixture, []domain.MemberDef) {
	t.Helper()
	fixed, variable := baselineRecalcMembers(t)
	fx := newFixture(t, fixed...)
	fx.set.Members = append(fx.set.Members, variable)
	sort.Slice(fx.set.Members, func(i, j int) bool { return fx.set.Members[i].Key < fx.set.Members[j].Key })
	// Prepare 需要 Storage 里先有数据才会建本地的列定义；之后本地内容由下面的直接写入整段替换。
	for _, subject := range []string{"S1", "S2", "S3"} {
		fx.st.putTag(subject, baselineRecalcTag, 100, minutesBetween(0, 9)...)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"S1", "S2", "S3"}))
	bars := baselineRecalcBars()
	for i, subject := range baselineRecalcSubjects {
		if minutes := bars[subject]; len(minutes) > 0 {
			baselineRecalcPutLocal(t, fx, subject, i+1, minutes)
		}
	}
	fx.now = at(60)
	preset := func(subject string, minute int, pmean map[string]float64) {
		members := map[string]map[string]float64{"gone__var": {"span": 5}}
		if pmean != nil {
			members[variable.Key] = pmean
		}
		raw, err := json.Marshal(members)
		require.NoError(t, err)
		fx.st.results[subject+at(minute).Format(time.RFC3339)+baselineRecalcTag] = storageio.ResultRow{SubjectID: subject, DataTime: at(minute), SeriesTag: baselineRecalcTag,
			Fields: map[string]any{"mean": -1.0, factorname.ParamsColumn: storageio.JSONText(raw)}}
	}
	for minute := 41; minute <= 44; minute++ {
		preset("S1", minute, map[string]float64{"window": 6})
		preset("S2", minute, map[string]float64{"window": 6})
	}
	preset("S1", 45, map[string]float64{"window": 8})
	preset("S2", 45, nil)
	return fx, fx.set.Members
}

// baselineResultStore 让真实的 recalcexec.StorageParamReader 读内存里的结果行：ReadWindow 按请求的列返回结果数据集的行，
// 并记下每次读取（固定成员的补算不应读 __params）。其余方法不会被参数读取方调用。
type baselineResultStore struct {
	storageio.Store
	st    *fakeStorage
	mu    sync.Mutex
	reads []storageio.ReadRequest
}

func (r *baselineResultStore) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	r.mu.Lock()
	r.reads = append(r.reads, req)
	r.mu.Unlock()
	r.st.mu.Lock()
	defer r.st.mu.Unlock()
	wanted := make(map[string]bool, len(req.Subjects))
	for _, subject := range req.Subjects {
		wanted[subject] = true
	}
	frames := map[string]*storageio.Frame{}
	keys := make([]string, 0, len(r.st.results))
	for key := range r.st.results {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		row := r.st.results[key]
		if !wanted[row.SubjectID] || row.DataTime.Before(req.Start) || !row.DataTime.Before(req.End) {
			continue
		}
		frame := frames[row.SubjectID]
		if frame == nil {
			frame = &storageio.Frame{SubjectID: row.SubjectID, Columns: append([]string{"data_time", "series_tag"}, req.Columns...)}
			frames[row.SubjectID] = frame
		}
		values := []any{row.DataTime, row.SeriesTag}
		for _, column := range req.Columns {
			values = append(values, row.Fields[column])
		}
		frame.Rows = append(frame.Rows, values)
	}
	return frames, nil
}

func (r *baselineResultStore) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reads)
}

type baselineRecalcScenario struct {
	name string
	// pick 从任务的全部成员里选出本次补算的成员；nil 表示全部。
	pick     func(domain.MemberDef) bool
	fillOnly bool
	// subjects 是补算选中的标的；nil 表示 baselineRecalcSubjects。
	subjects []string
}

func baselineRecalcScenarios() []baselineRecalcScenario {
	return []baselineRecalcScenario{
		{name: "all_members"},
		{name: "var_member_only", pick: func(member domain.MemberDef) bool { return member.IsVar() }},
		{name: "fixed_members_only", pick: func(member domain.MemberDef) bool { return !member.IsVar() }},
		{name: "fill_only", fillOnly: true},
		// 只选区间内数据齐全的标的：严格全集的截面因子能算出值（其余场景里它因为有标的缺数而整段跳过）。
		{name: "complete_subjects", subjects: []string{"S1", "S2", "S3"}},
	}
}

func baselineRecalcText(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	case storageio.JSONText:
		return string(typed)
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// baselineRecalcRun 跑一个场景，返回 CSV 记录。frozen 为假时 Selection 不是冻结全集（人工补算、失效重算的接线，
// 期望标的由数据层收窄），为真时按冻结全集处理（读到 marker 冻结全集的恢复的接线）。
// 记录里：先是每个分块的结果（状态、失败名单、成员状态、逐项结果、按默认参数新补的期次、告警），
// 再是结果数据集里的全部行（含补算前已有的行），按（标的、series_tag、时间、列名）排序。
func baselineRecalcRun(t *testing.T, scenario baselineRecalcScenario, frozen bool) [][]string {
	t.Helper()
	fx, members := baselineRecalcFixture(t)
	selected := make([]domain.MemberDef, 0, len(members))
	for _, member := range members {
		if scenario.pick == nil || scenario.pick(member) {
			selected = append(selected, member)
		}
	}
	require.NotEmpty(t, selected)
	subjects := scenario.subjects
	if subjects == nil {
		subjects = baselineRecalcSubjects
	}
	selection := recalcexec.Selection{Set: fx.set.Set, Members: selected, Subjects: subjects,
		Columns: []string{"close", "volume"}, FillOnly: scenario.fillOnly, FrozenUniverse: frozen}
	var records [][]string
	add := func(kind, subject, tag, dataTime, column, value string) {
		records = append(records, []string{scenario.name, kind, subject, tag, dataTime, column, value})
	}
	stamp := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	// 每 5 期一块：12 期分成三块，参数段还会在块内再分。
	results := &baselineResultStore{st: fx.st}
	executor := recalcexec.NewExecutor(fx.runner, recalcexec.WithChunkPeriods(5), recalcexec.WithParamReader(recalcexec.NewStorageParamReader(results)))
	err := executor.Run(context.Background(), recalcexec.Window{Start: at(baselineRecalcFrom), End: at(baselineRecalcTo)},
		func(context.Context) (recalcexec.Selection, error) { return selection, nil },
		func(_ context.Context, progress time.Time, outcome pipeline.Outcome) (bool, error) {
			chunk := stamp(progress)
			add("chunk", "", "", chunk, "status", outcome.Status)
			add("chunk", "", "", chunk, "failed_subjects", strings.Join(outcome.FailedSubjects, ";"))
			add("chunk", "", "", chunk, "rows_written", strconv.Itoa(outcome.RowsWritten))
			add("chunk", "", "", chunk, "warming", strconv.Itoa(outcome.Warming))
			states := append([]storageio.FactorState(nil), outcome.Factors...)
			sort.SliceStable(states, func(i, j int) bool { return states[i].MemberKey < states[j].MemberKey })
			for _, state := range states {
				add("member", "", "", chunk, state.MemberKey+".status", state.Status)
				add("member", "", "", chunk, state.MemberKey+".failed_subjects", strings.Join(state.FailedSubjects, ";"))
			}
			keys := make([]pipeline.ItemKey, 0, len(outcome.Items))
			for key := range outcome.Items {
				keys = append(keys, key)
			}
			sort.Slice(keys, func(i, j int) bool {
				a, b := keys[i], keys[j]
				if a.Member != b.Member {
					return a.Member < b.Member
				}
				if a.Subject != b.Subject {
					return a.Subject < b.Subject
				}
				return a.Period.Before(b.Period)
			})
			for _, key := range keys {
				add("item", key.Subject, "", stamp(key.Period), key.Member, string(outcome.Items[key]))
			}
			filledKeys := make([]string, 0, len(outcome.DefaultFilled))
			for key := range outcome.DefaultFilled {
				filledKeys = append(filledKeys, key)
			}
			sort.Strings(filledKeys)
			for _, key := range filledKeys {
				periods := append([]time.Time(nil), outcome.DefaultFilled[key]...)
				sort.Slice(periods, func(i, j int) bool { return periods[i].Before(periods[j]) })
				for _, period := range periods {
					add("default_filled", "", "", stamp(period), key, chunk)
				}
			}
			warned := make([]string, 0, len(outcome.Warnings))
			for key := range outcome.Warnings {
				warned = append(warned, key)
			}
			sort.Strings(warned)
			for _, key := range warned {
				reasons := make([]string, 0, len(outcome.Warnings[key]))
				for reason := range outcome.Warnings[key] {
					reasons = append(reasons, reason)
				}
				sort.Strings(reasons)
				for _, reason := range reasons {
					add("warning", "", "", chunk, key+"."+reason, strconv.Itoa(outcome.Warnings[key][reason]))
				}
			}
			return false, nil
		})
	require.NoError(t, err)
	// 被选成员里没有可变成员时不读 __params；有可变成员时每个参数段之前都读，请求只带 __params 一列。
	hasVar := false
	for _, member := range selected {
		hasVar = hasVar || member.IsVar()
	}
	if !hasVar {
		require.Zero(t, results.readCount(), "场景 %s：只补固定成员不应读 __params", scenario.name)
	} else {
		require.NotZero(t, results.readCount(), "场景 %s：有可变成员时要读 __params", scenario.name)
		for _, req := range results.reads {
			require.Equal(t, []string{factorname.ParamsColumn}, req.Columns)
		}
	}

	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	require.Empty(t, fx.st.markers, "补算不上报周期标记")
	rows := make([]storageio.ResultRow, 0, len(fx.st.results))
	for _, row := range fx.st.results {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.SubjectID != b.SubjectID {
			return a.SubjectID < b.SubjectID
		}
		if a.SeriesTag != b.SeriesTag {
			return a.SeriesTag < b.SeriesTag
		}
		return a.DataTime.Before(b.DataTime)
	})
	for _, row := range rows {
		columns := make([]string, 0, len(row.Fields))
		for column := range row.Fields {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		for _, column := range columns {
			add("row", row.SubjectID, row.SeriesTag, stamp(row.DataTime), column, baselineRecalcText(row.Fields[column]))
		}
	}
	return records
}

// withoutPreListing 去掉 S4 上市（第 46 分钟）之前的分块记录与 S4 的逐项结果，用于比较两种接线：数据层给期望序列时，
// 区间结束时还没上市的 S4 不在期望里；随 Selection 带上的序列整个窗口都含 S4（真实的冻结全集不会含还没上市的标的）。
func withoutPreListing(records [][]string) [][]string {
	listed := at(46).UTC().Format(time.RFC3339)
	var out [][]string
	for _, record := range records {
		kind, subject, dataTime := record[1], record[2], record[4]
		if (kind == "chunk" || kind == "member") && dataTime <= listed || kind == "item" && subject == "S4" && dataTime < listed {
			continue
		}
		out = append(out, record)
	}
	return out
}

// baselineRecalcFind 取一条记录的值；没有时第二个返回值为 false。
func baselineRecalcFind(records [][]string, scenario, kind, subject, dataTime, column string) (string, bool) {
	for _, record := range records {
		if record[0] == scenario && record[1] == kind && record[2] == subject && record[4] == dataTime && record[5] == column {
			return record[6], true
		}
	}
	return "", false
}

func TestBaselineSingleSeriesRecalc(t *testing.T) {
	var records [][]string
	for _, scenario := range baselineRecalcScenarios() {
		got := baselineRecalcRun(t, scenario, false)
		require.Equal(t, withoutPreListing(got), withoutPreListing(baselineRecalcRun(t, scenario, true)), "场景 %s：期望标的由数据层收窄与按冻结全集处理，结果不同", scenario.name)
		records = append(records, got...)
	}
	// 与金样无关的硬断言：确认场景标注的特性真的发生了。
	stamp := func(minute int) string { return at(minute).UTC().Format(time.RFC3339) }
	value := func(scenario, subject string, minute int, column string) (string, bool) {
		return baselineRecalcFind(records, scenario, "row", subject, stamp(minute), column)
	}
	if text, ok := value("all_members", "S1", 50, "mean"); !ok || text == "null" {
		t.Fatalf("S1 第 50 分钟的 mean 应算出：%q（有行 %v）", text, ok)
	}
	if text, ok := value("all_members", "S2", 43, "mean"); !ok || text != "null" {
		t.Fatalf("S2 第 43 分钟收盘价为 null，mean 应为 null：%q（有行 %v）", text, ok)
	}
	if _, ok := value("all_members", "S3", 46, "close"); !ok {
		t.Fatal("S3 第 46 分钟断档，本地是填充行，应写出结果行")
	}
	if _, ok := value("all_members", "S6", 47, "mean"); !ok {
		t.Fatal("S6 第 47 分钟还有数据，应有结果行")
	}
	if _, ok := value("all_members", "S6", 48, "mean"); ok {
		t.Fatal("S6 第 48 分钟起没有源行，不应写出结果行")
	}
	if _, ok := value("all_members", "S4", 45, "mean"); ok {
		t.Fatal("S4 第 46 分钟才上市，之前不应有结果行")
	}
	if _, ok := value("all_members", "S4", 50, "mean"); !ok {
		t.Fatal("S4 上市后应有结果行")
	}
	for _, record := range records {
		if record[1] == "row" && record[2] == "S5" {
			t.Fatalf("S5 本地没有数据，不应写出结果行：%v", record)
		}
	}
	windowOf := func(scenario, subject string, minute int) string {
		text, ok := value(scenario, subject, minute, factorname.ParamsColumn)
		require.True(t, ok, "%s %s 第 %d 分钟应有 __params", scenario, subject, minute)
		members, err := factorname.ParseParams(text)
		require.NoError(t, err)
		window, ok := members[baselineRecalcVarKey]["window"]
		if !ok {
			return "none"
		}
		return strconv.FormatFloat(window, 'g', -1, 64)
	}
	require.Equal(t, "4", windowOf("all_members", "S1", 40), "没有已有行的期次按默认参数")
	require.Equal(t, "6", windowOf("all_members", "S1", 42), "已有行记着 window=6，补算按它还原")
	require.Equal(t, "8", windowOf("all_members", "S1", 45))
	require.Equal(t, "4", windowOf("all_members", "S2", 45), "已有行里没有这个成员，按默认参数新补")
	if text, ok := value("all_members", "S1", 50, "rank"); !ok || text != "null" {
		t.Fatalf("有标的缺数时严格全集的截面因子应跳过、写 null：%q（有行 %v）", text, ok)
	}
	if text, ok := value("complete_subjects", "S1", 50, "rank"); !ok || text == "null" {
		t.Fatalf("标的数据齐全时严格全集的截面因子应算出：%q（有行 %v）", text, ok)
	}
	text, _ := value("var_member_only", "S1", 42, factorname.ParamsColumn)
	require.Contains(t, text, "gone__var", "只补可变成员时，其余成员在 __params 里的记录保留")
	text, _ = value("var_member_only", "S1", 42, "mean")
	require.Equal(t, "-1", text, "只补可变成员时不动固定成员已有的值")
	require.Equal(t, "6", windowOf("fixed_members_only", "S1", 42), "只补固定成员时不动可变成员的参数记录")
	_, filled := baselineRecalcFind(records, "all_members", "default_filled", "", stamp(40), baselineRecalcVarKey)
	require.True(t, filled, "第 40 分钟没有已有行，应记为按默认参数新补")

	baselineRecalcCompare(t, baselineRecalcPath, records)
}

// baselineRecalcCompare 把记录写成 CSV 与金样文件按字节比较；UPDATE_BASELINE=1 时改为写入。
func baselineRecalcCompare(t *testing.T, path string, records [][]string) {
	t.Helper()
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	require.NoError(t, writer.Write([]string{"scenario", "kind", "subject", "series_tag", "data_time", "column", "value"}))
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

// 实时金样：同一个单序列数据集上连续算五期，完成事件带“标的 × 唯一的 tag”的序列全集，把每期的结果、上报的周期标记与写出的结果行存成数据文件
// （金样最初按标的口径生成）。
// 第 9 分钟 L5 没有数据；第 10 分钟 L4 的 K 线还没到；第 11 分钟 L4 补到（缺的第 10 根在本地按规则填充）；第 12 分钟事件里 L2 上游失败；
// 第 13 分钟全集里没有 L5、四个标的都齐（严格全集的截面因子只有这一期能算出）。
// 周期标记里按序列的全集与名单同样不进金样，由派生断言检查（requireSingleTagLists）。

const baselineLivePath = "testdata/baseline/single_series_live.csv"

var baselineLiveSubjects = []string{"L1", "L2", "L3", "L4", "L5"}

// baselineLivePut 往 Storage 写一个标的的几根合成 K 线（带变更日志，同步器按它拉增量）。
func baselineLivePut(fx *fixture, subject string, ordinal int, minutes ...int) {
	fx.st.putTag(subject, baselineRecalcTag, 0, minutes...)
	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	for i, row := range fx.st.rows[subject] {
		minute := int(row.at.Sub(t0) / time.Minute)
		row.close, row.volume = baselineRecalcValue(ordinal, minute)
		fx.st.rows[subject][i] = row
	}
}

func baselineLiveRun(t *testing.T) [][]string {
	t.Helper()
	fixed, variable := baselineRecalcMembers(t)
	fx := newFixture(t, fixed...)
	fx.set.Members = append(fx.set.Members, variable)
	sort.Slice(fx.set.Members, func(i, j int) bool { return fx.set.Members[i].Key < fx.set.Members[j].Key })
	for i, subject := range baselineLiveSubjects[:4] {
		baselineLivePut(fx, subject, i+1, minutesBetween(0, 9)...)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, baselineLiveSubjects[:4]))

	var records [][]string
	add := func(kind, subject, tag, dataTime, column, value string) {
		records = append(records, []string{"live", kind, subject, tag, dataTime, column, value})
	}
	stamp := func(at time.Time) string { return at.UTC().Format(time.RFC3339) }
	states := func(kind, period string, items []storageio.FactorState) {
		sorted := append([]storageio.FactorState(nil), items...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].MemberKey < sorted[j].MemberKey })
		for _, state := range sorted {
			add(kind, "", "", period, state.MemberKey+".status", state.Status)
			add(kind, "", "", period, state.MemberKey+".failed_subjects", strings.Join(state.FailedSubjects, ";"))
		}
	}
	run := func(minute int, universe, upstreamFailed []string) {
		fx.now = at(minute + 1)
		plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
			Set: fx.set.Set, Members: fx.set.Members, PeriodTime: at(minute),
			UniverseSubjects: universe, UpstreamFailed: upstreamFailed,
			CarryColumns: []string{"close", "volume"}, TriggerEventID: fmt.Sprintf("evt-%d", minute),
		})
		require.NoError(t, err)
		outcome, err := fx.runner.Run(context.Background(), plan)
		require.NoError(t, err)
		fx.data.Drain()
		period := stamp(at(minute))
		add("period", "", "", period, "status", outcome.Status)
		add("period", "", "", period, "failed_subjects", strings.Join(outcome.FailedSubjects, ";"))
		add("period", "", "", period, "rows_written", strconv.Itoa(outcome.RowsWritten))
		add("period", "", "", period, "warming", strconv.Itoa(outcome.Warming))
		states("member", period, outcome.Factors)
	}
	run(9, baselineLiveSubjects, nil)
	for i, subject := range baselineLiveSubjects[:3] {
		baselineLivePut(fx, subject, i+1, 10)
	}
	run(10, baselineLiveSubjects, nil)
	for i, subject := range baselineLiveSubjects[:4] {
		baselineLivePut(fx, subject, i+1, 11)
	}
	run(11, baselineLiveSubjects, nil)
	for i, subject := range baselineLiveSubjects[:4] {
		baselineLivePut(fx, subject, i+1, 12)
	}
	run(12, baselineLiveSubjects, []string{"L2"})
	for i, subject := range baselineLiveSubjects[:4] {
		baselineLivePut(fx, subject, i+1, 13)
	}
	run(13, baselineLiveSubjects[:4], nil)

	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	require.Len(t, fx.st.markers, 5, "每期上报一次周期标记")
	for _, marker := range fx.st.markers {
		period := stamp(time.Unix(marker.PeriodTime, 0))
		add("marker", "", "", period, "status", marker.Status)
		add("marker", "", "", period, "universe_subjects", strings.Join(marker.UniverseSubjects, ";"))
		add("marker", "", "", period, "failed_subjects", strings.Join(marker.FailedSubjects, ";"))
		states("marker_member", period, marker.Factors)
	}
	rows := make([]storageio.ResultRow, 0, len(fx.st.results))
	for _, row := range fx.st.results {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.SubjectID != b.SubjectID {
			return a.SubjectID < b.SubjectID
		}
		if a.SeriesTag != b.SeriesTag {
			return a.SeriesTag < b.SeriesTag
		}
		return a.DataTime.Before(b.DataTime)
	})
	for _, row := range rows {
		columns := make([]string, 0, len(row.Fields))
		for column := range row.Fields {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		for _, column := range columns {
			add("row", row.SubjectID, row.SeriesTag, stamp(row.DataTime), column, baselineRecalcText(row.Fields[column]))
		}
	}
	return records
}

func TestBaselineSingleSeriesLive(t *testing.T) {
	records := baselineLiveRun(t)
	require.Equal(t, records, baselineLiveRun(t), "两次运行的结果不同")
	stamp := func(minute int) string { return at(minute).UTC().Format(time.RFC3339) }
	find := func(kind, subject string, minute int, column string) (string, bool) {
		return baselineRecalcFind(records, "live", kind, subject, stamp(minute), column)
	}
	failed, _ := find("period", "", 9, "failed_subjects")
	require.Equal(t, "L5", failed, "第 9 分钟只有 L5 没有数据")
	failed, _ = find("period", "", 10, "failed_subjects")
	require.Equal(t, "L4;L5", failed, "第 10 分钟 L4 的 K 线还没到")
	failed, _ = find("period", "", 12, "failed_subjects")
	require.Equal(t, "L2;L5", failed, "第 12 分钟 L2 上游失败")
	if text, ok := find("row", "L1", 11, "mean"); !ok || text == "null" {
		t.Fatalf("L1 第 11 分钟的 mean 应算出：%q（有行 %v）", text, ok)
	}
	if text, ok := find("row", "L4", 11, "mean"); !ok || text == "null" {
		t.Fatalf("L4 第 11 分钟补到，mean 应算出：%q（有行 %v）", text, ok)
	}
	if text, ok := find("row", "L1", 11, "rank_partial"); !ok || text == "null" {
		t.Fatalf("允许部分全集的截面因子应在可用标的上算出：%q（有行 %v）", text, ok)
	}
	if text, ok := find("row", "L1", 12, "rank"); !ok || text != "null" {
		t.Fatalf("全集有标的缺数时严格全集的截面因子应跳过、写 null：%q（有行 %v）", text, ok)
	}
	if text, ok := find("row", "L1", 13, "rank"); !ok || text == "null" {
		t.Fatalf("全集齐全时严格全集的截面因子应算出：%q（有行 %v）", text, ok)
	}
	status, _ := find("period", "", 13, "status")
	require.Equal(t, "complete", status)
	for _, record := range records {
		if record[1] == "row" && record[2] == "L5" {
			t.Fatalf("L5 没有数据，不应写出结果行：%v", record)
		}
	}
	baselineRecalcCompare(t, baselineLivePath, records)
}
