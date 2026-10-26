package localdata

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"github.com/mooyang-code/moox/modules/factor/internal/memadmit"
	"github.com/mooyang-code/moox/modules/factor/internal/periodclock"
	"github.com/mooyang-code/moox/modules/factor/internal/pipeline"
	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

var t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

type bar struct {
	at            time.Time
	close, volume float64
}

// fakeStorage 是内存里的 Storage：保存行、变更日志与列定义，同时充当管线写结果的 Store。
type fakeStorage struct {
	mu      sync.Mutex
	rows    map[string][]bar
	changes []storageio.Change
	results map[string]storageio.ResultRow
	markers []storageio.PeriodMarker
	// retention 是源数据集的保留期（如 "8d"）；空表示 forever。
	retention string
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{rows: map[string][]bar{}, results: map[string]storageio.ResultRow{}}
}

func (f *fakeStorage) put(subject string, base float64, minutes ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lo, hi := at(minutes[0]), at(minutes[0])
	for _, m := range minutes {
		b := bar{at: at(m), close: base + float64(m), volume: 10}
		replaced := false
		for i, r := range f.rows[subject] {
			if r.at.Equal(b.at) {
				f.rows[subject][i], replaced = b, true
			}
		}
		if !replaced {
			f.rows[subject] = append(f.rows[subject], b)
		}
		if b.at.Before(lo) {
			lo = b.at
		}
		if b.at.After(hi) {
			hi = b.at
		}
	}
	f.changes = append(f.changes, storageio.Change{Sequence: uint64(len(f.changes)) + 1, DatasetID: "spot",
		Ranges: []storageio.ChangeRange{{Subject: subject, Freq: "1m", Start: lo, End: hi}}})
}

func (f *fakeStorage) ListColumns(context.Context, string, string) ([]storageio.ColumnInfo, error) {
	return []storageio.ColumnInfo{
		{ColumnName: "close", ValueType: storageio.ColumnTypeDouble},
		{ColumnName: "volume", ValueType: storageio.ColumnTypeDouble},
	}, nil
}

// GetDataset 返回源数据集信息；retention 为空时按 forever（保留期不限制）。
func (f *fakeStorage) GetDataset(context.Context, string, string) (storageio.DatasetInfo, error) {
	retention := f.retention
	if retention == "" {
		retention = "forever"
	}
	return storageio.DatasetInfo{Retention: retention}, nil
}

func (f *fakeStorage) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*storageio.Frame{}
	for _, subject := range req.Subjects {
		frame := &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...)}
		rows := append([]bar(nil), f.rows[subject]...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
		for _, r := range rows {
			if r.at.Before(req.Start) || !r.at.Before(req.End) {
				continue
			}
			row := []any{r.at, ""}
			for _, c := range req.Columns {
				if c == "close" {
					row = append(row, r.close)
				} else {
					row = append(row, r.volume)
				}
			}
			frame.Rows = append(frame.Rows, row)
		}
		out[subject] = frame
	}
	return out, nil
}

func (f *fakeStorage) ReadDatasetChanges(_ context.Context, _, _ string, cursor *storageio.ChangeCursor, fromHead bool, _ int) (storageio.Changes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	head := storageio.ChangeCursor{NodeID: "n1", StoreID: "s1", Sequence: uint64(len(f.changes))}
	if fromHead {
		return storageio.Changes{Next: head}, nil
	}
	if cursor.StoreID != "s1" {
		return storageio.Changes{Gap: true, Next: head}, nil
	}
	out := storageio.Changes{Next: *cursor}
	for _, c := range f.changes {
		if c.Sequence > cursor.Sequence {
			out.Changes = append(out.Changes, c)
			out.Next.Sequence = c.Sequence
		}
	}
	return out, nil
}

func (f *fakeStorage) EarliestPeriod(_ context.Context, _, _, _ string, subjects, _ []string) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best time.Time
	for _, s := range subjects {
		for _, r := range f.rows[s] {
			if best.IsZero() || r.at.Before(best) {
				best = r.at
			}
		}
	}
	return best, !best.IsZero(), nil
}

