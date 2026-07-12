package schema

import (
	"context"
	"strings"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

func TestValidateWriteRecordRowsRequiresTimestampVersion(t *testing.T) {
	validator := NewValidator(recordVersionMetadata{})
	row := &pb.RecordRow{Key: &pb.RecordKey{
		SpaceId: "crypto", DatasetId: "news", RecordId: "news-1", Version: "opaque-version",
	}}
	if err := validator.ValidateWriteRecordRows(context.Background(), []*pb.RecordRow{row}); err == nil || !strings.Contains(err.Error(), "RFC3339") {
		t.Fatalf("opaque version error = %v, want RFC3339 validation", err)
	}

	row.Key.Version = "2026-07-10T12:00:00Z"
	if err := validator.ValidateWriteRecordRows(context.Background(), []*pb.RecordRow{row}); err != nil {
		t.Fatalf("timestamp version: %v", err)
	}
}

type recordVersionMetadata struct{}

func (recordVersionMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: "news", DataKind: pb.DataKind_DATA_KIND_RECORD, Status: "active"}, nil
}

func (recordVersionMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func TestValidateWriteTimeSeriesRowsRequiresKey(t *testing.T) {
	validator := NewValidator(recordVersionMetadata{})
	err := validator.ValidateWriteTimeSeriesRows(context.Background(), []*pb.TimeSeriesRow{{}})
	if err == nil || !strings.Contains(err.Error(), "key is required") {
		t.Fatalf("missing key error = %v, want key is required", err)
	}
}

func TestValidateWriteTimeSeriesRowsRejectsUnknownColumn(t *testing.T) {
	validator := NewValidator(timeSeriesColumnMetadata{})
	row := &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId: "crypto", DatasetId: "kline", SubjectId: "BTC", Freq: "1m", DataTime: "2026-07-10T12:00:00Z",
		},
		Columns: []*pb.ColumnValue{{
			ColumnName: "unknown_metric",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		}},
	}
	err := validator.ValidateWriteTimeSeriesRows(context.Background(), []*pb.TimeSeriesRow{row})
	if err == nil || !strings.Contains(err.Error(), "unknown_metric") {
		t.Fatalf("unknown column error = %v", err)
	}
}

type timeSeriesColumnMetadata struct{}

func (timeSeriesColumnMetadata) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return &pb.Dataset{DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active"}, nil
}

func (timeSeriesColumnMetadata) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return []*pb.DatasetColumn{{
		ColumnName: "close",
		ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
		Status:     "active",
	}}, &pb.PageResult{}, nil
}
