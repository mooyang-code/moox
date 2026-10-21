package seriessync

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
	"github.com/stretchr/testify/require"
)

var (
	src    = seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields = []seriesstore.Field{
		{Name: "close", Type: seriesstore.TypeFloat64, Fill: seriesstore.FillPrevClose},
		{Name: "volume", Type: seriesstore.TypeFloat64, Fill: seriesstore.FillZero},
	}
	t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

type remoteRow struct {
	at            time.Time
	tag           string
	close, volume float64
}

// fakeStorage 是内存里的 Storage：按标的保存行，写入时追加变更日志。
type fakeStorage struct {
	mu       sync.Mutex
	rows     map[string][]remoteRow
	changes  []storageio.Change
	trimmed  uint64
	readErr  map[string]error // 按标的注入读取失败
	reads    int
	nodeID   string
	storeID  string
	earliest map[string]time.Time // 覆盖 Storage 的保留起点（模拟保留期清理）
}

func newFakeStorage() *fakeStorage {
	return &fakeStorage{rows: map[string][]remoteRow{}, readErr: map[string]error{}, nodeID: "n1", storeID: "s1", earliest: map[string]time.Time{}}
}

// put 写入若干分钟的行（close=分钟序号+base），并记一条变更日志。
func (f *fakeStorage) put(subject string, base float64, minutes ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	lo, hi := at(minutes[0]), at(minutes[0])
	for _, m := range minutes {
		row := remoteRow{at: at(m), close: base + float64(m), volume: float64(m)}
		replaced := false
		for i, r := range f.rows[subject] {
			if r.at.Equal(row.at) && r.tag == row.tag {
				f.rows[subject][i], replaced = row, true
			}
		}
		if !replaced {
			f.rows[subject] = append(f.rows[subject], row)
		}
		if row.at.Before(lo) {
			lo = row.at
		}
		if row.at.After(hi) {
			hi = row.at
		}
	}
	f.changes = append(f.changes, storageio.Change{Sequence: uint64(len(f.changes)) + 1, DatasetID: "spot",
		Ranges: []storageio.ChangeRange{{Subject: subject, Freq: "1m", Start: lo, End: hi}}})
}

func (f *fakeStorage) drop(subject string, minute int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var kept []remoteRow
	for _, r := range f.rows[subject] {
		if !r.at.Equal(at(minute)) {
			kept = append(kept, r)
		}
	}
	f.rows[subject] = kept
	f.changes = append(f.changes, storageio.Change{Sequence: uint64(len(f.changes)) + 1, DatasetID: "spot",
		Ranges: []storageio.ChangeRange{{Subject: subject, Freq: "1m", Start: at(minute), End: at(minute)}}})
}

func (f *fakeStorage) ReadWindow(_ context.Context, req storageio.ReadRequest) (map[string]*storageio.Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads++
	out := map[string]*storageio.Frame{}
	for _, subject := range req.Subjects {
		if err := f.readErr[subject]; err != nil {
			return nil, err
		}
		frame := &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, req.Columns...)}
		rows := append([]remoteRow(nil), f.rows[subject]...)
		sort.Slice(rows, func(i, j int) bool { return rows[i].at.Before(rows[j].at) })
		for _, r := range rows {
			if r.at.Before(req.Start) || !r.at.Before(req.End) {
				continue
			}
			if e, ok := f.earliest[subject]; ok && r.at.Before(e) {
				continue
			}
			row := []any{r.at, r.tag}
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

func (f *fakeStorage) head() uint64 { return uint64(len(f.changes)) }

func (f *fakeStorage) ReadDatasetChanges(_ context.Context, _, _ string, cursor *storageio.ChangeCursor, fromHead bool, limit int) (storageio.Changes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	next := storageio.ChangeCursor{NodeID: f.nodeID, StoreID: f.storeID, Sequence: f.head()}
	if fromHead {
		return storageio.Changes{Next: next}, nil
	}
	if cursor.NodeID != f.nodeID || cursor.StoreID != f.storeID || cursor.Sequence > f.head() || cursor.Sequence < f.trimmed {
		return storageio.Changes{Gap: true, Next: next}, nil
	}
	out := storageio.Changes{}
	last := cursor.Sequence
	for _, c := range f.changes {
		if c.Sequence <= cursor.Sequence {
			continue
		}
		if limit > 0 && len(out.Changes) >= limit {
			out.HasMore = true
			break
		}
		out.Changes = append(out.Changes, c)
		last = c.Sequence
	}
	out.Next = storageio.ChangeCursor{NodeID: f.nodeID, StoreID: f.storeID, Sequence: last}
	return out, nil
}

func (f *fakeStorage) EarliestPeriod(_ context.Context, _, _, _ string, subjects, _ []string) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best time.Time
	for _, s := range subjects {
		for _, r := range f.rows[s] {
			if e, ok := f.earliest[s]; ok && r.at.Before(e) {
				continue
			}
			if best.IsZero() || r.at.Before(best) {
				best = r.at
			}
		}
	}
	return best, !best.IsZero(), nil
}

func newSyncer(t *testing.T, st Storage) (*Syncer, Spec) {
	t.Helper()
	store, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return &Syncer{Store: store, Storage: st, Workers: 2, BatchSubjects: 2}, Spec{Source: src, Fields: fields, Lookback: 5}
}

func closes(t *testing.T, s *Syncer, subject string) map[int]float64 {
	t.Helper()
	got, err := s.Store.ReadRange(context.Background(), src, []string{subject}, at(0), at(1000))
	require.NoError(t, err)
	out := map[int]float64{}
	c := got[subject]
	for i := 0; c != nil && i < c.Len(); i++ {
		out[int((c.Times[i]-t0.Unix())/60)] = c.ValueAt(0, i).F
	}
	return out
}

func TestPullInitialFetchesLookbackThenIncrements(t *testing.T) {
	st := newFakeStorage()
	for _, subject := range []string{"A", "B", "C"} {
		st.put(subject, 100, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	}
	s, spec := newSyncer(t, st)

	// 本地没有：拉最近 5 根（[5, 10)）。
	res, err := s.Pull(context.Background(), spec, []string{"A", "B", "C"}, at(10))
	require.NoError(t, err)
	require.Len(t, res.Written, 3)
	require.Empty(t, res.Failed)
	require.Equal(t, map[int]float64{5: 105, 6: 106, 7: 107, 8: 108, 9: 109}, closes(t, s, "A"))
	require.Equal(t, at(5), res.Written["A"].Earliest)
	require.True(t, res.Written["A"].PrevLatest.IsZero())

	// 之后只拉增量：Storage 又来了 10 和 11。
	st.put("A", 100, 10, 11)
	before := st.reads
	res, err = s.Pull(context.Background(), spec, []string{"A", "B", "C"}, at(12))
	require.NoError(t, err)
	require.Equal(t, 2, st.reads-before, "A 与 B、C 起点不同，各自一批")
	require.Equal(t, 1, len(res.Written), "B、C 没有新行，不写入")
	require.Equal(t, at(9), res.Written["A"].PrevLatest)
	require.Equal(t, at(11), res.Written["A"].Latest)
	require.Equal(t, 111.0, closes(t, s, "A")[11])

	// 已经是最新：不读取。
	before = st.reads
	res, err = s.Pull(context.Background(), spec, []string{"A"}, at(12))
	require.NoError(t, err)
	require.Equal(t, 0, st.reads-before)
	require.Empty(t, res.Written)
}

func TestPullFillsMissingPeriodsAndReportsFailures(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2)
	st.put("B", 100, 0, 1, 2)
	s, spec := newSyncer(t, st)
	s.BatchSubjects = 1 // 读取失败按批计：一批一个标的，A 不受 B 影响
	_, err := s.Pull(context.Background(), spec, []string{"A", "B"}, at(3))
	require.NoError(t, err)

	// A 缺 t3 t4，t5 到了：t3 t4 按 prev_close 填充。B 的读取失败。
	st.put("A", 100, 5)
	st.put("B", 100, 3)
	st.readErr["B"] = errors.New("storage unavailable")
	res, err := s.Pull(context.Background(), spec, []string{"A", "B"}, at(6))
	require.NoError(t, err)
	require.Equal(t, []string{"B"}, res.Failed)
	require.Equal(t, 2, res.Written["A"].Filled)
	require.Equal(t, map[int]float64{0: 100, 1: 101, 2: 102, 3: 102, 4: 102, 5: 105}, closes(t, s, "A"))
	require.Equal(t, map[int]float64{0: 100, 1: 101, 2: 102}, closes(t, s, "B"), "失败的标的不写入")
}

func TestBaselineAndFollowAppliesCorrectionsOnly(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2, 3, 4)
	st.put("B", 100, 0, 1, 2, 3, 4)
	s, spec := newSyncer(t, st)

	_, err := s.Follow(context.Background(), spec)
	require.ErrorIs(t, err, ErrNoBaseline)

	// 先取位点、再拉数据（设计 5.1）。
	cursor, err := s.Baseline(context.Background(), src)
	require.NoError(t, err)
	require.Equal(t, uint64(2), cursor.Sequence)
	_, err = s.Pull(context.Background(), spec, []string{"A"}, at(5))
	require.NoError(t, err)

	// 1）新数据（本地最新之后）：Follow 不处理，Pull 补；2）B 本地没有：忽略；3）A 的历史被修正。
	st.put("A", 100, 5)
	st.put("B", 100, 6)
	st.put("A", 500, 2)
	res, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.False(t, res.Gap)
	require.Len(t, res.Applied, 1)
	require.Equal(t, Span(at(2), at(3)), res.Applied["A"].Changed)
	require.Equal(t, 502.0, closes(t, s, "A")[2])
	require.NotContains(t, closes(t, s, "A"), 5, "新数据不由 Follow 写入")
	cur, ok, err := s.Cursor(src)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, st.head(), cur.Sequence)
	pending, err := s.Store.Pending(src)
	require.NoError(t, err)
	require.Equal(t, []seriesstore.PendingSpan{{Subject: "A", From: at(2), To: at(3), Reason: "storage_change"}}, pending)
	require.NoError(t, s.Store.ClearPending(src, pending))

	// 之后 Pull 补上新数据，对应的变更日志再被读到时与本地相同：不写入、不记失效重算。
	_, err = s.Pull(context.Background(), spec, []string{"A"}, at(6))
	require.NoError(t, err)
	st.put("A", 100, 5) // 重复写入同样的内容
	res, err = s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.Empty(t, res.Applied)
	pending, err = s.Store.Pending(src)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestFollowDetectsDeletionAndFillsIt(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2, 3)
	s, spec := newSyncer(t, st)
	_, err := s.Baseline(context.Background(), src)
	require.NoError(t, err)
	_, err = s.Pull(context.Background(), spec, []string{"A"}, at(4))
	require.NoError(t, err)

	st.drop("A", 2) // Storage 删掉了 t2：本地该周期改成填充行。
	res, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.Len(t, res.Applied, 1)
	require.Equal(t, map[int]float64{0: 100, 1: 101, 2: 101, 3: 103}, closes(t, s, "A"))
}