func (f *fakeStorage) DatasetColumns(context.Context, string, string) ([]string, error) {
	return []string{"close", "volume"}, nil
}
func (f *fakeStorage) WriteRows(_ context.Context, _, _, _ string, rows []storageio.ResultRow) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range rows {
		f.results[r.SubjectID+r.DataTime.Format(time.RFC3339)] = r
	}
	return nil
}
func (f *fakeStorage) ReportComputed(_ context.Context, m storageio.PeriodMarker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.markers = append(f.markers, m)
	return nil
}
func (f *fakeStorage) ComputedExists(context.Context, string, string, string, int64) (bool, error) {
	return false, nil
}

type fixture struct {
	st     *fakeStorage
	data   *Data
	runner *pipeline.Runner
	set    domain.EngineTask
	now    time.Time
}

func newFixture(t *testing.T, factors ...domain.FactorDef) *fixture {
	t.Helper()
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	st := newFakeStorage()
	fx := &fixture{st: st, now: at(10)}
	fx.data = New(Config{Store: store, Storage: st, Clock: periodclock.Continuous{}, Admit: memadmit.New(0), Workers: 2, BatchSubjects: 2, Now: func() time.Time { return fx.now }})
	fx.runner = pipeline.NewRunner(st, fx.data, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1})
	fx.set = domain.EngineTask{
		Set: domain.FactorTask{TaskID: "set1", SpaceID: "crypto", SourceDatasetID: "spot", Freq: "1m",
			ResultDatasetID: "factor_spot", Status: domain.TaskStatusEnabled},
		Members: domain.AsMembers(factors...),
	}
	return fx
}

func (fx *fixture) live(t *testing.T, target time.Time, subjects []string) pipeline.Outcome {
	t.Helper()
	plan, err := pipeline.BuildLivePlan(periodclock.Continuous{}, pipeline.LiveInput{
		Set: fx.set.Set, Members: fx.set.Members, PeriodTime: target, Universe: subjects, CarryColumns: []string{"close", "volume"}, TriggerEventID: "evt",
	})
	require.NoError(t, err)
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	fx.data.Drain() // 热路径的写盘与计算并行，断言磁盘内容之前先等它落完
	return outcome
}

func mean3() domain.FactorDef {
	return domain.MustDefinition("mean", "mean", "outputs:\n  mean3: ts_mean(close, 3)\n")
}

func (fx *fixture) result(subject string, m int) storageio.ResultRow {
	fx.st.mu.Lock()
	defer fx.st.mu.Unlock()
	return fx.st.results[subject+at(m).Format(time.RFC3339)]
}

type sink struct {
	mu      sync.Mutex
	entries []pipeline.GapEntry
}

func (s *sink) RecordGaps(_ context.Context, e []pipeline.GapEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, e...)
	return nil
}

func mean5() domain.FactorDef {
	return domain.MustDefinition("mean5", "mean5", "outputs:\n  mean5: ts_mean(close, 5)\n")
}

func isFilled() domain.FactorDef {
	return domain.MustDefinition("isf", "isf", "outputs:\n  isf: is_filled + 0\n")
}

// 窗口载入完成之前：实时周期不算、标的按预热处理；Prepare 之后补齐本地并载入窗口，之后每期只拉增量。
func TestLivePeriodsWarmThenIncrementalSync(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC", "ETH"}
	for _, s := range subjects {
		fx.st.put(s, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}

	outcome := fx.live(t, at(9), subjects)
	require.Equal(t, 2, outcome.Warming, "还没有预载：整个任务预热中")
	require.Empty(t, fx.data.WindowStatuses())

	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	status := fx.data.WindowStatuses()
	require.Len(t, status, 1)
	require.Equal(t, pipeline.WarmupStatus{TaskID: "set1", State: pipeline.WarmupReady, WarmSubjects: 2, ExpectedSubjects: 2}, status[0])
	// 本地只拉了回看窗口（3 根），不是全部历史。
	b, ok, err := fx.data.Store().Bounds(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}, "BTC")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(7), b.Earliest)
	require.Equal(t, at(9), b.Latest)

	outcome = fx.live(t, at(9), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.Equal(t, 0, outcome.Warming)
	require.InDelta(t, (107+108+109)/3.0, fx.result("BTC", 9).Fields["mean3"], 1e-9)
	require.InDelta(t, 109.0, fx.result("BTC", 9).Fields["close"], 1e-9, "携带列从本地行写出")

	// 下一期：Storage 来了新的一根，同步只拉增量，窗口追加。
	for _, s := range subjects {
		fx.st.put(s, 100, 10)
	}
	outcome = fx.live(t, at(10), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (108+109+110)/3.0, fx.result("ETH", 10).Fields["mean3"], 1e-9)
	b, _, _ = fx.data.Store().Bounds(seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}, "ETH")
	require.Equal(t, at(10), b.Latest)
}

