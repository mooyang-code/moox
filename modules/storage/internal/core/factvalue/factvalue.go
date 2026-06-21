// Package factvalue 提供跨存储设备（Pebble / DuckDB / Bleve）复用的
// TypedValue 处理、时间范围判断、过滤、排序和分页工具，消除各 device 包中重复实现。
package factvalue

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// String 返回 TypedValue 的字符串形式，用于文本比较与索引。
func String(value *pb.TypedValue) string {
	if value == nil {
		return ""
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return v.StringValue
	case *pb.TypedValue_IntValue:
		return strconv.FormatInt(v.IntValue, 10)
	case *pb.TypedValue_DoubleValue:
		return strconv.FormatFloat(v.DoubleValue, 'g', -1, 64)
	case *pb.TypedValue_BoolValue:
		return strconv.FormatBool(v.BoolValue)
	case *pb.TypedValue_TimeValue:
		return v.TimeValue
	case *pb.TypedValue_JsonValue:
		return v.JsonValue
	case *pb.TypedValue_BytesValue:
		return string(v.BytesValue)
	case *pb.TypedValue_ListValue:
		raw, _ := json.Marshal(v.ListValue)
		return string(raw)
	default:
		return ""
	}
}

// Numeric 尝试把 TypedValue 解释为浮点数，用于数值大小比较。
func Numeric(value *pb.TypedValue) (float64, bool) {
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_IntValue:
		return float64(v.IntValue), true
	case *pb.TypedValue_DoubleValue:
		return v.DoubleValue, true
	default:
		return 0, false
	}
}

// Compare 比较两个 TypedValue，优先按数值，否则按字符串。返回 -1/0/1。
func Compare(left, right *pb.TypedValue) int {
	leftNumber, leftOK := Numeric(left)
	rightNumber, rightOK := Numeric(right)
	if leftOK && rightOK {
		switch {
		case leftNumber < rightNumber:
			return -1
		case leftNumber > rightNumber:
			return 1
		default:
			return 0
		}
	}
	leftText := String(left)
	rightText := String(right)
	switch {
	case leftText < rightText:
		return -1
	case leftText > rightText:
		return 1
	default:
		return 0
	}
}

// StringSet 把字符串切片转为集合，空切片返回 nil。
func StringSet(values []string) map[string]bool {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]bool, len(values))
	for _, value := range values {
		out[value] = true
	}
	return out
}

// TimeInRange 判断时间值是否落在闭区间 [start, end] 内。
// 优先按 RFC3339 解析比较（时区安全）；解析失败时回退到字符串字典序。
func TimeInRange(value string, timeRange *pb.TimeRange) bool {
	if timeRange == nil {
		return true
	}
	valueTime, valueOK := ParseTime(value)
	if start := strings.TrimSpace(timeRange.GetStartTime()); start != "" {
		if startTime, startOK := ParseTime(start); valueOK && startOK {
			if valueTime.Before(startTime) {
				return false
			}
		} else if value < start {
			return false
		}
	}
	if end := strings.TrimSpace(timeRange.GetEndTime()); end != "" {
		if endTime, endOK := ParseTime(end); valueOK && endOK {
			if valueTime.After(endTime) {
				return false
			}
		} else if value > end {
			return false
		}
	}
	return true
}

// ParseTime 解析 RFC3339 / RFC3339Nano 时间字符串。
func ParseTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}