func TestFollowPagesAndMergesAdjacentChanges(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2, 3, 4, 5)
	s, spec := newSyncer(t, st)
	spec.Lookback = 100
	s.ChangePageSize = 2
	_, err := s.Baseline(context.Background(), src)
	require.NoError(t, err)
	_, err = s.Pull(context.Background(), spec, []string{"A"}, at(6))
	require.NoError(t, err)

	for _, m := range []int{1, 2, 3, 0, 5} {
		st.put("A", 900, m)
	}
	res, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	got := closes(t, s, "A")
	require.Equal(t, 900.0, got[0])
	require.Equal(t, 903.0, got[3])
	require.Equal(t, 104.0, got[4], "没有被修正的保持不变")
	require.Equal(t, 905.0, got[5])
	require.Contains(t, res.Applied, "A")
	cur, _, _ := s.Cursor(src)
	require.Equal(t, st.head(), cur.Sequence, "分页读完后位点推进到头部")
}

func TestFollowReportsGap(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1)
	s, spec := newSyncer(t, st)
	_, err := s.Baseline(context.Background(), src)
	require.NoError(t, err)

	st.storeID = "s2" // 存储重建：身份对不上。
	res, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.True(t, res.Gap)

	st.storeID = "s1"
	st.put("A", 100, 2)
	st.put("A", 100, 3)
	st.trimmed = 2 // 日志被清理到序号 2：位点 2 之前的游标都断档。
	require.NoError(t, s.Store.SetSyncCursor(src, encodeCursor(storageio.ChangeCursor{NodeID: "n1", StoreID: "s1", Sequence: 1})))
	res, err = s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.True(t, res.Gap)
}

