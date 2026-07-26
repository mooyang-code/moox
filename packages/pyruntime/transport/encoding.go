package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
)

type Encoding string

const (
	ArrowMMap Encoding = "arrow_mmap"
)

type Table struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// EncodeArrowFile writes Arrow IPC file bytes. Unlike a stream, the file has a
// footer and can be opened with ipc.NewMappedFileReader for shared read-only
// access by multiple workers.
func EncodeArrowFile(t Table) ([]byte, error) {
	record, schema, release, err := tableRecord(t)
	if err != nil {
		return nil, err
	}
	defer release()
	var buf bytes.Buffer
	w, err := ipc.NewFileWriter(&buf, ipc.WithSchema(schema), ipc.WithAllocator(memory.DefaultAllocator))
	if err != nil {
		return nil, fmt.Errorf("create arrow file writer: %w", err)
	}
	if err := w.Write(record); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("encode arrow file: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close arrow file: %w", err)
	}
	return buf.Bytes(), nil
}

type columnKind uint8

const (
	kindNull columnKind = iota
	kindInt64
	kindFloat64
	kindBool
	kindString
	kindTimestamp
)

func tableRecord(t Table) (arrow.RecordBatch, *arrow.Schema, func(), error) {
	if len(t.Columns) == 0 {
		if len(t.Rows) != 0 {
			return nil, nil, nil, fmt.Errorf("table has rows but no columns")
		}
		return array.NewRecordBatch(arrow.NewSchema(nil, nil), nil, 0), arrow.NewSchema(nil, nil), func() {}, nil
	}
	for i, row := range t.Rows {
		if len(row) != len(t.Columns) {
			return nil, nil, nil, fmt.Errorf("row %d has %d values, want %d", i, len(row), len(t.Columns))
		}
	}
	kinds := make([]columnKind, len(t.Columns))
	fields := make([]arrow.Field, len(t.Columns))
	cols := make([]arrow.Array, len(t.Columns))
	for col := range t.Columns {
		kind, err := inferKind(t.Rows, col)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("column %q: %w", t.Columns[col], err)
		}
		kinds[col] = kind
		fields[col] = arrow.Field{Name: t.Columns[col], Type: kindType(kind), Nullable: true}
		arr, err := buildColumn(t.Rows, col, kind)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("column %q: %w", t.Columns[col], err)
		}
		cols[col] = arr
	}
	schema := arrow.NewSchema(fields, nil)
	record := array.NewRecordBatch(schema, cols, int64(len(t.Rows)))
	for _, col := range cols {
		col.Release()
	}
	return record, schema, record.Release, nil
}

func inferKind(rows [][]any, col int) (columnKind, error) {
	kind := kindNull
	for _, row := range rows {
		if row[col] == nil {
			continue
		}
		valueKind := valueKindOf(row[col])
		if valueKind == kindNull {
			return kindNull, fmt.Errorf("unsupported value type %T", row[col])
		}
		if kind == kindNull {
			kind = valueKind
			continue
		}
		if kind == valueKind {
			continue
		}
		if (kind == kindInt64 && valueKind == kindFloat64) || (kind == kindFloat64 && valueKind == kindInt64) {
			kind = kindFloat64
			continue
		}
		return kindNull, fmt.Errorf("mixed types %T and %s", row[col], kindName(kind))
	}
	return kind, nil
}

func valueKindOf(v any) columnKind {
	switch value := v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return kindInt64
	case json.Number:
		if _, err := value.Int64(); err == nil {
			return kindInt64
		}
		if _, err := value.Float64(); err == nil {
			return kindFloat64
		}
		return kindNull
	case float32, float64:
		return kindFloat64
	case bool:
		return kindBool
	case string:
		return kindString
	case time.Time:
		return kindTimestamp
	default:
		return kindNull
	}
}

func kindType(kind columnKind) arrow.DataType {
	switch kind {
	case kindInt64:
		return arrow.PrimitiveTypes.Int64
	case kindFloat64:
		return arrow.PrimitiveTypes.Float64
	case kindBool:
		return arrow.FixedWidthTypes.Boolean
	case kindTimestamp:
		return &arrow.TimestampType{Unit: arrow.Millisecond, TimeZone: "UTC"}
	case kindNull:
		return arrow.Null
	default:
		return arrow.BinaryTypes.String
	}
}

func kindName(kind columnKind) string {
	switch kind {
	case kindInt64:
		return "int64"
	case kindFloat64:
		return "float64"
	case kindBool:
		return "bool"
	case kindString:
		return "string"
	case kindTimestamp:
		return "timestamp"
	default:
		return "null"
	}
}

func buildColumn(rows [][]any, col int, kind columnKind) (arrow.Array, error) {
	switch kind {
	case kindNull:
		return array.NewNull(len(rows)), nil
	case kindInt64:
		b := array.NewInt64Builder(memory.DefaultAllocator)
		defer b.Release()
		for _, row := range rows {
			if row[col] == nil {
				b.AppendNull()
				continue
			}
			v, ok := toInt64(row[col])
			if !ok {
				return nil, fmt.Errorf("%T is not an integer", row[col])
			}
			b.Append(v)
		}
		return b.NewArray(), nil
	case kindFloat64:
		b := array.NewFloat64Builder(memory.DefaultAllocator)
		defer b.Release()
		for _, row := range rows {
			if row[col] == nil {
				b.AppendNull()
				continue
			}
			v, ok := toFloat64(row[col])
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("%T is not a finite number", row[col])
			}
			b.Append(v)
		}
		return b.NewArray(), nil
	case kindBool:
		b := array.NewBooleanBuilder(memory.DefaultAllocator)
		defer b.Release()
		for _, row := range rows {
			if row[col] == nil {
				b.AppendNull()
				continue
			}
			v, ok := row[col].(bool)
			if !ok {
				return nil, fmt.Errorf("%T is not bool", row[col])
			}
			b.Append(v)
		}
		return b.NewArray(), nil
	case kindString:
		b := array.NewStringBuilder(memory.DefaultAllocator)
		defer b.Release()
		for _, row := range rows {
			if row[col] == nil {
				b.AppendNull()
				continue
			}
			v, ok := row[col].(string)
			if !ok {
				return nil, fmt.Errorf("%T is not string", row[col])
			}
			b.Append(v)
		}
		return b.NewArray(), nil
	case kindTimestamp:
		b := array.NewTimestampBuilder(memory.DefaultAllocator, &arrow.TimestampType{Unit: arrow.Millisecond, TimeZone: "UTC"})
		defer b.Release()
		for _, row := range rows {
			if row[col] == nil {
				b.AppendNull()
				continue
			}
			v, ok := row[col].(time.Time)
			if !ok {
				return nil, fmt.Errorf("%T is not time.Time", row[col])
			}
			b.Append(arrow.Timestamp(v.UnixMilli()))
		}
		return b.NewArray(), nil
	default:
		return nil, fmt.Errorf("unsupported column kind %d", kind)
	}
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	case uint:
		if uint64(n) > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case uint8:
		return int64(n), true
	case uint16:
		return int64(n), true
	case uint32:
		return int64(n), true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	default:
		return 0, false
	}
}

func toFloat64(v any) (float64, bool) {
	if i, ok := toInt64(v); ok {
		return float64(i), true
	}
	switch n := v.(type) {
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		number, err := n.Float64()
		return number, err == nil
	default:
		return 0, false
	}
}
