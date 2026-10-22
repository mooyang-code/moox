package seriesstore

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var (
	testSrc    = Source{SpaceID: "crypto", DatasetID: "spot_kline", Freq: "1m"}
	testFields = []Field{
		{Name: "open", Type: TypeFloat64, Fill: FillPrevClose},
		{Name: "close", Type: TypeFloat64, Fill: FillPrevClose},
		{Name: "volume", Type: TypeFloat64, Fill: FillZero},
		{Name: "trades", Type: TypeInt64, Fill: FillZero},
		{Name: "note", Type: TypeString, Fill: FillPrev},
	}
	t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func at(minutes int) time.Time { return t0.Add(time.Duration(minutes) * time.Minute) }

func openTest(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(Options{Dir: dir, BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// bar 构造一根真实 K 线：close 取 price，volume 取 price/10。
func bar(c *Columns, minute int, tag string, price float64) {
	c.AppendRow(at(minute), tag, false, Float(price-1), Float(price), Float(price/10), Int(int64(price)), Str("n"))
}

func rowsOf(minutes map[int]float64) *Columns {
	c := NewColumns(testFields, len(minutes))
	var keys []int
	for m := range minutes {
		keys = append(keys, m)
	}
	sortInts(keys)
	for _, m := range keys {
		bar(c, m, "", minutes[m])
	}
	return c
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func write(t *testing.T, s *Store, subject string, fromMin, toMin int, rows *Columns, fill bool) WriteResult {
	t.Helper()
	res, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: subject, From: at(fromMin), To: at(toMin), Rows: rows}, WriteOptions{FillGaps: fill})
	require.NoError(t, err)
	return res
}

type cell struct {
	minute int
	filled bool
	close  float64
	open   float64
	volume float64
	trades int64
	null   bool
}

func readAll(t *testing.T, s *Store, subject string, fromMin, toMin int) []cell {
	t.Helper()
	got, err := s.ReadRange(context.Background(), testSrc, []string{subject}, at(fromMin), at(toMin))
	require.NoError(t, err)
	c := got[subject]
	var out []cell
	for i := 0; c != nil && i < c.Len(); i++ {
		out = append(out, cell{
			minute: int((c.Times[i] - t0.Unix()) / 60), filled: c.Filled[i],
			close: c.ValueAt(1, i).F, open: c.ValueAt(0, i).F, volume: c.ValueAt(2, i).F, trades: c.ValueAt(3, i).I,
			null: c.ValueAt(1, i).Null,
		})
	}
	return out
}

func TestAppendAndRead(t *testing.T) {
	s := openTest(t, t.TempDir())
	res := write(t, s, "BTCUSDT", 0, 3, rowsOf(map[int]float64{0: 100, 1: 101, 2: 102}), false)
	require.Equal(t, 3, res.Written)
	require.True(t, res.PrevLatest.IsZero())
	require.Equal(t, Span{From: at(0), To: at(3)}, res.Changed)

	latest, ok, err := s.Latest(testSrc, "BTCUSDT")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(2), latest)
	earliest, _, _ := s.Earliest(testSrc, "BTCUSDT")
	require.Equal(t, at(0), earliest)

	// 紧接着追加一根。
	res = write(t, s, "BTCUSDT", 3, 4, rowsOf(map[int]float64{3: 103}), false)
	require.Equal(t, at(2), res.PrevLatest)
	cells := readAll(t, s, "BTCUSDT", 0, 10)
	require.Len(t, cells, 4)
	require.Equal(t, 103.0, cells[3].close)
	require.False(t, cells[3].filled)

	subjects, err := s.Subjects(testSrc)
	require.NoError(t, err)
	require.Equal(t, []string{"BTCUSDT"}, subjects)
}

func TestGapRejectedWithoutFill(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 2, rowsOf(map[int]float64{0: 100, 1: 101}), false)

	// 区间内缺 t3。
	_, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(2), To: at(5), Rows: rowsOf(map[int]float64{2: 102, 4: 104})}, WriteOptions{})
	var gap *GapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, []time.Time{at(3)}, gap.Missing)

	// 与已有数据之间留空档也一样被拒绝。
	_, err = s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(5), To: at(6), Rows: rowsOf(map[int]float64{5: 105})}, WriteOptions{})
	require.ErrorAs(t, err, &gap)
	require.Equal(t, []time.Time{at(2), at(3), at(4)}, gap.Missing)

	// 失败的写入不留任何痕迹。
	latest, _, _ := s.Latest(testSrc, "BTCUSDT")
	require.Equal(t, at(1), latest)
}

