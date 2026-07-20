package primarystorev2

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// MetadataValidator enforces the deliberately small write contract at the
// PrimaryStore boundary: Dataset must be active, RowKey kind must match it,
// and every field must already be registered. Required-field semantics are
// intentionally absent; partial field upserts are valid.
type MetadataValidator struct {
	reader metadataReader
}

type metadataReader interface {
	GetDataset(context.Context, string, string) (*pb.Dataset, error)
	ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
}

func NewMetadataValidator(reader metadataReader) *MetadataValidator {
	return &MetadataValidator{reader: reader}
}

func (v *MetadataValidator) ValidateRow(ctx context.Context, row *pb.RowFieldUpsert) error {
	if v == nil || v.reader == nil {
		return fmt.Errorf("metadata validator is not configured")
	}
	key := row.GetKey()
	dataset, err := v.reader.GetDataset(ctx, key.GetSpaceId(), key.GetDatasetId())
	if err != nil {
		return err
	}
	if dataset == nil || (dataset.GetStatus() != "" && dataset.GetStatus() != "active") {
		return fmt.Errorf("dataset %q is not active", key.GetDatasetId())
	}
	if dataset.GetDataKind() == pb.DataKind_DATA_KIND_TIME_SERIES && key.GetTimeSeries() == nil {
		return fmt.Errorf("dataset %q requires time-series row key", key.GetDatasetId())
	}
	if dataset.GetDataKind() == pb.DataKind_DATA_KIND_RECORD && key.GetRecord() == nil {
		return fmt.Errorf("dataset %q requires record row key", key.GetDatasetId())
	}
	const pageSize = uint32(1000)
	var columns []*pb.DatasetColumn
	for pageNo := uint32(1); ; pageNo++ {
		items, page, err := v.reader.ListDatasetColumns(ctx, key.GetSpaceId(), key.GetDatasetId(), &pb.Page{Page: pageNo, Size: pageSize})
		if err != nil {
			return err
		}
		columns = append(columns, items...)
		if page == nil || !page.GetHasMore() || len(items) == 0 {
			break
		}
	}
	allowed := make(map[string]*pb.DatasetColumn, len(columns)*2)
	for _, column := range columns {
		if column == nil || (column.GetStatus() != "" && column.GetStatus() != "active") {
			continue
		}
		allowed[column.GetColumnName()] = column
		allowed[column.GetOriginId()] = column
	}
	for _, field := range row.GetFields() {
		column := allowed[field.GetFieldId()]
		if column == nil {
			return fmt.Errorf("field %q is not registered in dataset %q", field.GetFieldId(), key.GetDatasetId())
		}
		declared, actual := column.GetValueType(), typedValueType(field.GetValue())
		if declared != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && actual != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED && declared != actual {
			return fmt.Errorf("field %q type mismatch: got %s want %s", field.GetFieldId(), actual.String(), declared.String())
		}
	}
	return nil
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
