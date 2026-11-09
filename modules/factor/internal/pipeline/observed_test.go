package pipeline

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

// historyData 是带 HistorySource 的数据层：读取 Available 与 HistorySubjects 的帧放进视图，目标区间的行只给 Available
// （与 localdata 的约定相同：历史截面的标的不产生结果行）。
type historyData struct {
	frameData
	history []string
	asked   int
}

func (d *historyData) HistorySubjects(_ context.Context, plan Plan) ([]string, error) {
	d.asked++
	available := map[string]struct{}{}
	for _, subject := range plan.Available {
		available[subject] = struct{}{}
	}
	out := []string{}
	for _, subject := range d.history {
		if _, ok := available[subject]; !ok {
			out = append(out, subject)
		}
	}
	return out, nil
}

func (d *historyData) Load(ctx context.Context, plan Plan) (DataResult, error) {
	result, err := d.frameData.Load(ctx, plan)
	if err != nil {
		return result, err
	}
	keep := map[string]struct{}{}
	for _, subject := range plan.Available {
		keep[subject] = struct{}{}
	}
	for subject := range result.Frames {
		if _, ok := keep[subject]; !ok {
			delete(result.Frames, subject)
		}
	}
	return result, nil
}

func observedStorage(target time.Time) (*memoryStorage, *historyData) {
	// BTC、ETH、SOL、OLD 的 close 基数为 100、200、300、400；OLD 在目标期之前下架（没有目标期的行）。
	store := newMemoryStorage([]string{"BTC", "ETH", "SOL", "OLD"}, target, 10)
	store.source["OLD"] = store.source["OLD"][:len(store.source["OLD"])-1]
	data := &historyData{history: []string{"OLD"}}
	data.read = func(ctx context.Context, plan Plan, start time.Time) (map[string]*storageio.Frame, error) {
		subjects := append(append([]string(nil), plan.Available...), plan.HistorySubjects...)
		return store.ReadWindow(ctx, storageio.ReadRequest{Subjects: subjects, Start: start, End: plan.TargetEnd, Columns: plan.CarryColumns})
	}
	return store, data
}

func rankMean(id string, observed bool) domain.FactorDef {
	spec := "output: ts_mean(cs_rank(close), 3)\n"
	if observed {
		spec = "cross_section_history: observed\n" + spec
	}
	def := domain.MustDefinition(id, id, spec)
	def.AllowPartialUniverse = true
	return def
}

// 端到端：observed 成员的历史截面含已下架的 OLD（第 8、9 分钟 BTC、ETH、SOL、OLD 名次 1/4、2/4、3/4、1），
// 目标期只有本期可用的三个标的；current 成员只用本期可用的标的。OLD 不写结果行；周期标记里两个成员的定义哈希不同。
func TestObservedPeriodEndToEnd(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	clock := periodclock.Continuous{}
	store, data := observedStorage(target)
	runner := NewRunner(store, data, clock, Config{ComputeWorkers: 2})
	plan, err := BuildLivePlan(clock, e2eLiveInput(target, []string{"BTC", "ETH", "SOL"}, []domain.FactorDef{rankMean("obs", true), rankMean("cur", false)}))
	require.NoError(t, err)

	outcome, err := runner.Run(context.Background(), plan)

	require.NoError(t, err)
	require.Equal(t, "complete", outcome.Status)
	require.Equal(t, 1, data.asked)
	rows := sortedRows(store)
	require.Len(t, rows, 3, "OLD 不写结果行")
	want := map[string][2]float64{
		"BTC": {(0.25 + 0.25 + 1.0/3) / 3, 1.0 / 3},
		"ETH": {(0.5 + 0.5 + 2.0/3) / 3, 2.0 / 3},
		"SOL": {(0.75 + 0.75 + 1) / 3, 1},
	}
	for _, row := range rows {
		require.InDelta(t, want[row.SubjectID][0], row.Fields["obs"], 1e-12, row.SubjectID)
		require.InDelta(t, want[row.SubjectID][1], row.Fields["cur"], 1e-12, row.SubjectID)
	}
	require.Len(t, store.markers, 1)
	hashes := map[string]string{}
	for _, state := range store.markers[0].Factors {
		hashes[state.MemberKey] = state.DefinitionHash
	}
	require.NotEqual(t, hashes["obs"], hashes["cur"])
	require.Equal(t, []string{"BTC", "ETH", "SOL"}, store.markers[0].UniverseSubjects)
}