func TestFillRules(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 1, rowsOf(map[int]float64{0: 100}), false)
	res := write(t, s, "BTCUSDT", 3, 4, rowsOf(map[int]float64{3: 105}), true)
	require.Equal(t, 2, res.Filled)
	// 间隙的填充行也算改变的区间：从 t1 开始。
	require.Equal(t, Span{From: at(1), To: at(4)}, res.Changed)

	cells := readAll(t, s, "BTCUSDT", 0, 10)
	require.Len(t, cells, 4)
	for _, m := range []int{1, 2} {
		c := cells[m]
		require.True(t, c.filled, "t%d", m)
		require.Equal(t, 100.0, c.open, "open 取上一期 close")
		require.Equal(t, 100.0, c.close, "close 取上一期 close")
		require.Equal(t, 0.0, c.volume, "volume 填 0")
		require.Equal(t, int64(0), c.trades)
	}
	require.False(t, cells[3].filled)
	require.Equal(t, 105.0, cells[3].close)
}

func TestCorrectionRefillsFollowingFilledRows(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 1, rowsOf(map[int]float64{0: 100}), false)
	write(t, s, "BTCUSDT", 3, 4, rowsOf(map[int]float64{3: 105}), true) // t1 t2 填充

	// 采集器补回 t1 的真实值：t2 要改按 t1 填充，改变的区间延伸到 t3。
	res, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(1), To: at(2), Rows: rowsOf(map[int]float64{1: 110})}, WriteOptions{FillGaps: true, RecordPending: true, PendingReason: "storage_change"})
	require.NoError(t, err)
	require.Equal(t, Span{From: at(1), To: at(3)}, res.Changed)
	require.Equal(t, 1, res.Filled, "t2 被重新生成")

	cells := readAll(t, s, "BTCUSDT", 0, 10)
	require.Len(t, cells, 4)
	require.False(t, cells[1].filled)
	require.Equal(t, 110.0, cells[1].close)
	require.True(t, cells[2].filled)
	require.Equal(t, 110.0, cells[2].close, "t2 按新的前值填充")
	require.Equal(t, 105.0, cells[3].close)

	pending, err := s.Pending(testSrc)
	require.NoError(t, err)
	require.Equal(t, []PendingSpan{{Subject: "BTCUSDT", From: at(1), To: at(3), Reason: "storage_change"}}, pending)
	require.NoError(t, s.ClearPending(testSrc, pending))
	pending, err = s.Pending(testSrc)
	require.NoError(t, err)
	require.Empty(t, pending)
}

func TestAppendBeyondLatestIsNotPending(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 2, rowsOf(map[int]float64{0: 100, 1: 101}), false)
	_, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(2), To: at(3), Rows: rowsOf(map[int]float64{2: 102})}, WriteOptions{FillGaps: true, RecordPending: true})
	require.NoError(t, err)
	pending, err := s.Pending(testSrc)
	require.NoError(t, err)
	require.Empty(t, pending, "追加新数据不需要失效重算")
}

func TestPrependEarlierHistory(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 10, 12, rowsOf(map[int]float64{10: 100, 11: 101}), false)

	// 导入更早的历史，与已有数据之间缺 t8 t9。
	_, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(5), To: at(8), Rows: rowsOf(map[int]float64{5: 95, 6: 96, 7: 97})}, WriteOptions{})
	var gap *GapError
	require.ErrorAs(t, err, &gap)
	require.Equal(t, []time.Time{at(8), at(9)}, gap.Missing)

	res := write(t, s, "BTCUSDT", 5, 8, rowsOf(map[int]float64{5: 95, 6: 96, 7: 97}), true)
	require.Equal(t, 2, res.Filled)
	require.Equal(t, at(5), res.Earliest)
	require.Equal(t, at(11), res.Latest)
	cells := readAll(t, s, "BTCUSDT", 0, 20)
	require.Len(t, cells, 7)
	require.True(t, cells[3].filled)
	require.Equal(t, 97.0, cells[3].close)
	require.True(t, cells[4].filled)
	require.False(t, cells[5].filled)
}

