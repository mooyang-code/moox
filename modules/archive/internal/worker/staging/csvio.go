package staging

import (
	"bufio"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// 归档文件的 CSV 格式（RFC 4180，逗号分隔，换行 \n）：
//   - 表头 data_time,subject_id,series_tag,<值列…>；
//   - data_time 与时间列为 UTC RFC3339，秒以下保留到纳秒（尾随的 0 省略）；
//   - 浮点数用最短的可精确往返表示：绝对值小于 1e21 时用定点写法（不用指数），整数值带 .0（例如 2.0、1234567.0）；
//     更大的用指数写法（1e+22）；无穷写作 +Inf、-Inf（NaN 在 SQLite 里存为 NULL，导出为缺失值）；
//   - 布尔为 true / false；二进制为标准 base64；
//   - 缺失值（NULL）是空字段，空字符串写作 ""，读回时二者可以区分。
// encoding/csv 读不出字段是否带引号，所以这里自带一个最小的读写实现。

// csvField 是一个字段；null 表示缺失值（写成不带引号的空字段）。
type csvField struct {
	value string
	null  bool
}

// writeCSVRecord 写一行；需要时加引号（含逗号、引号、换行、首尾空白，或空字符串）。
func writeCSVRecord(w *bufio.Writer, fields []csvField) error {
	for i, f := range fields {
		if i > 0 {
			if err := w.WriteByte(','); err != nil {
				return err
			}
		}
		if f.null {
			continue
		}
		if f.value != "" && !strings.ContainsAny(f.value, ",\"\r\n") && strings.TrimSpace(f.value) == f.value {
			if _, err := w.WriteString(f.value); err != nil {
				return err
			}
			continue
		}
		if _, err := w.WriteString(`"` + strings.ReplaceAll(f.value, `"`, `""`) + `"`); err != nil {
			return err
		}
	}
	return w.WriteByte('\n')
}

// csvReader 读取 writeCSVRecord 写出的文件，并保留字段是否带引号。
type csvReader struct {
	r    *bufio.Reader
	line int
	// limit 大于 0 时是单条记录的字节上限（读外部文件时防止一条超长记录撑爆内存）。
	limit int
}

// MaxImportRecordBytes 是 OpenCSV 读取时单条记录的字节上限。
const MaxImportRecordBytes = 1 << 20

// ErrRecordTooLong 表示一条记录超过了字节上限。
var ErrRecordTooLong = errors.New("记录过长（超过 1MiB）")

// read 读一行；文件结束返回 io.EOF。不带引号的空字段为 null。
func (c *csvReader) read() ([]csvField, error) {
	c.line++
	var fields []csvField
	var buf strings.Builder
	quoted, inQuotes, started := false, false, false
	size := 0
	finish := func() {
		fields = append(fields, csvField{value: buf.String(), null: !quoted && buf.Len() == 0})
		buf.Reset()
		quoted = false
	}
	for {
		b, err := c.r.ReadByte()
		if err == io.EOF {
			if inQuotes {
				return nil, errors.New("引号没有闭合")
			}
			if !started {
				return nil, io.EOF
			}
			finish()
			return fields, nil
		}
		if err != nil {
			return nil, err
		}
		started = true
		if size++; c.limit > 0 && size > c.limit {
			return nil, ErrRecordTooLong
		}
		if inQuotes {
			if b != '"' {
				buf.WriteByte(b)
				continue
			}
			next, err := c.r.ReadByte()
			if err == nil && next == '"' {
				buf.WriteByte('"')
				continue
			}
			if err == nil {
				_ = c.r.UnreadByte()
			}
			inQuotes = false
			continue
		}
		switch b {
		case ',':
			finish()
		case '\n':
			finish()
			return fields, nil
		case '\r':
			// 容忍 \r\n 行尾。
		case '"':
			if buf.Len() != 0 || quoted {
				return nil, errors.New("字段中间出现引号（字段里的引号要写成两个，并把整个字段用引号括起来）")
			}
			quoted, inQuotes = true, true
		default:
			buf.WriteByte(b)
		}
	}
}

// formatTime 写时间：UTC RFC3339，秒以下保留到纳秒。
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// formatFloat 写浮点数：最短可往返表示；绝对值小于 1e21 用定点写法，整数值补 .0。
func formatFloat(v float64) string {
	if math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'g', -1, 64) // +Inf / -Inf
	}
	if math.Abs(v) >= 1e21 {
		return strconv.FormatFloat(v, 'g', -1, 64)
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// formatValue 按列类型把暂存表里扫描出的值写成字段。
func formatValue(columnType string, value any) (csvField, error) {
	if value == nil {
		return csvField{null: true}, nil
	}
	switch columnType {
	case TypeDouble:
		if v, ok := value.(float64); ok {
			return csvField{value: formatFloat(v)}, nil
		}
		if v, ok := value.(int64); ok {
			return csvField{value: formatFloat(float64(v))}, nil
		}
	case TypeInt:
		if v, ok := value.(int64); ok {
			return csvField{value: strconv.FormatInt(v, 10)}, nil
		}
	case TypeBool:
		if v, ok := value.(int64); ok {
			return csvField{value: strconv.FormatBool(v != 0)}, nil
		}
	case TypeTime:
		if v, ok := value.(int64); ok {
			return csvField{value: formatTime(fromNanos(v))}, nil
		}
	case TypeString, TypeJSON:
		switch v := value.(type) {
		case string:
			return csvField{value: v}, nil
		case []byte:
			return csvField{value: string(v)}, nil
		}
	case TypeBytes:
		switch v := value.(type) {
		case []byte:
			return csvField{value: base64.StdEncoding.EncodeToString(v)}, nil
		case string:
			return csvField{value: base64.StdEncoding.EncodeToString([]byte(v))}, nil
		}
	}
	return csvField{}, fmt.Errorf("staged value %T does not match column type %s", value, columnType)
}

// parseValue 把字段读回成绑定参数（与 bindValue 的结果一致）。
func parseValue(columnType string, f csvField) (any, error) {
	if f.null {
		return nil, nil
	}
	switch columnType {
	case TypeDouble:
		return strconv.ParseFloat(f.value, 64) // 也接受 +Inf、-Inf
	case TypeInt:
		return strconv.ParseInt(f.value, 10, 64)
	case TypeBool:
		b, err := strconv.ParseBool(f.value)
		if err != nil {
			return nil, err
		}
		if b {
			return int64(1), nil
		}
		return int64(0), nil
	case TypeTime:
		t, err := time.Parse(time.RFC3339Nano, f.value)
		if err != nil {
			return nil, err
		}
		return nanos(t), nil
	case TypeString, TypeJSON:
		return f.value, nil
	case TypeBytes:
		return base64.StdEncoding.DecodeString(f.value)
	}
	return nil, errors.New("unsupported column type " + columnType)
}

// Cell 是 CSV 的一个字段：Null 为真表示缺失值（不带引号的空字段），空字符串写作 "" 时 Null 为假。
type Cell struct {
	Value string
	Null  bool
}

// CSVReader 按归档文件的 CSV 规则逐行读取（区分缺失值与空字符串）；gzip 压缩的输入按魔数自动解压。
type CSVReader struct {
	r      *csvReader
	closer io.Closer
}

// OpenCSV 打开一个 .csv 或 .csv.gz 文件（按内容的魔数判断是否压缩）；单条记录超过 1MiB 时 Read 返回 ErrRecordTooLong。
func OpenCSV(path string) (*CSVReader, error) {
	reader, closer, err := openCSV(path)
	if err != nil {
		return nil, err
	}
	reader.limit = MaxImportRecordBytes
	return &CSVReader{r: reader, closer: closer}, nil
}

// Read 读一行；文件结束返回 io.EOF。
func (c *CSVReader) Read() ([]Cell, error) {
	fields, err := c.r.read()
	if err != nil {
		return nil, err
	}
	out := make([]Cell, len(fields))
	for i, f := range fields {
		out[i] = Cell{Value: f.value, Null: f.null}
	}
	return out, nil
}

// Line 返回最近一次 Read 读到的记录序号（从 1 起，表头是第 1 行；字段里的换行不计）。
func (c *CSVReader) Line() int { return c.r.line }

// Close 关闭文件。
func (c *CSVReader) Close() error { return c.closer.Close() }

// ParseCell 按列类型解析一个字段，返回 Stage 所需的值（与 Row.Values 一致）：时间为 time.Time、布尔为 bool，
// 缺失值为 nil；时间超出可存储范围时报错。与读回归档文件用同一套解析规则（parseValue），再把绑定值转回 Go 值。
func ParseCell(columnType string, c Cell) (any, error) {
	if columnType == TypeTime && !c.Null {
		t, err := time.Parse(time.RFC3339Nano, c.Value)
		if err != nil {
			return nil, fmt.Errorf("不是 RFC3339 时间: %q", c.Value)
		}
		if err := CheckNanosRange(t); err != nil {
			return nil, err
		}
		return t.UTC(), nil
	}
	bound, err := parseValue(columnType, csvField{value: c.Value, null: c.Null})
	if err != nil {
		return nil, fmt.Errorf("不是合法的 %s 值: %q", columnType, c.Value)
	}
	return unbindValue(columnType, bound), nil
}

// unbindValue 把绑定参数转回 Stage 所需的 Go 值（bindValue 的逆）：布尔 0/1 → bool，时间纳秒 → time.Time。
func unbindValue(columnType string, bound any) any {
	switch v := bound.(type) {
	case int64:
		switch columnType {
		case TypeBool:
			return v != 0
		case TypeTime:
			return fromNanos(v)
		}
	}
	return bound
}
