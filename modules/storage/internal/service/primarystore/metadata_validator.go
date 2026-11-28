package primarystore

import (
	"errors"
	"fmt"
	"strings"

	metacache "github.com/mooyang-code/moox/modules/storage/internal/service/metadata/cache"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// validateRowShape 检查单行的基础形态：键完整、记录写带版本、至少带一个字段或属性、字段与属性键值非空。
func validateRowShape(row *pb.Row) error {
	if row == nil || row.GetKey() == nil {
		return errors.New("row key is required")
	}
	key := row.GetKey()
	if strings.TrimSpace(key.GetSpaceId()) == "" || strings.TrimSpace(key.GetDatasetId()) == "" {
		return errors.New("space_id and dataset_id are required")
	}
	if key.GetTimeSeries() == nil && key.GetRecord() == nil {
		return errors.New("row key kind is required")
	}
	if record := key.GetRecord(); record != nil && strings.TrimSpace(record.GetVersion()) == "" {
		return errors.New("record version is required for writes")
	}
	if len(row.GetFields()) == 0 && len(row.GetAttributes()) == 0 {
		return errors.New("at least one field or attribute is required")
	}
	for _, field := range row.GetFields() {
		if field == nil || strings.TrimSpace(field.GetFieldId()) == "" || field.GetValue() == nil {
			return errors.New("field_id and value are required")
		}
	}
	for name, value := range row.GetAttributes() {
		if strings.TrimSpace(name) == "" || value == nil {
			return errors.New("attribute key and value are required")
		}
	}
	return nil
}

// validateRowsSchema 使用本次请求已取出的启用数据集与快照校验整批行：
// 键形态与频率匹配数据集定义，且写入的每个字段都是该数据集里已注册并启用的列、类型一致（允许部分字段写入）。
func validateRowsSchema(snapshot *metacache.DatasetSnapshot, dataset *pb.Dataset, rows []*pb.Row) error {
	for _, row := range rows {
		if err := validateRowSchema(snapshot, dataset, row); err != nil {
			return err
		}
	}
	return nil
}

func validateRowSchema(snapshot *metacache.DatasetSnapshot, dataset *pb.Dataset, row *pb.Row) error {
	key := row.GetKey()
	if dataset.GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES && key.GetTimeSeries() == nil {
		return fmt.Errorf("dataset %q requires time-series row key", key.GetDatasetId())
	}
	if dataset.GetDataKind() == pb.DataKind_DATA_KIND_RECORD && key.GetRecord() == nil {
		return fmt.Errorf("dataset %q requires record row key", key.GetDatasetId())
	}
	// 每个 Dataset 只有一个规范频率，频率不同（含 1H 这类别名）的行一律拒绝。
	if series := key.GetTimeSeries(); series != nil && series.GetFreq() != dataset.GetFreq() {
		return fmt.Errorf("row freq %q does not match dataset %q freq %q", series.GetFreq(), key.GetDatasetId(), dataset.GetFreq())
	}
	spaceID, datasetID := dataset.GetSpaceId(), dataset.GetDatasetId()
	for _, field := range row.GetFields() {
		column, ok := snapshot.GetDatasetColumn(spaceID, datasetID, field.GetFieldId())
		if !ok || (column.GetStatus() != "" && column.GetStatus() != "active") {
			return fmt.Errorf("field %q is not registered in dataset %q", field.GetFieldId(), datasetID)
		}
		declared := column.GetValueType()
		if declared == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
			return fmt.Errorf("field %q has no declared value type", field.GetFieldId())
		}
		if !validFieldValueType(declared) {
			return fmt.Errorf("field %q has invalid declared value type %d", field.GetFieldId(), declared)
		}
		if nullValue, ok := field.GetValue().GetValue().(*pb.TypedValue_NullValue); ok {
			if nullValue.NullValue != pb.NullValue_NULL_VALUE_NULL {
				return fmt.Errorf("field %q has invalid null marker %s", field.GetFieldId(), nullValue.NullValue.String())
			}
			continue
		}
		actual := typedValueType(field.GetValue())
		if actual == pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED {
			return fmt.Errorf("field %q has no value type", field.GetFieldId())
		}
		if declared != actual {
			return fmt.Errorf("field %q type mismatch: got %s want %s", field.GetFieldId(), actual.String(), declared.String())
		}
	}
	return nil
}

func validFieldValueType(valueType pb.FieldValueType) bool {
	switch valueType {
	case pb.FieldValueType_FIELD_VALUE_TYPE_STRING,
		pb.FieldValueType_FIELD_VALUE_TYPE_INT,
		pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		pb.FieldValueType_FIELD_VALUE_TYPE_BOOL,
		pb.FieldValueType_FIELD_VALUE_TYPE_TIME,
		pb.FieldValueType_FIELD_VALUE_TYPE_JSON,
		pb.FieldValueType_FIELD_VALUE_TYPE_BYTES:
		return true
	default:
		return false
	}
}

func typedValueType(value *pb.TypedValue) pb.FieldValueType {
	if value == nil {
		return pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED
	}
	switch value.GetValue().(type) {
	case *pb.TypedValue_StringValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_STRING
	case *pb.TypedValue_IntValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_INT
	case *pb.TypedValue_DoubleValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE
	case *pb.TypedValue_BoolValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_BOOL
	case *pb.TypedValue_TimeValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_TIME
	case *pb.TypedValue_JsonValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_JSON
	case *pb.TypedValue_BytesValue:
		return pb.FieldValueType_FIELD_VALUE_TYPE_BYTES
	default:
		return pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED
	}
}