// 补更早的历史使 Earliest 前移：不带 RecordPending 也自动记入失效重算待办（此前因历史不足而为空的结果要重算）；普通追加不记。
func TestEarliestMovedRecordsPendingWithoutRecordPending(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 10, 12, rowsOf(map[int]float64{10: 100, 11: 101}), false)
	pending, err := s.Pending(testSrc)
	require.NoError(t, err)
	require.Empty(t, pending, "首次写入与追加不记")

	res := write(t, s, "BTCUSDT", 8, 10, rowsOf(map[int]float64{8: 98, 9: 99}), false)
	require.Equal(t, at(10), res.PrevEarliest)
	require.Equal(t, at(8), res.Earliest)
	pending, err = s.Pending(testSrc)
	require.NoError(t, err)
	require.Equal(t, []PendingSpan{{Subject: "BTCUSDT", From: at(8), To: at(10), Reason: "earliest_moved"}}, pending)
}

func TestReplaceWithMissingRowsFills(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 5, rowsOf(map[int]float64{0: 100, 1: 101, 2: 102, 3: 103, 4: 104}), false)
	// Storage 删掉了 t2：整段替换后 t2 变成填充行。
	res := write(t, s, "BTCUSDT", 0, 5, rowsOf(map[int]float64{0: 100, 1: 101, 3: 103, 4: 104}), true)
	require.Equal(t, 5, res.Removed)
	cells := readAll(t, s, "BTCUSDT", 0, 10)
	require.Len(t, cells, 5)
	require.True(t, cells[2].filled)
	require.Equal(t, 101.0, cells[2].close)
}

func TestTruncatingTailAndEmptyingSeries(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 4, rowsOf(map[int]float64{0: 100, 1: 101, 2: 102, 3: 103}), false)
	// 用空行替换末尾两根：本地最新退回 t1。
	write(t, s, "BTCUSDT", 2, 4, NewColumns(testFields, 0), false)
	latest, _, _ := s.Latest(testSrc, "BTCUSDT")
	require.Equal(t, at(1), latest)
	require.Len(t, readAll(t, s, "BTCUSDT", 0, 10), 2)
	// 全部替换为空：标的不再有本地数据。
	write(t, s, "BTCUSDT", 0, 2, NewColumns(testFields, 0), false)
	_, ok, err := s.Bounds(testSrc, "BTCUSDT")
	require.NoError(t, err)
	require.False(t, ok)
	subjects, _ := s.Subjects(testSrc)
	require.Empty(t, subjects)
}

func TestDroppingFilledRunWithoutPrevious(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 1, rowsOf(map[int]float64{0: 100}), false)
	write(t, s, "BTCUSDT", 3, 4, rowsOf(map[int]float64{3: 105}), true) // t1 t2 填充
	// 删掉 t0：后面的填充行没有前值，被丢弃，序列从第一根真实行 t3 开始。
	write(t, s, "BTCUSDT", 0, 1, NewColumns(testFields, 0), true)
	cells := readAll(t, s, "BTCUSDT", 0, 10)
	require.Len(t, cells, 1)
	require.Equal(t, 3, cells[0].minute)
	earliest, _, _ := s.Earliest(testSrc, "BTCUSDT")
	require.Equal(t, at(3), earliest)
}

func TestBlockBoundaries(t *testing.T) {
	s := openTest(t, t.TempDir())
	minutes := map[int]float64{}
	for m := 0; m < 150; m++ { // 跨三个小时块
		minutes[m] = float64(100 + m)
	}
	write(t, s, "BTCUSDT", 0, 150, rowsOf(minutes), false)
	cells := readAll(t, s, "BTCUSDT", 0, 200)
	require.Len(t, cells, 150)
	// 半开区间读取。
	cells = readAll(t, s, "BTCUSDT", 59, 61)
	require.Len(t, cells, 2)
	require.Equal(t, []int{59, 60}, []int{cells[0].minute, cells[1].minute})
	// 修正跨块边界的一根。
	write(t, s, "BTCUSDT", 60, 61, rowsOf(map[int]float64{60: 999}), true)
	cells = readAll(t, s, "BTCUSDT", 59, 62)
	require.Equal(t, 999.0, cells[1].close)
	require.Len(t, cells, 3)
}

func TestMultipleSeriesTags(t *testing.T) {
	s := openTest(t, t.TempDir())
	c := NewColumns(testFields, 0)
	bar(c, 0, "a", 100)
	bar(c, 0, "b", 200)
	bar(c, 1, "a", 101)
	bar(c, 2, "a", 102)
	bar(c, 2, "b", 202)
	write(t, s, "BTCUSDT", 0, 3, c, true) // b 在 t1 缺，按 b 自己的前值填充
	got, err := s.ReadRange(context.Background(), testSrc, []string{"BTCUSDT"}, at(0), at(10))
	require.NoError(t, err)
	cols := got["BTCUSDT"]
	require.Equal(t, 6, cols.Len())
	var filledTag string
	for i := 0; i < cols.Len(); i++ {
		if cols.Filled[i] {
			filledTag = cols.Tags[i]
			require.Equal(t, 200.0, cols.ValueAt(1, i).F)
		}
	}
	require.Equal(t, "b", filledTag)
}

