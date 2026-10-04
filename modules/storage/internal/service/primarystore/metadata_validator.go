package primarystore

import (
	"context"
	"fmt"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
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
	return v.validateRow(ctx, row, v.snapshotReader(ctx))
}

func (v *MetadataValidator) ValidateRows(ctx context.Context, rows []*pb.RowFieldUpsert) error {
	reader := v.snapshotReader(ctx)
	for _, row := range rows {
		if err := v.validateRow(ctx, row, reader); err != nil {
			return err
		}
	}
	return nil
}

// snapshotReader prefers the request-scoped snapshot so validation and routing
// share one immutable metadata view for the whole write.
func (v *MetadataValidator) snapshotReader(ctx context.Context) metadataReader {
	if snapshot := metadata.RequestSnapshotFromContext(ctx); snapshot != nil {
		return requestSnapshotReader{snapshot: snapshot}
	}
	if provider, ok := v.reader.(interface {
		SnapshotReader() metadata.SnapshotReader
	}); ok {
		if snapshot := provider.SnapshotReader(); snapshot != nil {
			return snapshot
		}
	}
	return v.reader
}

// requestSnapshotReader keeps validation on the same immutable generation as
// routing. It deliberately has no access to the persistent metadata Store.
type requestSnapshotReader struct {
	snapshot metadata.RequestSnapshot
}

func (r requestSnapshotReader) GetDataset(_ context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	if r.snapshot == nil {
		return nil, fmt.Errorf("metadata snapshot is unavailable")
	}
	item, ok := r.snapshot.GetDataset(spaceID, datasetID)
	if !ok {
		return nil, fmt.Errorf("dataset %q is not found", datasetID)
	}
	return item, nil
}

func (r requestSnapshotReader) ListDatasetColumns(_ context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	if r.snapshot == nil {
		return nil, nil, fmt.Errorf("metadata snapshot is unavailable")
	}
	return r.snapshot.ListDatasetColumns(spaceID, datasetID, page)
}

func (v *MetadataValidator) RequestSnapshot() metadata.RequestSnapshot {
	if provider, ok := v.reader.(interface {
		RequestSnapshot() metadata.RequestSnapshot
	}); ok {
		return provider.RequestSnapshot()
	}
	return nil
}

func (v *MetadataValidator) validateRow(ctx context.Context, row *pb.RowFieldUpsert, reader metadataReader) error {
	if v == nil || reader == nil {
		return fmt.Errorf("metadata validator is not configured")
	}
	key := row.GetKey()
	dataset, err := reader.GetDataset(ctx, key.GetSpaceId(), key.GetDatasetId())
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
		items, page, err := reader.ListDatasetColumns(ctx, key.GetSpaceId(), key.GetDatasetId(), &pb.Page{Page: pageNo, Size: pageSize})
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