func TestReconcileRebuildsBaselineAndKeepsOutOfRetentionHistory(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2, 3, 4, 5, 6, 7)
	s, spec := newSyncer(t, st)
	spec.Lookback = 100
	_, err := s.Baseline(context.Background(), src)
	require.NoError(t, err)
	_, err = s.Pull(context.Background(), spec, []string{"A"}, at(8))
	require.NoError(t, err)

	// 本地基线失效（模拟离线太久）；离线期间 Storage 修正了 t5，并且保留期清理掉了 t0 t1（Storage 里没了、本地仍保留）。
	st.storeID = "s2"
	st.put("A", 700, 5)
	st.earliest["A"] = at(2)
	res, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.True(t, res.Gap)

	var progress []int
	out, err := s.Reconcile(context.Background(), spec, nil, func(done, total int) { progress = append(progress, done*10+total) })
	require.NoError(t, err)
	require.Equal(t, 1, out.Subjects)
	require.Contains(t, out.Replaced, "A")
	got := closes(t, s, "A")
	require.Equal(t, 100.0, got[0], "保留期之外的本地数据不动")
	require.Equal(t, 101.0, got[1])
	require.Equal(t, 705.0, got[5], "保留期内的修正被对账补上")
	require.Equal(t, []int{11}, progress)
	cur, ok, err := s.Cursor(src)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, storageio.ChangeCursor{NodeID: "n1", StoreID: "s2", Sequence: st.head()}, cur)

	// 对账期间发生的修正由 C0 之后的跟进补上。
	again, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.False(t, again.Gap)
}

