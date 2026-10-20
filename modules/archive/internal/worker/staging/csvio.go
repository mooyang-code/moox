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
//   - 浮点数用最短的可精确往返表示，整数值带 .0（例如 2.0），NaN/Inf 写作 NaN、+Inf、-Inf；
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
}

// read 读一行；文件结束返回 io.EOF。不带引号的空字段为 null。
func (c *csvReader) read() ([]csvField, error) {
	c.line++
	var fields []csvField
	var buf strings.Builder
	quoted, inQuotes, started := false, false, false
	finish := func() {
		fields = append(fields, csvField{value: buf.String(), null: !quoted && buf.Len() == 0})
		buf.Reset()
		quoted = false
	}
	for {
		b, err := c.r.ReadByte()
		if err == io.EOF {
			if inQuotes {
				return nil, fmt.Errorf("line %d: unterminated quoted field", c.line)
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
				return nil, fmt.Errorf("line %d: unexpected quote inside a field", c.line)
			}
			quoted, inQuotes = true, true
		default:
			buf.WriteByte(b)
		}
	}
}

// formatTime 写时间：UTC RFC3339，秒以下保留到纳秒。
func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// formatFloat 写浮点数：最短可往返表示，整数值补 .0。
func formatFloat(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if !strings.ContainsAny(s, ".e") {
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
		switch f.value {
		case "NaN":
			return math.NaN(), nil
		case "+Inf":
			return math.Inf(1), nil
		case "-Inf":
			return math.Inf(-1), nil
		}
		return strconv.ParseFloat(f.value, 64)
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
