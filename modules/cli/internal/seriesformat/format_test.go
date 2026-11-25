package seriesformat

import (
	"bytes"
	"compress/gzip"
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
	r, err := OpenCSV(strings.NewReader(text), opts)
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
	_, err := OpenCSV(strings.NewReader("data_time,close\n2026-10-01,1\n"), Options{Fields: fields})
	var missing *ErrMissingFields
	require.True(t, errors.As(err, &missing))
	require.Equal(t, []string{"closed", "note", "trades", "ts"}, missing.Fields)

	_, err = OpenCSV(strings.NewReader("data_time,close,extra\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
	var unknown *ErrUnknownColumns
	require.True(t, errors.As(err, &unknown))
	require.Equal(t, []string{"extra"}, unknown.Columns)

	_, err = OpenCSV(strings.NewReader("close\n1\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
	require.ErrorContains(t, err, "缺少时间列")
	_, err = OpenCSV(strings.NewReader("data_time,close,series_tag\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}, SeriesTag: "x"})
	require.ErrorContains(t, err, "不能再有 series_tag 列")
	_, err = OpenCSV(strings.NewReader(""), Options{})
	require.ErrorContains(t, err, "空的")
	_, err = OpenCSV(strings.NewReader("data_time,close,close\n"), Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
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

func TestOpenCSVDecompressesGzipByMagic(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write([]byte("data_time,close\n2026-10-01,1\n"))
	require.NoError(t, gz.Close())
	r, err := OpenCSV(&buf, Options{Fields: []Field{{Name: "close", Type: TypeDouble}}})
	require.NoError(t, err)
	batch, err := r.Next(10)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, []float64{1}, batch.Cols[0].F)
}

func TestDataFileName(t *testing.T) {
	for in, want := range map[string]string{"BTCUSDT.csv": "BTCUSDT", "BTCUSDT.csv.gz": "BTCUSDT", "ETH.CSV.GZ": "ETH"} {
		got, ok := DataFileName(in)
		require.True(t, ok, in)
		require.Equal(t, want, got, in)
	}
	_, ok := DataFileName("a.txt")
	require.False(t, ok)
	_, ok = DataFileName("a.gz")
	require.False(t, ok)
}

func TestSubjectColumn(t *testing.T) {
	close := []Field{{Name: "close", Type: TypeDouble}}
	_, err := OpenCSV(strings.NewReader("data_time,subject_id,close\n"), Options{Fields: close, SeriesTag: "x"})
	require.ErrorContains(t, err, "多标的文件只能只写归档")

	r := open(t, "data_time,subject_id,close\n2026-10-01,A,1\n2026-10-01,B,2\n", Options{Fields: close, SeriesTag: "x", SubjectColumn: true})
	batch, err := r.Next(10)
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, []string{"A", "B"}, batch.Subjects)

	_, err = open(t, "data_time,subject_id,close\n2026-10-01, ,1\n", Options{Fields: close, SeriesTag: "x", SubjectColumn: true}).Next(10)
	require.ErrorContains(t, err, "subject_id 为空")

	// 单标的文件不带 Subjects。
	batch, _ = open(t, "data_time,close\n2026-10-01,1\n", Options{Fields: close, SubjectColumn: true}).Next(10)
	require.Nil(t, batch.Subjects)
}

func TestRequireSeriesTag(t *testing.T) {
	close := []Field{{Name: "close", Type: TypeDouble}}
	_, err := OpenCSV(strings.NewReader("data_time,close\n"), Options{Fields: close, RequireSeriesTag: true})
	require.ErrorContains(t, err, "series_tag 必须非空")
	_, err = OpenCSV(strings.NewReader("data_time,close\n"), Options{Fields: close, RequireSeriesTag: true, SeriesTag: "spot"})
	require.NoError(t, err)
	_, err = open(t, "data_time,close,series_tag\n2026-10-01,1,spot\n2026-10-02,1,\n", Options{Fields: close, RequireSeriesTag: true}).Next(10)
	require.ErrorContains(t, err, "第 3 行的 series_tag 为空")
}

func TestCheckerKeysBySubjectAndTag(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	multi := &Batch{Times: []time.Time{at, at, at.Add(time.Minute)}, Tags: []string{"s", "s", "s"}, Subjects: []string{"A", "B", "A"}}
	c := NewChecker(time.Minute)
	require.NoError(t, c.Check(multi), "同一时刻不同标的不算重复")
	require.Equal(t, []string{"A", "B"}, c.Finish().Subjects)
	dup := &Batch{Times: []time.Time{at, at}, Tags: []string{"s", "s"}, Subjects: []string{"A", "A"}}
	require.ErrorContains(t, NewChecker(time.Minute).Check(dup), "标的 A 的序列")
}