// 目标周期的数据还没到：该标的本期按失败处理并记入账本，其他标的照常；数据补上之后下一期自然追平。
func TestLiveSubjectWithoutTargetBarFailsAndRecordsLedger(t *testing.T) {
	fx := newFixture(t, mean3())
	ledger := &sink{}
	fx.runner = pipeline.NewRunner(fx.st, fx.data, periodclock.Continuous{}, pipeline.Config{ComputeWorkers: 1, GapSink: ledger})
	subjects := []string{"BTC", "ETH"}
	for _, s := range subjects {
		fx.st.put(s, 100, 5, 6, 7, 8, 9)
	}
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))

	fx.st.put("BTC", 100, 10) // ETH 的第 10 根还没到
	outcome := fx.live(t, at(10), subjects)
	require.Equal(t, "degraded", outcome.Status)
	require.Equal(t, []string{"ETH"}, outcome.FailedSubjects)
	require.NotZero(t, fx.result("BTC", 10).Fields["mean3"])
	require.Len(t, ledger.entries, 1)
	require.Equal(t, "ETH", ledger.entries[0].Subject)
	require.Equal(t, pipeline.SkipReasonUpstream, ledger.entries[0].Reason)

	// 第 11 根两个标的都到了：ETH 缺的第 10 根按规则填充，本期正常。
	for _, s := range subjects {
		fx.st.put(s, 100, 11)
	}
	outcome = fx.live(t, at(11), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (109+109+111)/3.0, fx.result("ETH", 11).Fields["mean3"], 1e-9, "t10 被填充成上一期 close")
}

// 交易所缺的周期写成缺失行并带标记：因子默认把填充值当正常值，用 is_filled 可以区分。
func TestFilledRowsAreMarkedAndUsableInFactors(t *testing.T) {
	fx := newFixture(t, mean3(), isFilled())
	subjects := []string{"BTC"}
	fx.st.put("BTC", 100, 5, 6, 7, 8, 9, 11) // 缺第 10 根，之后的第 11 根到了
	fx.now = at(10)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	// Prepare 只拉到最新已收盘的一根（第 9 根）；第 11 根在实时周期里补进来，t10 被填充。
	outcome := fx.live(t, at(11), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (109+109+111)/3.0, fx.result("BTC", 11).Fields["mean3"], 1e-9)
	require.Equal(t, 0.0, fx.result("BTC", 11).Fields["isf"])

	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(10), TargetEnd: at(11),
		Expected: subjects, Available: subjects, CarryColumns: []string{"close", "volume"}, WriteCarry: true}
	_, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, 1.0, fx.result("BTC", 10).Fields["isf"], "填充行的 is_filled 为 1")
	require.InDelta(t, 109.0, fx.result("BTC", 10).Fields["close"], 1e-9, "close 取上一期 close")
	require.InDelta(t, 0.0, fx.result("BTC", 10).Fields["volume"], 1e-9, "量类字段填 0")
}

// 补算只读本地存储，不碰 Storage：回看窗口完整的周期正常算出。
func TestRecalcReadsLocalStoreOnly(t *testing.T) {
	fx := newFixture(t, mean5())
	subjects := []string{"BTC"}
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))

	fx.st.mu.Lock()
	fx.st.rows = map[string][]bar{} // Storage 里的数据没了：补算仍然能算（读本地）
	fx.st.mu.Unlock()
	plan := pipeline.Plan{Mode: pipeline.ModeRecalc, Set: fx.set.Set, Members: fx.set.Members, TargetStart: at(9), TargetEnd: at(10),
		Expected: subjects, Available: subjects, CarryColumns: []string{"close", "volume"}, WriteCarry: true}
	outcome, err := fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, 107.0, fx.result("BTC", 9).Fields["mean5"], 1e-9)

	// 本地没有的标的：失败，不去 Storage 补。
	plan.Expected, plan.Available = []string{"BTC", "SOL"}, []string{"BTC", "SOL"}
	outcome, err = fx.runner.Run(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, []string{"SOL"}, outcome.FailedSubjects)
}

