package primarystore

import (
	"context"
	"fmt"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

type pagedMetadataReader struct {
	columns []*pb.DatasetColumn
}

func (r *pagedMetadataReader) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: "d", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, nil
}

func (r *pagedMetadataReader) ListDatasetColumns(_ context.Context, _, _ string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	pageNo, size := uint32(1), uint32(1000)
	if page != nil {
		pageNo, size = page.GetPage(), page.GetSize()
	}
	start := int((pageNo - 1) * size)
	if start > len(r.columns) {
		start = len(r.columns)
	}
	end := start + int(size)
	if end > len(r.columns) {
		end = len(r.columns)
	}
	return r.columns[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint32(len(r.columns)), HasMore: end < len(r.columns)}, nil
}

func TestMetadataValidatorReadsAllDatasetColumnPages(t *testing.T) {
	columns := make([]*pb.DatasetColumn, 1001)
	for i := range columns {
		columns[i] = &pb.DatasetColumn{ColumnName: fmt.Sprintf("field_%04d", i), OriginId: fmt.Sprintf("field_%04d", i), ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active"}
	}
	validator := NewMetadataValidator(&pagedMetadataReader{columns: columns})
	err := validator.ValidateRow(context.Background(), &pb.RowFieldUpsert{
		Key: &pb.RowKey{SpaceId: "s", DatasetId: "d", Kind: &pb.RowKey_Record{Record: &pb.RecordRowKey{RecordId: "r", Version: "1"}}},
		Fields: []*pb.FieldValue{{
			FieldId: "field_1000",
			Value:   &pb.TypedValue{Value: &pb.TypedValue_StringValue{StringValue: "ok"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}
