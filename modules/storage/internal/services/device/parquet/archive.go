package parquet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	parquetgo "github.com/parquet-go/parquet-go"
)

type Manifest struct {
	RowCount    uint64
	ContentHash string
	Columns     []string
	MinTime     string
	MaxTime     string
}

type FactRow struct {
	SpaceID        string `parquet:"space_id"`
	DatasetID      string `parquet:"dataset_id"`
	SubjectID      string `parquet:"subject_id"`
	Freq           string `parquet:"freq"`
	DimensionsJSON string `parquet:"dimensions_json"`
	DataTime       string `parquet:"data_time"`
	RowID          string `parquet:"row_id"`
	ColumnName     string `parquet:"column_name"`
	ValueType      string `parquet:"value_type"`
	StringValue    string `parquet:"string_value"`
	IntValue       int64  `parquet:"int_value"`
	DoubleValue    double `parquet:"double_value"`
	BoolValue      bool   `parquet:"bool_value"`
	TimeValue      string `parquet:"time_value"`
	JSONValue      string `parquet:"json_value"`
	BytesValue     []byte `parquet:"bytes_value"`
	AttributesJSON string `parquet:"attributes_json"`
}

type double = float64

func WriteFacts(ctx context.Context, path string, rows []*pb.DataRow) (*Manifest, error) {
	_ = ctx
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	facts, manifest, err := flattenRows(rows)
	if err != nil {
		return nil, err
	}
	if err := parquetgo.WriteFile(path, facts); err != nil {
		return nil, err
	}
	hash, err := fileHash(path)
	if err != nil {
		return nil, err
	}
	manifest.ContentHash = hash
	return manifest, nil
}

func flattenRows(rows []*pb.DataRow) ([]FactRow, *Manifest, error) {
	columns := make(map[string]bool)
	var facts []FactRow
	manifest := &Manifest{}
	for _, row := range rows {
		scope := row.GetKey().GetScope()
		dimensions, err := marshalJSON(scope.GetDimensions())
		if err != nil {
			return nil, nil, err
		}
		attributes, err := marshalJSON(row.GetAttributes())
		if err != nil {
			return nil, nil, err
		}
		dataTime := row.GetKey().GetDataTime()
		if manifest.MinTime == "" || dataTime < manifest.MinTime {
			manifest.MinTime = dataTime
		}
		if manifest.MaxTime == "" || dataTime > manifest.MaxTime {
			manifest.MaxTime = dataTime
		}
		for _, column := range row.GetColumns() {
			fact := FactRow{
				SpaceID:        scope.GetSpaceId(),
				DatasetID:      scope.GetDatasetId(),
				SubjectID:      scope.GetSubjectId(),
				Freq:           scope.GetFreq(),
				DimensionsJSON: dimensions,
				DataTime:       dataTime,
				RowID:          row.GetKey().GetRowId(),
				ColumnName:     column.GetColumnName(),
				ValueType:      valueTypeName(column.GetValueType()),
				AttributesJSON: attributes,
			}
			fillValue(&fact, column.GetValue())
			facts = append(facts, fact)
			columns[column.GetColumnName()] = true
		}
	}
	manifest.RowCount = uint64(len(facts))
	manifest.Columns = sortedKeys(columns)
	return facts, manifest, nil
}

func fillValue(row *FactRow, value *pb.TypedValue) {
	switch v := value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		row.StringValue = v.StringValue
	case *pb.TypedValue_IntValue:
		row.IntValue = v.IntValue
	case *pb.TypedValue_DoubleValue:
		row.DoubleValue = v.DoubleValue
	case *pb.TypedValue_BoolValue:
		row.BoolValue = v.BoolValue
	case *pb.TypedValue_TimeValue:
		row.TimeValue = v.TimeValue
	case *pb.TypedValue_JsonValue:
		row.JSONValue = v.JsonValue
	case *pb.TypedValue_BytesValue:
		row.BytesValue = v.BytesValue
	}
}

func valueTypeName(valueType pb.FieldValueType) string {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING:
		return "string"
	case pb.FieldValueType_FIELD_VALUE_TYPE_INT:
		return "int"
	case pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE:
		return "double"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BOOL:
		return "bool"
	case pb.FieldValueType_FIELD_VALUE_TYPE_TIME:
		return "time"
	case pb.FieldValueType_FIELD_VALUE_TYPE_JSON:
		return "json"
	case pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return "bytes"
	default:
		return "unspecified"
	}
}

func marshalJSON(value any) (string, error) {
	if value == nil {
		return "{}", nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func sortedKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func fileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
