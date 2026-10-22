package seriesformat

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() { Register(csvDecoder{}) }

// SeriesTagColumn 是文件里可选的 series_tag 列名。
const SeriesTagColumn = "series_tag"

type csvDecoder struct{}

func (csvDecoder) Format() string { return "csv" }

// Open 读取 CSV：第一行是表头，必须有时间列与数据集的全部字段，可选 series_tag 列；
// 空单元格表示该行该字段为空（NULL）。时间按 UTC 解析：没有时区的视为 UTC，带时区的转换为 UTC；
// 也接受秒或毫秒的 Unix 时间戳。
func (csvDecoder) Open(r io.Reader, opts Options) (BatchReader, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = true
	reader.LazyQuotes = true
	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("CSV 文件是空的（没有表头）")
		}
		return nil, fmt.Errorf("读取 CSV 表头：%w", err)
	}
	positions := make(map[string]int, len(header))
	for i, name := range header {
		name = strings.TrimSpace(strings.TrimPrefix(name, "\xef\xbb\xbf"))
		if _, dup := positions[name]; dup {
			return nil, fmt.Errorf("CSV 表头列 %q 重复", name)
		}
		positions[name] = i
	}
	timeName := opts.TimeColumnName()
	timePos, ok := positions[timeName]
	if !ok {
		return nil, fmt.Errorf("CSV 缺少时间列 %q", timeName)
	}
	tagPos, hasTag := positions[SeriesTagColumn]
	if hasTag && opts.SeriesTag != "" {
		return nil, fmt.Errorf("指定了统一的 series_tag，文件里不能再有 %s 列", SeriesTagColumn)
	}
	fieldPos := make([]int, len(opts.Fields))
	var missing []string
	used := map[string]struct{}{timeName: {}, SeriesTagColumn: {}}
	for i, f := range opts.Fields {
		pos, ok := positions[f.Name]
		if !ok {
			missing = append(missing, f.Name)
			continue
		}
		fieldPos[i] = pos
		used[f.Name] = struct{}{}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &ErrMissingFields{Fields: missing}
	}
	var unknown []string
	for name := range positions {
		if _, ok := used[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &ErrUnknownColumns{Columns: unknown}
	}
	return &csvReader{r: reader, opts: opts, timePos: timePos, tagPos: tagPos, hasTag: hasTag, fieldPos: fieldPos, line: 1}, nil
}

type csvReader struct {
	r        *csv.Reader
	opts     Options
	timePos  int
	tagPos   int
	hasTag   bool
	fieldPos []int
	line     int
	closed   bool
}

func (c *csvReader) Close() error { c.closed = true; return nil }

func (c *csvReader) Next(max int) (*Batch, error) {
	if c.closed {
		return nil, errClosed
	}
	batch := NewBatch(c.opts.Fields, max)
	for i := range batch.Cols {
		batch.Cols[i] = Column{}
	}
	for batch.Len() < max {
		record, err := c.r.Read()
		c.line++
		if errors.Is(err, io.EOF) {
			return batch, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("第 %d 行：%w", c.line, err)
		}
		if len(record) == 0 || (len(record) == 1 && strings.TrimSpace(record[0]) == "") {
			continue // 空行
		}
		if c.timePos >= len(record) {
			return nil, fmt.Errorf("第 %d 行没有时间列", c.line)
		}
		at, err := ParseTime(record[c.timePos])
		if err != nil {
			return nil, fmt.Errorf("第 %d 行时间 %q：%w", c.line, record[c.timePos], err)
		}
		tag := c.opts.SeriesTag
		if c.hasTag && c.tagPos < len(record) {
			tag = strings.TrimSpace(record[c.tagPos])
		}
		batch.appendRow(at, tag)
		for i, f := range c.opts.Fields {
			cell := ""
			if c.fieldPos[i] < len(record) {
				cell = record[c.fieldPos[i]]
			}
			if err := appendCell(&batch.Cols[i], f.Type, cell); err != nil {
				return nil, fmt.Errorf("第 %d 行字段 %s：%w", c.line, f.Name, err)
			}
		}
	}
	return batch, nil
}

func appendCell(col *Column, t FieldType, cell string) error {
	cell = strings.TrimSpace(cell)
	null := cell == ""
	col.Null = append(col.Null, null)
	switch t {
	case TypeDouble:
		var v float64
		if !null {
			parsed, err := strconv.ParseFloat(cell, 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) {
				return fmt.Errorf("%q 不是有效的数值", cell)
			}
			v = parsed
		}
		col.F = append(col.F, v)
	case TypeInt:
		var v int64
		if !null {
			parsed, err := strconv.ParseInt(cell, 10, 64)
			if err != nil {
				// 兼容 "12.0" 这类整数值的写法。
				f, ferr := strconv.ParseFloat(cell, 64)
				if ferr != nil || f != math.Trunc(f) || math.Abs(f) > 1<<53 {
					return fmt.Errorf("%q 不是整数", cell)
				}
				parsed = int64(f)
			}
			v = parsed
		}
		col.I = append(col.I, v)
	case TypeBool:
		var v int64
		if !null {
			switch strings.ToLower(cell) {
			case "true", "1", "t", "yes":
				v = 1
			case "false", "0", "f", "no":
				v = 0
			default:
				return fmt.Errorf("%q 不是布尔值", cell)
			}
		}
		col.I = append(col.I, v)
	case TypeTime:
		v := ""
		if !null {
			at, err := ParseTime(cell)
			if err != nil {
				return err
			}
			v = at.Format(time.RFC3339Nano)
		}
		col.S = append(col.S, v)
	default: // string、json、bytes 原样保存
		col.S = append(col.S, cell)
	}
	return nil
}

// ParseTime 把文本按 UTC 解析：RFC3339（带时区的转换为 UTC）、没有时区的 "2006-01-02 15:04:05" / "2006-01-02T15:04:05" /
// "2006-01-02"（视为 UTC），或秒、毫秒的 Unix 时间戳。
func ParseTime(text string) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, errors.New("时间为空")
	}
	if n, err := strconv.ParseInt(text, 10, 64); err == nil {
		switch {
		case n > 1e17: // 纳秒
			return time.Unix(0, n).UTC(), nil
		case n > 1e11: // 毫秒
			return time.UnixMilli(n).UTC(), nil
		case n > 0:
			return time.Unix(n, 0).UTC(), nil
		}
		return time.Time{}, fmt.Errorf("无效的 Unix 时间戳 %d", n)
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if at, err := time.Parse(layout, text); err == nil {
			return at.UTC(), nil
		}
	}
	for _, layout := range []string{"2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999", "2006-01-02 15:04", "2006-01-02"} {
		if at, err := time.ParseInLocation(layout, text, time.UTC); err == nil {
			return at.UTC(), nil
		}
	}
	return time.Time{}, errors.New("无法识别的时间格式（支持 RFC3339、2006-01-02 15:04:05、Unix 秒或毫秒）")
}
