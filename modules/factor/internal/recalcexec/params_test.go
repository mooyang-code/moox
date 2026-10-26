package recalcexec

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/stretchr/testify/require"
)

type fakeParamReader struct {
	rows map[pipeline.RowKey]map[string]map[string]float64
	err  error
}

func (f *fakeParamReader) ReadRowParams(_ context.Context, _ domain.FactorTask, _ []string, start, end time.Time) (map[pipeline.RowKey]map[string]map[string]float64, error) {
	out := map[pipeline.RowKey]map[string]map[string]float64{}
	for key, members := range f.rows {
		if !key.At.Before(start) && key.At.Before(end) {
			out[key] = members
		}
	}
	return out, f.err
}

func paramSelection(t *testing.T, subjects ...string) (Selection, string) {
	t.Helper()
	def := domain.MustDefinition("pmean", "pmean", "params:\n  window: {type: int, min: 2, max: 50, default: 20}\noutput: ts_mean(close, window)\n")
	member := domain.MemberDef{Key: "pmean__var", Factor: def, Kind: domain.MemberKindVar, Params: map[string]float64{"window": 20}}
	selection := executorSelection(subjects...)
	selection.Members = []domain.MemberDef{member}
	return selection, member.Key
}

func windowParam(minute func(int) time.Time, at int, key string, window float64) (pipeline.RowKey, map[string]map[string]float64) {
	return pipeline.RowKey{Subject: "BTC", At: minute(at), Tag: ""}, map[string]map[string]float64{key: {"window": window}}
}

// 补算按已有行的 __params 分参数段：段内每期每个标的每个被选成员的参数都相同；
// 已有行不存在的期次用成员自己的参数，并记为按默认参数新补。
func TestRunChunkSplitsByRowParams(t *testing.T) {
	window := testWindow(10) // 00:00～00:10
	at := func(minute int) time.Time { return window.Start.Add(time.Duration(minute) * time.Minute) }
	selection, key := paramSelection(t, "BTC")
	reader := &fakeParamReader{rows: map[pipeline.RowKey]map[string]map[string]float64{}}
	for minute, value := range map[int]float64{2: 30, 3: 30, 4: 30, 5: 30, 6: 40} {
		rowKey, members := windowParam(at, minute, key, value)
		reader.rows[rowKey] = members
	}
	// 真实管线在补算里总会给出逐项结果：这里让每个期次都写出了值。
	runner := &recordingRunner{onRun: func(_ context.Context, plan pipeline.Plan) (pipeline.Outcome, error) {
		items := map[pipeline.ItemKey]pipeline.ItemStatus{}
		for period := plan.TargetStart; period.Before(plan.TargetEnd); period = period.Add(time.Minute) {
			items[pipeline.ItemKey{Member: key, Subject: "BTC", Period: period.UTC()}] = pipeline.ItemWritten
		}
		return pipeline.Outcome{Status: "complete", Items: items}, nil
	}}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(100), WithParamReader(reader))

	outcome, err := executor.RunChunk(context.Background(), selection, window.Start, window.End)
	require.NoError(t, err)

	type segment struct {
		start, end int
		window     float64
		bases      int
	}
	var got []segment
	for _, plan := range runner.snapshot() {
		item := segment{start: int(plan.TargetStart.Sub(window.Start) / time.Minute), end: int(plan.TargetEnd.Sub(window.Start) / time.Minute), bases: len(plan.BaseParams)}
		item.window = plan.Params[key].Value("window", "BTC")
		got = append(got, item)
	}
	require.Equal(t, []segment{
		{0, 2, 20, 0},
		{2, 6, 30, 4},
		{6, 7, 40, 1},
		{7, 10, 20, 0},
	}, got)
	var filled []int
	for _, period := range outcome.DefaultFilled[key] {
		filled = append(filled, int(period.Sub(window.Start)/time.Minute))
	}
	sort.Ints(filled)
	require.Equal(t, []int{0, 1, 7, 8, 9}, filled, "没有已有行的期次记为按默认参数新补")
}

// 行存在但 __params 里没有这个成员，也记为新补；按标的的参数不同时用覆盖还原。
func TestParamSegmentRestoresPerSubjectAndDetectsMissingMember(t *testing.T) {
	window := testWindow(4)
	at := func(minute int) time.Time { return window.Start.Add(time.Duration(minute) * time.Minute) }
	selection, key := paramSelection(t, "BTC", "ETH")
	rows := map[pipeline.RowKey]map[string]map[string]float64{}
	for minute := 0; minute < 4; minute++ {
		rows[pipeline.RowKey{Subject: "BTC", At: at(minute)}] = map[string]map[string]float64{key: {"window": 30}}
		rows[pipeline.RowKey{Subject: "ETH", At: at(minute)}] = map[string]map[string]float64{key: {"window": 20}}
	}
	// 第 3 期 ETH 的行里没有这个成员。
	rows[pipeline.RowKey{Subject: "ETH", At: at(3)}] = map[string]map[string]float64{"other__var": {"span": 5}}

	seg := paramSegment(selection, newParamRows(rows), window.Start, window.End, time.Minute)
	require.Equal(t, at(3), seg.end, "第 3 期的参数与前面不同，分段在这里截断")
	require.Equal(t, 30.0, seg.params[key].Value("window", "BTC"))
	require.Equal(t, 20.0, seg.params[key].Value("window", "ETH"))
	require.Empty(t, seg.defaultFilled[key])

	last := paramSegment(selection, newParamRows(rows), at(3), window.End, time.Minute)
	require.Equal(t, window.End, last.end)
	require.Equal(t, []time.Time{at(3)}, last.defaultFilled[key])
	require.Equal(t, 30.0, last.params[key].Value("window", "BTC"))
	require.Equal(t, 20.0, last.params[key].Value("window", "ETH"), "缺这个成员的标的取成员自己的参数")
}

// 没有设置读取方时，所有期次都按成员自己的参数计算一个段，不记录新补的期次。
func TestRunChunkWithoutParamReaderComputesOneSegment(t *testing.T) {
	window := testWindow(5)
	selection, key := paramSelection(t, "BTC")
	runner := &recordingRunner{}
	executor := NewExecutor(runner, WithClock(periodclock.Continuous{}), WithChunkPeriods(100))
	outcome, err := executor.RunChunk(context.Background(), selection, window.Start, window.End)
	require.NoError(t, err)
	require.Len(t, runner.snapshot(), 1)
	require.Empty(t, outcome.DefaultFilled[key])
}