// Storage 修正了历史数据：Follow 整段替换本地并重新载入窗口，同时记下失效重算待办；内容相同的变更不重复记。
func TestFollowAppliesCorrectionAndRecordsPending(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC"}
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}

	fx.st.put("BTC", 500, 8) // 历史修正
	res, err := fx.data.Follow(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, res.Changed)
	require.Empty(t, res.Gapped)
	pending, err := fx.data.Store().Pending(src)
	require.NoError(t, err)
	require.Equal(t, []seriesstore.PendingSpan{{Subject: "BTC", From: at(8), To: at(9), Reason: "storage_change"}}, pending)

	// 窗口已经是修正后的值：下一期用它计算（t9 的均值含修正后的 t8=508）。
	fx.st.put("BTC", 100, 10)
	outcome := fx.live(t, at(10), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (508+109+110)/3.0, fx.result("BTC", 10).Fields["mean3"], 1e-9)

	// 同样内容再被写一次：不改变本地、不新增待办。
	require.NoError(t, fx.data.Store().ClearPending(src, pending))
	fx.st.put("BTC", 100, 10)
	res, err = fx.data.Follow(context.Background())
	require.NoError(t, err)
	require.Zero(t, res.Changed)
	pending, _ = fx.data.Store().Pending(src)
	require.Empty(t, pending)
}

// 变更日志断档：Prepare 把数据源判为未就绪并给出明确错误，实时周期继续按预热处理；对账之后恢复。
func TestChangeGapBlocksSourceUntilReconciled(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC"}
	fx.st.put("BTC", 100, 5, 6, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}

	// 位点的存储身份对不上（Storage 重建）。
	require.NoError(t, fx.data.Store().SetSyncCursor(src, []byte("n1\x00other-store\x005")))
	require.ErrorIs(t, fx.data.Prepare(context.Background(), fx.set, subjects), ErrChangeGap)
	require.ErrorIs(t, fx.data.Blocked(src), ErrChangeGap)
	require.Equal(t, pipeline.WarmupWarming, fx.data.WindowStatuses()[0].State)
	outcome := fx.live(t, at(9), subjects)
	require.Equal(t, 1, outcome.Warming)

	// Follow 也报告断档。
	res, err := fx.data.Follow(context.Background())
	require.NoError(t, err)
	require.Equal(t, []seriesstore.Source{src}, res.Gapped)

	// 对账：重建基线，窗口重新载入，数据源恢复就绪。
	summary, err := fx.data.Reconcile(context.Background(), src, nil, nil)
	require.NoError(t, err)
	require.Equal(t, 1, summary.Subjects)
	require.NoError(t, fx.data.Blocked(src))
	require.Equal(t, pipeline.WarmupReady, fx.data.WindowStatuses()[0].State)
	outcome = fx.live(t, at(9), subjects)
	require.Equal(t, 0, outcome.Warming)
}

// 窗口内存超出 window.max_memory：拒绝启用并给出明确原因。
func TestPrepareRejectsWindowOverMemoryLimit(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.data.cfg.WindowMax = 100
	fx.st.put("BTC", 100, 7, 8, 9)
	err := fx.data.Prepare(context.Background(), fx.set, []string{"BTC"})
	require.ErrorContains(t, err, "window.max_memory")
	require.Equal(t, pipeline.WarmupWarming, fx.data.WindowStatuses()[0].State, "没有就绪")
}

// 导入写本地：数据源已准备好时窗口随之更新。
func TestWriteLocalReloadsWindow(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.put("BTC", 100, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"BTC"}))
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields, _, _ := fx.data.Store().Schema(src)

	rows := seriesstore.NewColumns(fields, 1)
	rows.AppendRow(at(8), "", false, seriesstore.Float(900), seriesstore.Float(10))
	_, err := fx.data.WriteLocal(context.Background(), src, []seriesstore.SubjectWrite{{Subject: "BTC", From: at(8), To: at(9), Rows: rows}}, seriesstore.WriteOptions{FillGaps: true})
	require.NoError(t, err)
	fx.st.put("BTC", 100, 10)
	// 窗口里 t8 已经是导入的值（本地写入的是 close 与 volume，Storage 上的 t8 不同也不会被覆盖，除非有变更日志）。
	view := fx.data.st(src).win.Load().Acquire([]string{"BTC"}, at(9).Unix())
	idx, _ := view.FieldIndex("close")
	require.Equal(t, 900.0, view.Series("BTC")[0].Cols[idx][1])
}

