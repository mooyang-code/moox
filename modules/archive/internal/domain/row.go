package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type Scalar struct {
	Type   storagepb.FieldValueType `json:"type"`
	Null   bool                     `json:"null,omitempty"`
	String *string                  `json:"string,omitempty"`
	Int    *int64                   `json:"int,omitempty"`
	Double *float64                 `json:"double,omitempty"`
	Bool   *bool                    `json:"bool,omitempty"`
	Time   *string                  `json:"time,omitempty"`
	JSON   *string                  `json:"json,omitempty"`
	Bytes  *[]byte                  `json:"bytes,omitempty"`
}

type RowPatch struct {
	Partition      PartitionKey      `json:"partition"`
	DataTime       time.Time         `json:"data_time"`
	DimensionsJSON string            `json:"dimensions_json"`
	Attributes     map[string]string `json:"attributes"`
	WrittenAt      time.Time         `json:"written_at"`
	Columns        map[string]Scalar `json:"columns"`
}

type EventBatch struct {
	MessageID string     `json:"message_id"`
	Rows      []RowPatch `json:"rows"`
}

type ArchiveRow struct {
	Partition      PartitionKey
	DataTime       time.Time
	DimensionsJSON string
	Attributes     map[string]string
	WrittenAt      time.Time
	Columns        map[string]Scalar
}

type Manifest struct {
	Path           string    `json:"path"`
	Generation     uint64    `json:"generation"`
	SHA256         string    `json:"sha256"`
	Size           int64     `json:"size"`
	RowCount       uint64    `json:"row_count"`
	MinTime        time.Time `json:"min_time"`
	MaxTime        time.Time `json:"max_time"`
	Columns        []string  `json:"columns"`
	MaterializedAt time.Time `json:"materialized_at"`
}

type COSState struct {
	Status      string    `json:"status"`
	Generation  uint64    `json:"generation"`
	ObjectKey   string    `json:"object_key"`
	LastAttempt time.Time `json:"last_attempt"`
	LastError   string    `json:"last_error"`
	NextRetry   time.Time `json:"next_retry"`
}

func (s Scalar) PointerCount() int {
	n := 0
	if s.String != nil {
		n++
	}
	if s.Int != nil {
		n++
	}
	if s.Double != nil {
		n++
	}
	if s.Bool != nil {
		n++
	}
	if s.Time != nil {
		n++
	}
	if s.JSON != nil {
		n++
	}
	if s.Bytes != nil {
		n++
	}
	return n
}

func CanonicalStringMap(values map[string]string) (string, error) {
	if values == nil {
		values = map[string]string{}
	}
	raw, err := json.Marshal(values)
	return string(raw), err
}

func ScalarFromField(fieldID string, typed *storagepb.TypedValue) (Scalar, error) {
	if fieldID == "" || typed == nil {
		return Scalar{}, fmt.Errorf("field and value are required")
	}
	var s Scalar
	switch value := typed.GetValue().(type) {
	case *storagepb.TypedValue_NullValue:
		if value.NullValue != storagepb.NullValue_NULL_VALUE {
			return Scalar{}, fmt.Errorf("unspecified null value for %s", fieldID)
		}
		s.Null = true
	case *storagepb.TypedValue_StringValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING
		s.String = &value.StringValue
	case *storagepb.TypedValue_IntValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_INT
		s.Int = &value.IntValue
	case *storagepb.TypedValue_DoubleValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
		s.Double = &value.DoubleValue
	case *storagepb.TypedValue_BoolValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_BOOL
		s.Bool = &value.BoolValue
	case *storagepb.TypedValue_TimeValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_TIME
		t, err := time.Parse(time.RFC3339Nano, value.TimeValue)
		if err != nil {
			return Scalar{}, fmt.Errorf("invalid time value: %w", err)
		}
		normalized := t.UTC().Format(time.RFC3339Nano)
		s.Time = &normalized
	case *storagepb.TypedValue_JsonValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_JSON
		var raw json.RawMessage
		if err := json.Unmarshal([]byte(value.JsonValue), &raw); err != nil {
			return Scalar{}, fmt.Errorf("invalid json value: %w", err)
		}
		normalized := string(raw)
		s.JSON = &normalized
	case *storagepb.TypedValue_BytesValue:
		s.Type = storagepb.FieldValueType_FIELD_VALUE_TYPE_BYTES
		copied := append([]byte(nil), value.BytesValue...)
		s.Bytes = &copied
	default:
		return Scalar{}, fmt.Errorf("unsupported value branch for %s", fieldID)
	}
	return s, nil
}

func (s Scalar) clone() Scalar {
	out := s
	if s.String != nil {
		v := *s.String
		out.String = &v
	}
	if s.Int != nil {
		v := *s.Int
		out.Int = &v
	}
	if s.Double != nil {
		v := *s.Double
		out.Double = &v
	}
	if s.Bool != nil {
		v := *s.Bool
		out.Bool = &v
	}
	if s.Time != nil {
		v := *s.Time
		out.Time = &v
	}
	if s.JSON != nil {
		v := *s.JSON
		out.JSON = &v
	}
	if s.Bytes != nil {
		v := append([]byte(nil), (*s.Bytes)...)
		out.Bytes = &v
	}
	return out
}

func MergePatch(base ArchiveRow, patch RowPatch) ArchiveRow {
	out := base
	out.DataTime = patch.DataTime
	out.Partition = patch.Partition
	out.DimensionsJSON = patch.DimensionsJSON
	out.WrittenAt = patch.WrittenAt
	out.Attributes = cloneStrings(base.Attributes)
	if out.Attributes == nil {
		out.Attributes = map[string]string{}
	}
	for k, v := range patch.Attributes {
		out.Attributes[k] = v
	}
	out.Columns = cloneScalars(base.Columns)
	if out.Columns == nil {
		out.Columns = map[string]Scalar{}
	}
	for k, v := range patch.Columns {
		if v.Null {
			delete(out.Columns, k)
			continue
		}
		out.Columns[k] = v.clone()
	}
	return out
}

func SortedColumnNames(schema map[string]storagepb.FieldValueType) []string {
	names := make([]string, 0, len(schema))
	for name := range schema {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func cloneStrings(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneScalars(in map[string]Scalar) map[string]Scalar {
	if in == nil {
		return nil
	}
	out := make(map[string]Scalar, len(in))
	for k, v := range in {
		out[k] = v.clone()
	}
	return out
}