// 同一任务混合两种口径：current 成员与只有 current 成员的任务逐位相同；只有 current 成员时不向数据层要历史标的。
func TestObservedMixedTaskKeepsCurrentBitwise(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	clock := periodclock.Continuous{}
	mixedStore, mixedData := observedStorage(target)
	mixed := NewRunner(mixedStore, mixedData, clock, Config{ComputeWorkers: 2})
	plan, err := BuildLivePlan(clock, e2eLiveInput(target, []string{"BTC", "ETH", "SOL"},
		[]domain.FactorDef{rankMean("obs", true), rankMean("cur", false), e2eMean()}))
	require.NoError(t, err)
	_, err = mixed.Run(context.Background(), plan)
	require.NoError(t, err)

	aloneStore, aloneData := observedStorage(target)
	alone := NewRunner(aloneStore, aloneData, clock, Config{ComputeWorkers: 2})
	plan, err = BuildLivePlan(clock, e2eLiveInput(target, []string{"BTC", "ETH", "SOL"}, []domain.FactorDef{rankMean("cur", false), e2eMean()}))
	require.NoError(t, err)
	_, err = alone.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Zero(t, aloneData.asked, "只有 current 成员时不扩大面板")

	mixedRows, aloneRows := sortedRows(mixedStore), sortedRows(aloneStore)
	require.Len(t, mixedRows, 3)
	require.Len(t, aloneRows, 3)
	for i := range aloneRows {
		for _, column := range []string{"cur", "mean"} {
			got, wantValue := mixedRows[i].Fields[column].(float64), aloneRows[i].Fields[column].(float64)
			require.Equal(t, math.Float64bits(wantValue), math.Float64bits(got), "%s %s", aloneRows[i].SubjectID, column)
		}
	}
}

// 内存预检按扩大后的序列数估算：历史截面的标的也计入。
func TestObservedMemoryPrecheckCountsHistorySubjects(t *testing.T) {
	target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
	plan := computePlan(target, []domain.FactorDef{rankMean("obs", true)}, []string{"BTC", "ETH", "SOL"})
	members := plan.Members
	three, err := EstimateChunkMemory(members, 3, 1, len(plan.CarryColumns))
	require.NoError(t, err)
	runner := newComputeRunner(Config{MaxMemory: three.Total()})
	require.NoError(t, runner.precheckMemory(plan))
	plan.HistorySubjects = []string{"OLD", "GONE"}
	require.ErrorIs(t, runner.precheckMemory(plan), ErrMemoryBudget)
}

// 目标时刻没有源行的标的不参与 observed 的截面：已下架的 OLD 用 delay、ts_last_valid 还能在目标期算出值，
// 但它这一刻没有源行，不能改变在册标的的名次；observed 与 current 在目标期截面上结果相同。
func TestObservedTargetSectionUsesPresentRowsOnly(t *testing.T) {
	for _, expression := range []string{"cs_rank(delay(close, 1))", "cs_rank(ts_last_valid(close, 5))"} {
		t.Run(expression, func(t *testing.T) {
			target := time.Date(2026, 10, 4, 0, 10, 0, 0, time.UTC)
			clock := periodclock.Continuous{}
			store, data := observedStorage(target)
			runner := NewRunner(store, data, clock, Config{ComputeWorkers: 1})
			observed := domain.MustDefinition("obs", "obs", "cross_section_history: observed\noutput: "+expression+"\n")
			observed.AllowPartialUniverse = true
			current := domain.MustDefinition("cur", "cur", "output: "+expression+"\n")
			current.AllowPartialUniverse = true
			plan, err := BuildLivePlan(clock, e2eLiveInput(target, []string{"BTC", "ETH", "SOL"}, []domain.FactorDef{observed, current}))
			require.NoError(t, err)
			_, err = runner.Run(context.Background(), plan)
			require.NoError(t, err)
			rows := sortedRows(store)
			require.Len(t, rows, 3)
			want := map[string]float64{"BTC": 1.0 / 3, "ETH": 2.0 / 3, "SOL": 1}
			for _, row := range rows {
				require.InDelta(t, want[row.SubjectID], row.Fields["cur"], 1e-12, row.SubjectID)
				require.Equal(t, row.Fields["cur"], row.Fields["obs"], row.SubjectID)
			}
		})
	}
}