// 追赶一个早于窗口最新一根的周期：窗口只保留到最新为止的回看根数，截止该周期的回看不全，要改读本地磁盘，
// 不能用不完整的窗口算出看似正常的值（mean3 在 t8 需要 t6、t7、t8）。
func TestLiveCatchUpOlderPeriodReadsDiskNotTruncatedWindow(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC"}
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10)
	fx.now = at(11)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	// 本地磁盘里有更早的历史（例如导入的），窗口里没有。
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields, ok, err := fx.data.Store().Schema(src)
	require.NoError(t, err)
	require.True(t, ok)
	hist := seriesstore.NewColumns(fields, 8)
	for m := 0; m < 8; m++ {
		values := make([]seriesstore.Value, len(fields))
		for i, f := range fields {
			values[i] = seriesstore.Float(100 + float64(m))
			if f.Name == "volume" {
				values[i] = seriesstore.Float(10)
			}
		}
		hist.AppendRow(at(m), "", false, values...)
	}
	_, err = fx.data.Store().WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: "BTC", From: at(0), To: at(8), Rows: hist}, seriesstore.WriteOptions{})
	require.NoError(t, err)

	outcome := fx.live(t, at(8), subjects)
	require.Equal(t, "complete", outcome.Status)
	require.InDelta(t, (106+107+108)/3.0, fx.result("BTC", 8).Fields["mean3"], 1e-9)
}

// 回看变长后，本地不足的标的在 Prepare 时从 Storage 往前补齐（设计 5.2 启动加载，决定 24）；
// 新上市的标的（本地最早一根就是 Storage 首根）不需要补，覆盖检查也放行。
func TestPrepareBackfillsShortSubjectsAndCoverageAllowsNewListings(t *testing.T) {
	fx := newFixture(t, mean3())
	subjects := []string{"BTC", "NEW"}
	fx.st.put("BTC", 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	fx.st.put("NEW", 100, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	short, err := fx.data.Coverage(context.Background(), src)
	require.NoError(t, err)
	require.Empty(t, short, "回看 3 根，本地各有 3 根")

	fx.set.Members = domain.AsMembers(mean5()) // 回看变长，本地原来只有 3 根
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, subjects))
	short, err = fx.data.Coverage(context.Background(), src)
	require.NoError(t, err)
	require.Empty(t, short, "BTC 已从 Storage 补到 5 根，NEW 是新上市")
	b, ok, err := fx.data.Store().Bounds(src, "BTC")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(5), b.Earliest, "往前补到 5 根")
	n, ok, err := fx.data.Store().Bounds(src, "NEW")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(7), n.Earliest, "新上市的标的 Storage 里也只有这些，没有可补的")
}

// 回看数量 N 超过数据集保留期能容纳的根数时，Storage 的首根可能是被保留期截断的，新上市豁免不成立（设计 6.2）。
func TestCoverageDoesNotExemptWhenLookbackExceedsRetention(t *testing.T) {
	fx := newFixture(t, mean3())
	fx.st.retention = "1h" // 1m 频率下只能容纳 60 根
	fx.data.cfg.Lookback = func(seriesstore.Source) int { return 100 }
	fx.set.Members = domain.AsMembers(mean3())
	fx.st.put("NEW", 100, 7, 8, 9)
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"NEW"}))
	src := seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	short, err := fx.data.Coverage(context.Background(), src)
	require.NoError(t, err)
	require.Len(t, short, 1, "N=100 超过保留期容量 60 根，不能把不足 N 根当作新上市")
	require.Equal(t, 100, short[0].Want)

	// 保留期放宽到能容纳 N 之后豁免成立。
	fx.st.retention = "7d"
	require.NoError(t, fx.data.Prepare(context.Background(), fx.set, []string{"NEW"}))
	short, err = fx.data.Coverage(context.Background(), src)
	require.NoError(t, err)
	require.Empty(t, short)
}
