package seriesformat

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

var fields = []Field{
	{Name: "close", Type: TypeDouble},
	{Name: "trades", Type: TypeInt},
	{Name: "closed", Type: TypeBool},
	{Name: "ts", Type: TypeTime},
	{Name: "note", Type: TypeString},
}

func open(t *testing.T, text string, opts Options) BatchReader {
	t.Helper()
	d, err := Lookup("csv")
	require.NoError(t, err)
	r, err := d.Open(strings.NewReader(text), opts)
	require.NoError(t, err)
	return r
}

func TestCSVDecodesAllFieldTypesAndUTC(t *testing.T) {
	text := "data_time,close,trades,closed,ts,note\n" +
		"2026-10-01T08:00:00+08:00,100.5,12,true,2026-10-01 00:00:00,a\n" +
		"2026-10-01 00:01:00,,13.0,0,,\n" +
		"1790812920,102,14,yes,1790812920000,x\n" +
		"\n"
	r := open(t, text, Options{Fields: fields, SeriesTag: "venue:okx"})
	batch, err := r.Next(10)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 3, batch.Len())
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, []time.Time{t0, t0.Add(time.Minute), time.Unix(1790812920, 0).UTC()}, batch.Times, "带时区的转换成 UTC，没有时区的视为 UTC，Unix 秒也接受")
	require.Equal(t, []string{"venue:okx", "venue:okx", "venue:okx"}, batch.Tags)
	require.Equal(t, []float64{100.5, 0, 102}, batch.Cols[0].F)
	require.Equal(t, []bool{false, true, false}, batch.Cols[0].Null, "空单元格是空值")
	require.Equal(t, []int64{12, 13, 14}, batch.Cols[1].I, "12.0 这类整数写法被接受")
	require.Equal(t, []int64{1, 0, 1}, batch.Cols[2].I)
	require.Equal(t, "2026-10-01T00:00:00Z", batch.Cols[3].S[0])
	require.Equal(t, "", batch.Cols[3].S[1])
	require.Equal(t, "2026-10-01T00:02:00Z", batch.Cols[3].S[2], "毫秒时间戳")
	require.Equal(t, []string{"a", "", "x"}, batch.Cols[4].S)
}

func TestCSVBatchingAndTimeColumnName(t *testing.T) {
	text := "candle_begin_time,close\n2026-10-01 00:00:00,1\n2026-10-01 00:01:00,2\n2026-10-01 00:02:00,3\n"
	r := open(t, text, Options{Fields: []Field{{Name: "close", Type: TypeDouble}}, TimeColumn: "candle_begin_time"})
	first, err := r.Next(2)
	require.NoError(t, err)
	require.Equal(t, 2, first.Len())
	second, err := r.Next(2)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, 1, second.Len())
	require.NoError(t, r.Close())
	_, err = r.Next(1)
	require.Error(t, err)
}

