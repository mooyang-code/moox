package seriessync

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/seriesstore"
	"github.com/mooyang-code/moox/modules/factor/internal/storageio"
)

// FieldsFromColumns 把数据集的字段定义转成本地存储的字段表：只取启用的、非系统的列；
// 类型 double→Float64、int→Int64、bool→Bool，其余（string、time、json、bytes）按字节串存；
// 缺失行的填充规则取列属性 fill，没有时按字段名给默认（OHLC 取上一期 close，量类填 0，其余取上一期的值）。
func FieldsFromColumns(columns []storageio.ColumnInfo) []seriesstore.Field {
	reserved := map[string]struct{}{"subject_id": {}, "freq": {}, "data_time": {}, "series_tag": {}}
	var fields []seriesstore.Field
	for _, col := range columns {
		if col.Status != "" && col.Status != storageio.ColumnStatusActive {
			continue
		}
		if col.OriginType == storageio.ColumnOriginSystem {
			continue
		}
		if _, skip := reserved[col.ColumnName]; skip {
			continue
		}
		fields = append(fields, seriesstore.Field{Name: col.ColumnName, Type: fieldType(col.ValueType), Fill: fillRule(col)})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Name < fields[j].Name })
	return fields
}

func fieldType(valueType string) seriesstore.FieldType {
	switch valueType {
	case storageio.ColumnTypeDouble:
		return seriesstore.TypeFloat64
	case storageio.ColumnTypeInt:
		return seriesstore.TypeInt64
	case storageio.ColumnTypeBool:
		return seriesstore.TypeBool
	default:
		return seriesstore.TypeString
	}
}

// ValueTypes 返回各列的数据集类型，携带列写回结果数据集时按它还原 TIME / JSON 类型。
func ValueTypes(columns []storageio.ColumnInfo) map[string]string {
	out := make(map[string]string, len(columns))
	for _, col := range columns {
		out[col.ColumnName] = col.ValueType
	}
	return out
}

// DefaultFill 是没有 fill 属性时按字段名给的填充规则。
func DefaultFill(name string) seriesstore.FillRule {
	switch strings.ToLower(name) {
	case "open", "high", "low", "close":
		return seriesstore.FillPrevClose
	case "volume", "quote_volume", "amount", "turnover", "trades", "trade_count", "num_trades", "count",
		"taker_buy_volume", "taker_buy_quote_volume", "taker_buy_base_volume":
		return seriesstore.FillZero
	}
	return seriesstore.FillPrev
}

func fillRule(col storageio.ColumnInfo) seriesstore.FillRule {
	switch strings.TrimSpace(col.Attributes["fill"]) {
	case "prev":
		return seriesstore.FillPrev
	case "prev_close":
		return seriesstore.FillPrevClose
	case "zero":
		return seriesstore.FillZero
	}
	return DefaultFill(col.ColumnName)
}

// FrameToColumns 把一个标的的帧（data_time、series_tag、按 fields 顺序的字段值）转成列式行，按（时间、标签）排序。
func FrameToColumns(fields []seriesstore.Field, frame *storageio.Frame) (*seriesstore.Columns, error) {
	out := seriesstore.NewColumns(fields, 0)
	if frame == nil {
		return out, nil
	}
	if len(frame.Columns) != len(fields)+2 {
		return nil, fmt.Errorf("帧的列数 %d 与字段数 %d 不一致", len(frame.Columns), len(fields))
	}
	for r, row := range frame.Rows {
		at, ok := row[0].(time.Time)
		if !ok {
			return nil, fmt.Errorf("第 %d 行 data_time 类型为 %T", r, row[0])
		}
		tag, _ := row[1].(string)
		values := make([]seriesstore.Value, len(fields))
		for i, f := range fields {
			v, err := toValue(f.Type, row[2+i])
			if err != nil {
				return nil, fmt.Errorf("第 %d 行字段 %s：%w", r, f.Name, err)
			}
			values[i] = v
		}
		out.AppendRow(at, tag, false, values...)
	}
	return out.SortedByKey(), nil
}

func toValue(t seriesstore.FieldType, raw any) (seriesstore.Value, error) {
	if raw == nil {
		return seriesstore.NullValue, nil
	}
	switch t {
	case seriesstore.TypeFloat64:
		switch v := raw.(type) {
		case float64:
			return seriesstore.Float(v), nil
		case int64:
			return seriesstore.Float(float64(v)), nil
		case int:
			return seriesstore.Float(float64(v)), nil
		}
	case seriesstore.TypeInt64:
		switch v := raw.(type) {
		case int64:
			return seriesstore.Int(v), nil
		case int:
			return seriesstore.Int(int64(v)), nil
		case float64:
			return seriesstore.Int(int64(v)), nil
		}
	case seriesstore.TypeBool:
		if v, ok := raw.(bool); ok {
			return seriesstore.Bool(v), nil
		}
	case seriesstore.TypeString:
		switch v := raw.(type) {
		case string:
			return seriesstore.Str(v), nil
		case storageio.TimeText:
			return seriesstore.Str(string(v)), nil
		case storageio.JSONText:
			return seriesstore.Str(string(v)), nil
		case []byte:
			return seriesstore.Str(string(v)), nil
		}
	}
	return seriesstore.Value{}, fmt.Errorf("值 %v（%T）与字段类型不符", raw, raw)
}

// ColumnsToFrame 把列式行转成按 names 顺序的帧行（携带列用）：数值还原成 float64 / int64 / bool，字符串按数据集类型还原成
// TimeText / JSONText；NULL 是 nil。types 是各列的数据集类型。
func ColumnsToFrame(subject string, c *seriesstore.Columns, names []string, types map[string]string) *storageio.Frame {
	frame := &storageio.Frame{SubjectID: subject, Columns: append([]string{"data_time", "series_tag"}, names...)}
	idx := make([]int, len(names))
	for i, n := range names {
		idx[i] = c.FieldIndex(n)
	}
	for r := 0; r < c.Len(); r++ {
		row := make([]any, 0, 2+len(names))
		row = append(row, time.Unix(c.Times[r], 0).UTC(), c.Tags[r])
		for i, fi := range idx {
			if fi < 0 {
				row = append(row, nil)
				continue
			}
			v := c.ValueAt(fi, r)
			if v.Null {
				row = append(row, nil)
				continue
			}
			switch c.Fields[fi].Type {
			case seriesstore.TypeFloat64:
				row = append(row, v.F)
			case seriesstore.TypeInt64:
				row = append(row, v.I)
			case seriesstore.TypeBool:
				row = append(row, v.I != 0)
			default:
				switch types[names[i]] {
				case storageio.ColumnTypeTime:
					row = append(row, storageio.TimeText(v.S))
				case storageio.ColumnTypeJSON:
					row = append(row, storageio.JSONText(v.S))
				case storageio.ColumnTypeBytes:
					row = append(row, []byte(v.S))
				default:
					row = append(row, v.S)
				}
			}
		}
		frame.Rows = append(frame.Rows, row)
	}
	return frame
}
