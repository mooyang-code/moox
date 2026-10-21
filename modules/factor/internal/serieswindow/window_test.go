package serieswindow

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/stretchr/testify/require"
)

var (
	src    = seriesstore.Source{SpaceID: "crypto", DatasetID: "spot", Freq: "1m"}
	fields = []seriesstore.Field{
		{Name: "close", Type: seriesstore.TypeFloat64, Fill: seriesstore.FillPrevClose},
		{Name: "volume", Type: seriesstore.TypeFloat64, Fill: seriesstore.FillZero},
		{Name: "trades", Type: seriesstore.TypeInt64, Fill: seriesstore.FillZero},
		{Name: "note", Type: seriesstore.TypeString},
	}
	t0 = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
)

func at(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }

func rows(from, to int, tag string) *seriesstore.Columns {
	c := seriesstore.NewColumns(fields, to-from)
	for m := from; m < to; m++ {
		c.AppendRow(at(m), tag, false, seriesstore.Float(float64(m)), seriesstore.Float(float64(m)/10), seriesstore.Int(int64(m)), seriesstore.Str("n"))
	}
	return c
}

func newStore(t *testing.T) *seriesstore.Store {
	t.Helper()
	s, err := seriesstore.Open(seriesstore.Options{Dir: t.TempDir(), BlockCacheBytes: 8 << 20, DisableSync: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func put(t *testing.T, s *seriesstore.Store, subject string, from, to int, c *seriesstore.Columns) seriesstore.WriteResult {
	t.Helper()
	res, err := s.WriteRange(context.Background(), src, seriesstore.SubjectWrite{Subject: subject, From: at(from), To: at(to), Rows: c}, seriesstore.WriteOptions{FillGaps: true})
	require.NoError(t, err)
	return res
}

func newWindow(t *testing.T, lookback, headroom int) *Window {
	t.Helper()
	w, err := New(Config{Source: src, Lookback: lookback, Headroom: headroom}, 60, NumericFields(fields))
	require.NoError(t, err)
	return w
}

func closeOf(v *View, subject, tag string) []float64 {
	for _, s := range v.Series(subject) {
		if s.Tag == tag {
			i, _ := v.FieldIndex("close")
			return s.Cols[i]
		}
	}
	return nil
}

func TestLoadKeepsLastLookbackRows(t *testing.T) {
	store := newStore(t)
	put(t, store, "BTC", 0, 100, rows(0, 100, ""))
	w := newWindow(t, 10, 4)
	require.NoError(t, w.Load(context.Background(), store, []string{"BTC", "NOPE"}))
	require.Equal(t, []string{"BTC"}, w.Subjects())
	latest, ok := w.Latest("BTC")
	require.True(t, ok)
	require.Equal(t, at(99), latest)

	v := w.Acquire([]string{"BTC", "NOPE"}, at(99).Unix())
	require.Equal(t, 1, v.Len())
	s := v.Series("BTC")[0]
	require.Equal(t, 10, s.N)
	require.Equal(t, at(90).Unix(), s.Start)
	require.Equal(t, []float64{90, 91, 92, 93, 94, 95, 96, 97, 98, 99}, closeOf(v, "BTC", ""))
	vol, _ := v.FieldIndex("volume")
	require.InDelta(t, 9.9, s.Cols[vol][9], 1e-9)
	trades, _ := v.FieldIndex("trades")
	require.Equal(t, 99.0, s.Cols[trades][9], "INT 转 float64")
	_, hasNote := v.FieldIndex("note")
	require.False(t, hasNote, "非数值字段不进窗口")
}

func TestAcquireClipsToEnd(t *testing.T) {
	store := newStore(t)
	put(t, store, "BTC", 0, 20, rows(0, 20, ""))
	w := newWindow(t, 10, 4)
	require.NoError(t, w.Load(context.Background(), store, []string{"BTC"}))
	v := w.Acquire([]string{"BTC"}, at(15).Unix())
	require.Equal(t, []float64{10, 11, 12, 13, 14, 15}, closeOf(v, "BTC", ""))
	require.Equal(t, 6, v.Series("BTC")[0].N)
	require.Equal(t, at(15).Unix(), v.Series("BTC")[0].End(60))
	// 截止时间早于窗口全部行：该标的不在视图里。
	require.Equal(t, 0, w.Acquire([]string{"BTC"}, at(5).Unix()).Len())
	// 截止时间等于窗口第一行：只剩一根。
	require.Equal(t, []float64{10}, closeOf(w.Acquire([]string{"BTC"}, at(10).Unix()), "BTC", ""))
}

func TestAppendSlidesAndCompactsWithoutDisturbingLeases(t *testing.T) {
	store := newStore(t)
	put(t, store, "BTC", 0, 10, rows(0, 10, ""))
	w := newWindow(t, 10, 3)
	require.NoError(t, w.Load(context.Background(), store, []string{"BTC"}))
	lease := w.Acquire([]string{"BTC"}, at(9).Unix())
	before := append([]float64(nil), closeOf(lease, "BTC", "")...)

	// 追加 20 根：越过余量好几次（发生换代），窗口始终是最近 10 根。
	for m := 10; m < 30; m++ {
		require.NoError(t, w.Append("BTC", rows(m, m+1, "")))
		v := w.Acquire([]string{"BTC"}, at(m).Unix())
		got := closeOf(v, "BTC", "")
		require.Len(t, got, 10)
		require.Equal(t, float64(m), got[9])
		require.Equal(t, float64(m-9), got[0])
	}
	require.Equal(t, before, closeOf(lease, "BTC", ""), "租约看到的数据不随追加与换代改变")
	st := w.Stats()
	require.Equal(t, 1, st.Series)
	require.Equal(t, 10, st.Rows)
	require.Equal(t, int64((10+3)*(3*8+1)), st.Bytes)
}

func TestAppendRejectsGapsAndSchemaChange(t *testing.T) {
	store := newStore(t)
	put(t, store, "BTC", 0, 10, rows(0, 10, ""))
	w := newWindow(t, 10, 4)
	require.NoError(t, w.Load(context.Background(), store, []string{"BTC"}))

	require.ErrorIs(t, w.Append("BTC", rows(11, 12, "")), ErrDiscontinuous, "跳过了 t10")
	require.ErrorIs(t, w.Append("ETH", rows(0, 1, "")), ErrDiscontinuous, "标的不在窗口里")
	require.ErrorIs(t, w.Append("BTC", rows(10, 11, "other")), ErrDiscontinuous, "新序列不能追加")
	// 失败的追加不留痕迹。
	require.Equal(t, 10, w.Acquire([]string{"BTC"}, at(100).Unix()).Series("BTC")[0].N)
	latest, _ := w.Latest("BTC")
	require.Equal(t, at(9), latest)

	extra := append(append([]seriesstore.Field(nil), fields...), seriesstore.Field{Name: "funding", Type: seriesstore.TypeFloat64})
	c := seriesstore.NewColumns(extra, 1)
	c.AppendRow(at(10), "", false, seriesstore.Float(1), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"), seriesstore.Float(0.1))
	require.ErrorIs(t, w.Append("BTC", c), ErrSchemaChanged)
	short := seriesstore.NewColumns(fields[:1], 1)
	short.AppendRow(at(10), "", false, seriesstore.Float(1))
	require.ErrorIs(t, w.Append("BTC", short), ErrSchemaChanged)
}

func TestReloadAfterCorrectionAndFilledRows(t *testing.T) {
	store := newStore(t)
	// t0 真实，t1 t2 填充，t3 真实。
	put(t, store, "BTC", 0, 1, rows(0, 1, ""))
	put(t, store, "BTC", 3, 4, rows(3, 4, ""))
	w := newWindow(t, 10, 4)
	require.NoError(t, w.Load(context.Background(), store, []string{"BTC"}))
	v := w.Acquire([]string{"BTC"}, at(3).Unix())
	s := v.Series("BTC")[0]
	require.Equal(t, []bool{false, true, true, false}, s.Filled)
	require.Equal(t, []float64{0, 0, 0, 3}, closeOf(v, "BTC", ""))

	// 修正 t1 之后，t2 也被重新生成；窗口按标的重新载入。
	c := seriesstore.NewColumns(fields, 1)
	c.AppendRow(at(1), "", false, seriesstore.Float(111), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	put(t, store, "BTC", 1, 2, c)
	require.NoError(t, w.Reload(context.Background(), store, "BTC"))
	v = w.Acquire([]string{"BTC"}, at(3).Unix())
	require.Equal(t, []float64{0, 111, 111, 3}, closeOf(v, "BTC", ""))
	require.Equal(t, []bool{false, false, true, false}, v.Series("BTC")[0].Filled)
}

func TestNullBecomesNaNAndMultipleTags(t *testing.T) {
	store := newStore(t)
	c := seriesstore.NewColumns(fields, 3)
	c.AppendRow(at(0), "a", false, seriesstore.NullValue, seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	c.AppendRow(at(0), "b", false, seriesstore.Float(5), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	c.AppendRow(at(1), "a", false, seriesstore.Float(2), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	c.AppendRow(at(1), "b", false, seriesstore.Float(6), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	put(t, store, "BTC", 0, 2, c)
	w := newWindow(t, 5, 2)
	require.NoError(t, w.Load(context.Background(), store, []string{"BTC"}))
	v := w.Acquire([]string{"BTC"}, at(1).Unix())
	require.Equal(t, 2, v.Len())
	require.Equal(t, "a", v.Series("BTC")[0].Tag, "按标签排序")
	require.True(t, math.IsNaN(closeOf(v, "BTC", "a")[0]))
	require.Equal(t, 2.0, closeOf(v, "BTC", "a")[1])
	require.Equal(t, []float64{5, 6}, closeOf(v, "BTC", "b"))

	// 两个序列一起追加。
	n := seriesstore.NewColumns(fields, 2)
	n.AppendRow(at(2), "a", false, seriesstore.Float(3), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	n.AppendRow(at(2), "b", false, seriesstore.Float(7), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	require.NoError(t, w.Append("BTC", n))
	v = w.Acquire([]string{"BTC"}, at(2).Unix())
	require.Equal(t, []float64{5, 6, 7}, closeOf(v, "BTC", "b"))
}

func TestFromColumnsBuildsViewAndRejectsGaps(t *testing.T) {
	c := rows(5, 9, "")
	v, err := FromColumns(60, NumericFields(fields), map[string]*seriesstore.Columns{"BTC": c})
	require.NoError(t, err)
	require.Equal(t, []string{"BTC"}, v.Subjects())
	s := v.Series("BTC")[0]
	require.Equal(t, at(5).Unix(), s.Start)
	require.Equal(t, 4, s.N)
	require.Equal(t, []float64{5, 6, 7, 8}, closeOf(v, "BTC", ""))
	latest, ok := v.Latest("BTC")
	require.True(t, ok)
	require.Equal(t, at(8).Unix(), latest)

	gap := rows(0, 2, "")
	gap.AppendRow(at(5), "", false, seriesstore.Float(5), seriesstore.Float(1), seriesstore.Int(1), seriesstore.Str("n"))
	_, err = FromColumns(60, NumericFields(fields), map[string]*seriesstore.Columns{"BTC": gap})
	require.Error(t, err)
}

func TestBytesPerSeriesMatchesStats(t *testing.T) {
	require.Equal(t, int64((10+4)*(3*8+1)), BytesPerSeries(10, 4, 3))
	require.Equal(t, int64((100+25)*(7*8+1)), BytesPerSeries(100, 0, 7))
	_, err := New(Config{Source: src}, 60, nil)
	require.Error(t, err)
}