func TestCSVRequiresAllFieldsAndRejectsUnknownColumns(t *testing.T) {
	d, _ := Lookup("csv")
	_, err := d.Open(strings.NewReader("data_time,close\n2026-10-01,1\n"), Options{Fields: fields})
	var missing *ErrMissingFields
	require.True(t, errors.As(err, &missing))
	require.Equal(t, []string{"closed", "note", "trades", "ts"}, missing.Fields)

	_, err = d.Open(strings.NewReader("data_time,close,extra\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
	var unknown *ErrUnknownColumns
	require.True(t, errors.As(err, &unknown))
	require.Equal(t, []string{"extra"}, unknown.Columns)

	_, err = d.Open(strings.NewReader("close\n1\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
	require.ErrorContains(t, err, "缺少时间列")
	_, err = d.Open(strings.NewReader("data_time,close,series_tag\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}, SeriesTag: "x"})
	require.ErrorContains(t, err, "不能再有 series_tag 列")
	_, err = d.Open(strings.NewReader(""), Options{})
	require.ErrorContains(t, err, "空的")
	_, err = d.Open(strings.NewReader("data_time,close,close\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
	require.ErrorContains(t, err, "重复")
}

func TestCSVReportsBadCellsWithLineNumbers(t *testing.T) {
	cases := map[string]string{
		"data_time,close\n2026-10-01,abc\n":               "第 2 行字段 close",
		"data_time,close\nnot-a-time,1\n":                 "第 2 行时间",
		"data_time,trades\n2026-10-01,1.5\n":              "不是整数",
		"data_time,closed\n2026-10-01,maybe\n":            "不是布尔值",
		"data_time,close\n2026-10-01,1\n2026-10-02,NaN\n": "第 3 行",
	}
	for text, want := range cases {
		cols := strings.Split(strings.Split(text, "\n")[0], ",")[1:]
		var fs []Field
		for _, c := range cols {
			switch c {
			case "close":
				fs = append(fs, Field{Name: c, Type: TypeDouble})
			case "trades":
				fs = append(fs, Field{Name: c, Type: TypeInt})
			case "closed":
				fs = append(fs, Field{Name: c, Type: TypeBool})
			}
		}
		_, err := open(t, text, Options{Fields: fs}).Next(10)
		require.ErrorContains(t, err, want, text)
	}
}

func TestParseTime(t *testing.T) {
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, in := range []string{"2026-10-01T00:00:00Z", "2026-10-01T08:00:00+08:00", "2026-10-01 00:00:00", "2026-10-01", "1790812800", "1790812800000"} {
		got, err := ParseTime(in)
		require.NoError(t, err, in)
		require.Equal(t, want, got, in)
	}
	for _, bad := range []string{"", "yesterday", "-5", "0"} {
		_, err := ParseTime(bad)
		require.Error(t, err, bad)
	}
}

func TestCheckerValidatesAlignmentOrderAndCountsGaps(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 10, 1, 0, m, 0, 0, time.UTC) }
	mk := func(minutes ...int) *Batch {
		b := &Batch{}
		for _, m := range minutes {
			b.Times, b.Tags = append(b.Times, at(m)), append(b.Tags, "")
		}
		return b
	}
	c := NewChecker(time.Minute)
	require.NoError(t, c.Check(mk(0, 1, 2, 5)))
	require.NoError(t, c.Check(mk(6, 10)))
	rep := c.Finish()
	require.Equal(t, 6, rep.Rows)
	require.Equal(t, at(0), rep.First)
	require.Equal(t, at(10), rep.Last)
	require.Equal(t, 5, rep.MissingCount)
	require.Equal(t, []time.Time{at(3), at(4), at(7), at(8), at(9)}, rep.Missing)

	require.ErrorContains(t, NewChecker(time.Minute).Check(&Batch{Times: []time.Time{at(0).Add(time.Second)}, Tags: []string{""}}), "没有按")
	require.ErrorContains(t, NewChecker(time.Minute).Check(mk(1, 1)), "重复")
	require.ErrorContains(t, NewChecker(time.Minute).Check(mk(2, 1)), "升序")

	// 不同序列各自检查，互不影响。
	two := &Batch{Times: []time.Time{at(0), at(0), at(1)}, Tags: []string{"a", "b", "a"}}
	require.NoError(t, NewChecker(time.Minute).Check(two))

	// 多序列的文件要整体按时间升序：按序列分块排列（a 的全部行、再 b 的全部行）会让按时间边界切出的批互相覆盖，拒绝。
	blocked := &Batch{Times: []time.Time{at(0), at(1), at(0), at(1)}, Tags: []string{"a", "a", "b", "b"}}
	require.ErrorContains(t, NewChecker(time.Minute).Check(blocked), "整体按时间升序")

	limited := NewChecker(time.Minute)
	limited.MaxMissing = 2
	require.NoError(t, limited.Check(mk(0, 9)))
	require.Equal(t, 8, limited.Finish().MissingCount)
	require.Len(t, limited.Finish().Missing, 2)
}

func TestRegistry(t *testing.T) {
	require.Equal(t, []string{"csv"}, Formats())
	_, err := Lookup("parquet")
	require.ErrorContains(t, err, "不支持的数据格式")
	require.Panics(t, func() { Register(csvDecoder{}) })
}