func TestFrameConversionRoundTrip(t *testing.T) {
	cols := []storageio.ColumnInfo{
		{ColumnName: "close", ValueType: storageio.ColumnTypeDouble},
		{ColumnName: "trades", ValueType: storageio.ColumnTypeInt},
		{ColumnName: "closed", ValueType: storageio.ColumnTypeBool},
		{ColumnName: "ts", ValueType: storageio.ColumnTypeTime},
		{ColumnName: "meta", ValueType: storageio.ColumnTypeJSON, Attributes: map[string]string{"fill": "prev"}},
		{ColumnName: "volume", ValueType: storageio.ColumnTypeDouble, Attributes: map[string]string{"fill": "zero"}},
		{ColumnName: "subject_id", ValueType: storageio.ColumnTypeString},
		{ColumnName: "sys", ValueType: storageio.ColumnTypeString, OriginType: storageio.ColumnOriginSystem},
		{ColumnName: "off", ValueType: storageio.ColumnTypeString, Status: "disabled"},
	}
	fs := FieldsFromColumns(cols)
	names := make([]string, len(fs))
	for i, f := range fs {
		names[i] = f.Name
	}
	require.Equal(t, []string{"close", "closed", "meta", "trades", "ts", "volume"}, names, "系统列、保留列、停用列不进本地存储")
	fill := map[string]seriesstore.FillRule{}
	for _, f := range fs {
		fill[f.Name] = f.Fill
	}
	require.Equal(t, seriesstore.FillPrevClose, fill["close"], "没有 fill 属性时按字段名默认")
	require.Equal(t, seriesstore.FillZero, fill["volume"])
	require.Equal(t, seriesstore.FillPrev, fill["meta"])

	frame := &storageio.Frame{SubjectID: "A", Columns: []string{"data_time", "series_tag", "close", "closed", "meta", "trades", "ts", "volume"}, Rows: [][]any{
		{at(1), "", 2.5, true, storageio.JSONText(`{"a":1}`), int64(7), storageio.TimeText("2026-10-01T00:01:00Z"), nil},
		{at(0), "", 1.5, false, storageio.JSONText(`{}`), int64(3), storageio.TimeText("2026-10-01T00:00:00Z"), 9.0},
	}}
	c, err := FrameToColumns(fs, frame)
	require.NoError(t, err)
	require.Equal(t, at(0).Unix(), c.Times[0], "按时间排序")
	back := ColumnsToFrame("A", c, []string{"close", "closed", "meta", "trades", "ts", "volume"}, ValueTypes(cols))
	require.Equal(t, []any{at(1), "", 2.5, true, storageio.JSONText(`{"a":1}`), int64(7), storageio.TimeText("2026-10-01T00:01:00Z"), nil}, back.Rows[1], "携带列还原成原始类型")

	bad := &storageio.Frame{Columns: frame.Columns, Rows: [][]any{{at(0), "", "x", true, nil, int64(1), nil, nil}}}
	_, err = FrameToColumns(fs, bad)
	require.Error(t, err)
}

func Span(from, to time.Time) seriesstore.Span { return seriesstore.Span{From: from, To: to} }

// 保留期之外的本地历史（导入的或早先同步的）不能被「Storage 已经清理」改写：变更修正只重拉 Storage 现存的区间。
func TestFollowLeavesHistoryBeyondStorageRetention(t *testing.T) {
	st := newFakeStorage()
	st.put("A", 100, 0, 1, 2, 3)
	s, spec := newSyncer(t, st)
	_, err := s.Baseline(context.Background(), src)
	require.NoError(t, err)
	_, err = s.Pull(context.Background(), spec, []string{"A"}, at(4))
	require.NoError(t, err)

	// t1 的修正日志还没消费，但 Storage 已经清理了 t0、t1（保留起点移到 t2）。
	st.put("A", 500, 1)
	st.earliest["A"] = at(2)
	res, err := s.Follow(context.Background(), spec)
	require.NoError(t, err)
	require.Empty(t, res.Applied, "修正落在保留期之外：本地历史不动")
	require.Equal(t, map[int]float64{0: 100, 1: 101, 2: 102, 3: 103}, closes(t, s, "A"))
}
