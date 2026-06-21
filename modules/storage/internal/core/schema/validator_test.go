package schema_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/schema"
	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestValidatorRejectsUnknownTimeSeriesColumn(t *testing.T) {
	ctx := context.Background()
	validator := schema.NewValidator(fakeValidatorMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active"},
		columns: []*pb.DatasetColumn{{
			SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
		}},
	})

	err := validator.ValidateWriteTimeSeriesRows(ctx, []*pb.TimeSeriesRow{{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("unknown_close", 9.9)},
	}})

	require.ErrorContains(t, err, "column unknown_close is not registered")
}

func TestValidatorDoesNotRequireSubjectBindingOnTimeSeriesWrite(t *testing.T) {
	ctx := context.Background()
	validator := schema.NewValidator(fakeValidatorMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active"},
		columns: []*pb.DatasetColumn{{
			SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active",
		}},
	})

	err := validator.ValidateWriteTimeSeriesRows(ctx, []*pb.TimeSeriesRow{{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 9.9)},
	}})

	require.NoError(t, err)
}

func TestValidatorAllowsPartialUpdateWhenRequiredColumnIsNotCarried(t *testing.T) {
	ctx := context.Background()
	validator := schema.NewValidator(fakeValidatorMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active"},
		columns: []*pb.DatasetColumn{
			{SpaceId: "crypto", DatasetId: "kline", ColumnName: "open", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Required: true, Status: "active"},
			{SpaceId: "crypto", DatasetId: "kline", ColumnName: "close", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE, Status: "active"},
		},
	})

	err := validator.ValidateWriteTimeSeriesRows(ctx, []*pb.TimeSeriesRow{{
		Key:     &pb.TimeSeriesKey{SpaceId: "crypto", DatasetId: "kline", SubjectId: "APT-USDT", Freq: "1m", DataTime: "2026-06-15T00:00:00Z"},
		Columns: []*pb.ColumnValue{testutil.DoubleValue("close", 9.9)},
	}})

	require.NoError(t, err)
}

func TestValidatorRejectsRecordDatasetKindMismatch(t *testing.T) {
	ctx := context.Background()
	validator := schema.NewValidator(fakeValidatorMetadata{
		dataset: &pb.Dataset{SpaceId: "crypto", DatasetId: "kline", DataKind: pb.DataKind_DATA_KIND_TIME_SERIES, Status: "active"},
		columns: []*pb.DatasetColumn{{
			SpaceId: "crypto", DatasetId: "kline", ColumnName: "name", ValueType: pb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active",
		}},
	})

	err := validator.ValidateWriteRecordRows(ctx, []*pb.RecordRow{{
		Key:     &pb.RecordKey{SpaceId: "crypto", DatasetId: "kline", RecordId: "APT-USDT"},
		Columns: []*pb.ColumnValue{testutil.StringValue("name", "APT")},
	}})

	require.ErrorContains(t, err, "data_kind mismatch")
}

// fakeValidatorMetadata 是 Schema 校验测试使用的元数据桩。
type fakeValidatorMetadata struct {
	dataset *pb.Dataset
	columns []*pb.DatasetColumn
}

func (f fakeValidatorMetadata) GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error) {
	if f.dataset.GetSpaceId() == spaceID && f.dataset.GetDatasetId() == datasetID {
		return f.dataset, nil
	}
	return nil, fmt.Errorf("dataset not found")
}

func (f fakeValidatorMetadata) ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return f.columns, &pb.PageResult{Total: uint64(len(f.columns))}, nil
}