func TestSchemaEvolutionAndMismatch(t *testing.T) {
	s := openTest(t, t.TempDir())
	write(t, s, "BTCUSDT", 0, 2, rowsOf(map[int]float64{0: 100, 1: 101}), false)

	// 缺字段：导入必须带全部字段。
	short := NewColumns(testFields[:2], 0)
	short.AppendRow(at(2), "", false, Float(1), Float(2))
	_, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(2), To: at(3), Rows: short}, WriteOptions{})
	require.ErrorIs(t, err, ErrSchema)

	// 数据集加字段：新行带新字段，旧行读出为 NULL。
	more := append(append([]Field(nil), testFields...), Field{Name: "funding", Type: TypeFloat64, Fill: FillPrev})
	c := NewColumns(more, 0)
	c.AppendRow(at(2), "", false, Float(101), Float(102), Float(1), Int(1), Str("n"), Float(0.01))
	_, err = s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(2), To: at(3), Rows: c}, WriteOptions{})
	require.NoError(t, err)
	got, err := s.ReadRange(context.Background(), testSrc, []string{"BTCUSDT"}, at(0), at(10))
	require.NoError(t, err)
	cols := got["BTCUSDT"]
	require.Equal(t, 3, cols.Len())
	require.Len(t, cols.Fields, 6)
	require.True(t, cols.ValueAt(5, 0).Null)
	require.True(t, cols.ValueAt(5, 1).Null)
	require.Equal(t, 0.01, cols.ValueAt(5, 2).F)

	// 类型变了：拒绝。
	bad := append([]Field(nil), more...)
	bad[0].Type = TypeInt64
	_, err = s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "BTCUSDT", From: at(3), To: at(4), Rows: NewColumns(bad, 0)}, WriteOptions{})
	require.ErrorIs(t, err, ErrSchema)
}

func TestBatchCursorAndReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(Options{Dir: dir, BlockCacheBytes: 8 << 20})
	require.NoError(t, err)
	res, err := s.WriteBatch(context.Background(), testSrc, []SubjectWrite{
		{Subject: "A", From: at(0), To: at(2), Rows: rowsOf(map[int]float64{0: 1, 1: 2})},
		{Subject: "B", From: at(0), To: at(1), Rows: rowsOf(map[int]float64{0: 10})},
	}, WriteOptions{Cursor: []byte("node1/store1/42")})
	require.NoError(t, err)
	require.Len(t, res, 2)
	require.NoError(t, s.Close())

	s, err = Open(Options{Dir: dir, BlockCacheBytes: 8 << 20})
	require.NoError(t, err)
	defer s.Close()
	cursor, ok, err := s.SyncCursor(testSrc)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "node1/store1/42", string(cursor))
	subjects, err := s.Subjects(testSrc)
	require.NoError(t, err)
	require.Equal(t, []string{"A", "B"}, subjects)
	fields, ok, err := s.Schema(testSrc)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, testFields, fields)

	// 同一批里标的重复：拒绝。
	_, err = s.WriteBatch(context.Background(), testSrc, []SubjectWrite{{Subject: "A", From: at(2), To: at(3)}, {Subject: "A", From: at(3), To: at(4)}}, WriteOptions{})
	require.ErrorIs(t, err, ErrInvalidWrite)
}

func TestLayoutMismatch(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir)
	require.NoError(t, s.db.Set([]byte("m/layout"), []byte("99"), nil))
	require.NoError(t, s.Close())
	_, err := Open(Options{Dir: dir})
	require.True(t, errors.Is(err, ErrLayoutMismatch))
}

