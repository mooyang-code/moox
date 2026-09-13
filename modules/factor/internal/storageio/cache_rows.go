package storageio

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// decodeCacheRows only decodes full-column responses. Scope, source contract
// and coverage must be validated separately before these rows can be cached.
func decodeCacheRows(columns []*pb.ResultColumn, source []*pb.TimeSeriesRow) ([]inputcache.Column, [][]any, error) {
	types := map[pb.FieldValueType]string{
		pb.FieldValueType_FIELD_VALUE_TYPE_STRING: "VARCHAR", pb.FieldValueType_FIELD_VALUE_TYPE_INT: "BIGINT",
		pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE: "DOUBLE", pb.FieldValueType_FIELD_VALUE_TYPE_BOOL: "BOOLEAN",
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME: "TIMESTAMP_NS", pb.FieldValueType_FIELD_VALUE_TYPE_JSON: "JSON",
		pb.FieldValueType_FIELD_VALUE_TYPE_BYTES: "BLOB",
	}
	keys := map[string]string{"subject_id": "VARCHAR", "freq": "VARCHAR", "data_time": "TIMESTAMP_NS", "series_tag": "VARCHAR"}
	schema := make([]inputcache.Column, len(columns))
	positions := make(map[string]int, len(columns))
	seenNames := make(map[string]bool, len(columns))
	for i, column := range columns {
		name := column.GetColumnName()
		kind, ok := types[column.GetValueType()]
		if !ok || name == "" || strings.ContainsRune(name, 0) || strings.HasPrefix(strings.ToLower(name), "__moox_cache_") || seenNames[strings.ToLower(name)] {
			return nil, nil, fmt.Errorf("invalid full cache column %q", name)
		}
		seenNames[strings.ToLower(name)] = true
		positions[name] = i
		schema[i] = inputcache.Column{Name: name, Type: kind}
	}
	for key, kind := range keys {
		index, ok := positions[key]
		if !ok || schema[index].Type != kind {
			return nil, nil, fmt.Errorf("full cache schema requires key %q of type %s", key, kind)
		}
	}
	rows := make([][]any, 0, len(source))
	seenRows := map[struct{ subject, freq, tag, at string }]bool{}
	for _, row := range source {
		key := row.GetKey()
		at, err := cacheTime(key.GetDataTime())
		if err != nil || key.GetSubjectId() == "" || key.GetFreq() == "" {
			return nil, nil, fmt.Errorf("invalid full cache row key")
		}
		identity := struct{ subject, freq, tag, at string }{key.GetSubjectId(), key.GetFreq(), key.GetSeriesTag(), at.Format(time.RFC3339Nano)}
		if seenRows[identity] {
			return nil, nil, fmt.Errorf("duplicate full cache row")
		}
		seenRows[identity] = true
		values := make([]any, len(schema))
		values[positions["subject_id"]], values[positions["freq"]] = key.GetSubjectId(), key.GetFreq()
		values[positions["data_time"]], values[positions["series_tag"]] = at, key.GetSeriesTag()
		seen := map[string]bool{}
		for _, field := range row.GetFields() {
			name := field.GetFieldId()
			index, exists := positions[name]
			if !exists || keys[name] != "" || seen[name] {
				return nil, nil, fmt.Errorf("unknown or duplicate full cache field %q", name)
			}
			seen[name] = true
			value, err := cacheFieldValue(schema[index].Type, field.GetValue())
			if err != nil {
				return nil, nil, fmt.Errorf("cache field %q: %w", name, err)
			}
			values[index] = value
		}
		if len(seen) != len(schema)-len(keys) {
			return nil, nil, fmt.Errorf("full cache row is missing fields")
		}
		rows = append(rows, values)
	}
	return schema, rows, nil
}

func cacheTime(raw string) (time.Time, error) {
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !at.Equal(time.Unix(0, at.UnixNano())) {
		return time.Time{}, fmt.Errorf("invalid cache nanosecond timestamp %q", raw)
	}
	return at.UTC(), nil
}

func cacheFieldValue(kind string, value *pb.TypedValue) (any, error) {
	if null, ok := value.GetValue().(*pb.TypedValue_NullValue); ok && null.NullValue == pb.NullValue_NULL_VALUE_NULL {
		return nil, nil
	}
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		if kind == "VARCHAR" || (kind == "JSON" && json.Valid([]byte(v.StringValue))) {
			return v.StringValue, nil
		}
	case *pb.TypedValue_JsonValue:
		if kind == "JSON" && json.Valid([]byte(v.JsonValue)) {
			return v.JsonValue, nil
		}
	case *pb.TypedValue_IntValue:
		if kind == "BIGINT" {
			return v.IntValue, nil
		}
	case *pb.TypedValue_DoubleValue:
		if kind == "DOUBLE" {
			return v.DoubleValue, nil
		}
	case *pb.TypedValue_BoolValue:
		if kind == "BOOLEAN" {
			return v.BoolValue, nil
		}
	case *pb.TypedValue_TimeValue:
		if kind == "TIMESTAMP_NS" {
			return cacheTime(v.TimeValue)
		}
	case *pb.TypedValue_BytesValue:
		if kind == "BLOB" {
			return append([]byte{}, v.BytesValue...), nil
		}
	}
	return nil, fmt.Errorf("missing or mismatched value for %s", kind)
}