func TestInvalidWrites(t *testing.T) {
	s := openTest(t, t.TempDir())
	ctx := context.Background()
	_, err := s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "A", From: at(0).Add(time.Second), To: at(1)}, WriteOptions{})
	require.ErrorIs(t, err, ErrInvalidWrite)
	_, err = s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "A", From: at(1), To: at(1)}, WriteOptions{})
	require.ErrorIs(t, err, ErrInvalidWrite)
	_, err = s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "a/b", From: at(0), To: at(1)}, WriteOptions{})
	require.ErrorIs(t, err, ErrInvalidWrite)
	// 行时间超出区间。
	_, err = s.WriteRange(ctx, testSrc, SubjectWrite{Subject: "A", From: at(0), To: at(1), Rows: rowsOf(map[int]float64{1: 1})}, WriteOptions{})
	require.ErrorIs(t, err, ErrInvalidWrite)
	// 不支持的频率。
	_, err = s.WriteRange(ctx, Source{SpaceID: "crypto", DatasetID: "d", Freq: "3m"}, SubjectWrite{Subject: "A", From: at(0), To: at(1)}, WriteOptions{})
	require.Error(t, err)
}

func TestCodecRoundTrip(t *testing.T) {
	fields := []Field{{Name: "f", Type: TypeFloat64}, {Name: "i", Type: TypeInt64}, {Name: "b", Type: TypeBool}, {Name: "s", Type: TypeString}}
	c := NewColumns(fields, 3)
	c.AppendRow(at(0), "x", false, Float(math.Pi), Int(-7), Bool(true), Str("héllo"))
	c.AppendRow(at(1), "y", true, NullValue, NullValue, NullValue, NullValue)
	c.AppendRow(at(2), "x", false, Float(math.Inf(1)), Int(math.MaxInt64), Bool(false), Str(""))
	got, err := decodeBlock(encodeBlock(c))
	require.NoError(t, err)
	require.Equal(t, c.Fields, got.Fields)
	require.Equal(t, c.Times, got.Times)
	require.Equal(t, c.Tags, got.Tags)
	require.Equal(t, c.Filled, got.Filled)
	for f := range fields {
		for r := 0; r < 3; r++ {
			require.Equal(t, c.ValueAt(f, r), got.ValueAt(f, r), "field %d row %d", f, r)
		}
	}
	// 截断的数据被识别为损坏，不会 panic。
	raw := encodeBlock(c)
	for cut := 0; cut < len(raw); cut += 3 {
		_, err := decodeBlock(raw[:cut])
		require.Error(t, err)
	}
}

// TestRandomWritesKeepInvariants 随机整段替换（含缺期、截断、前插），检查本地数据始终连续、真实行与模型一致、
// 填充行都是按前一行和规则生成的。
func TestRandomWritesKeepInvariants(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for round := 0; round < 40; round++ {
		s, err := Open(Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
		require.NoError(t, err)
		real := map[int]float64{} // 模型：真实行
		for op := 0; op < 25; op++ {
			from := rng.Intn(150)
			to := from + 1 + rng.Intn(30)
			rows := map[int]float64{}
			for m := from; m < to; m++ {
				if rng.Intn(4) != 0 {
					rows[m] = float64(rng.Intn(1000) + 1)
				}
			}
			_, err := s.WriteRange(context.Background(), testSrc, SubjectWrite{Subject: "S", From: at(from), To: at(to), Rows: rowsOf(rows)}, WriteOptions{FillGaps: true})
			require.NoError(t, err)
			for m := from; m < to; m++ {
				delete(real, m)
			}
			for m, v := range rows {
				real[m] = v
			}
			checkInvariants(t, s, real)
		}
		require.NoError(t, s.Close())
	}
}

func checkInvariants(t *testing.T, s *Store, real map[int]float64) {
	t.Helper()
	cells := readAll(t, s, "S", 0, 400)
	if len(cells) == 0 {
		require.Empty(t, real)
		return
	}
	// 连续，且第一根是真实行。
	require.False(t, cells[0].filled, "序列的第一根必须是真实行")
	for i := 1; i < len(cells); i++ {
		require.Equal(t, cells[i-1].minute+1, cells[i].minute, "必须连续")
	}
	gotReal := map[int]float64{}
	for i, c := range cells {
		if c.filled {
			prev := cells[i-1]
			require.Equal(t, prev.close, c.close, "填充行的 close 取前一行")
			require.Equal(t, prev.close, c.open)
			require.Equal(t, 0.0, c.volume)
			continue
		}
		gotReal[c.minute] = c.close
	}
	// 真实行一个不丢；本地最后一根必须是真实行或其后的填充行不存在（尾部不填充）。
	require.Equal(t, real, gotReal)
	require.False(t, cells[len(cells)-1].filled, "尾部不会有填充行")
	b, ok, err := s.Bounds(testSrc, "S")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, at(cells[0].minute), b.Earliest)
	require.Equal(t, at(cells[len(cells)-1].minute), b.Latest)
}
