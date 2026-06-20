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

func TestValidatorRejectsUnknownColumn(t *testing.T) {
	ctx := context.Background()
	meta := &fakeValidatorMetadata{
		dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "binance_spot_kline", Status: "active"},
		bindings: []*pb.DataSetSubject{{
			SpaceId:   "crypto",
			DatasetId: "binance_spot_kline",
			SubjectId: "APT-USDT",
			Status:    "active",
		}},
		columns: []*pb.DataSetColumn{{
			SpaceId:    "crypto",
			DatasetId:  "binance_spot_kline",
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Status:     "active",
		}},
	}
	validator := schema.NewValidator(meta)

	err := validator.ValidateWriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope: &pb.DataScope{
				SpaceId:   "crypto",
				DatasetId: "binance_spot_kline",
				SubjectId: "APT-USDT",
				Freq:      "1m",
			},
			DataTime: "2026-06-15T00:00:00+08:00",
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("unknown_close", 9.9),
		},
	}})

	require.ErrorContains(t, err, "column unknown_close is not registered")
}

func TestValidatorRejectsUnboundSubject(t *testing.T) {
	ctx := context.Background()
	meta := &fakeValidatorMetadata{
		dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "binance_spot_kline", Status: "active"},
		columns: []*pb.DataSetColumn{{
			SpaceId:    "crypto",
			DatasetId:  "binance_spot_kline",
			ColumnName: "close",
			ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
			Status:     "active",
		}},
	}
	validator := schema.NewValidator(meta)

	err := validator.ValidateWriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope: &pb.DataScope{
				SpaceId:   "crypto",
				DatasetId: "binance_spot_kline",
				SubjectId: "APT-USDT",
				Freq:      "1m",
			},
			DataTime: "2026-06-15T00:00:00+08:00",
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 9.9),
		},
	}})

	require.ErrorContains(t, err, "subject APT-USDT is not bound to dataset binance_spot_kline")
}

func TestValidatorAllowsPartialUpdateWhenRequiredColumnIsNotCarried(t *testing.T) {
	ctx := context.Background()
	meta := &fakeValidatorMetadata{
		dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "binance_spot_kline", Status: "active"},
		bindings: []*pb.DataSetSubject{{
			SpaceId:   "crypto",
			DatasetId: "binance_spot_kline",
			SubjectId: "APT-USDT",
			Status:    "active",
		}},
		columns: []*pb.DataSetColumn{
			{
				SpaceId:    "crypto",
				DatasetId:  "binance_spot_kline",
				ColumnName: "open",
				ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Required:   true,
				Status:     "active",
			},
			{
				SpaceId:    "crypto",
				DatasetId:  "binance_spot_kline",
				ColumnName: "close",
				ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Status:     "active",
			},
		},
	}
	validator := schema.NewValidator(meta)

	err := validator.ValidateWriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope: &pb.DataScope{
				SpaceId:   "crypto",
				DatasetId: "binance_spot_kline",
				SubjectId: "APT-USDT",
				Freq:      "1m",
			},
			DataTime: "2026-06-15T00:00:00+08:00",
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 9.9),
		},
	}})

	require.NoError(t, err)
}

func TestValidatorAllowsPartialUpdateWithoutRequiredColumns(t *testing.T) {
	ctx := context.Background()
	meta := &fakeValidatorMetadata{
		dataset: &pb.DataSet{SpaceId: "crypto", DatasetId: "binance_spot_kline", Status: "active"},
		bindings: []*pb.DataSetSubject{{
			SpaceId:   "crypto",
			DatasetId: "binance_spot_kline",
			SubjectId: "APT-USDT",
			Status:    "active",
		}},
		columns: []*pb.DataSetColumn{
			{
				SpaceId:    "crypto",
				DatasetId:  "binance_spot_kline",
				ColumnName: "open",
				ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Required:   true,
				Status:     "active",
			},
			{
				SpaceId:    "crypto",
				DatasetId:  "binance_spot_kline",
				ColumnName: "close",
				ValueType:  pb.FieldValueType_FIELD_VALUE_TYPE_DOUBLE,
				Status:     "active",
			},
		},
	}
	validator := schema.NewValidator(meta)

	err := validator.ValidateWriteRows(ctx, []*pb.DataRow{{
		Key: &pb.DataKey{
			Scope: &pb.DataScope{
				SpaceId:   "crypto",
				DatasetId: "binance_spot_kline",
				SubjectId: "APT-USDT",
				Freq:      "1m",
			},
			DataTime: "2026-06-15T00:00:00+08:00",
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("close", 9.9),
		},
	}})

	require.NoError(t, err)
}

type fakeValidatorMetadata struct {
	dataset  *pb.DataSet
	bindings []*pb.DataSetSubject
	columns  []*pb.DataSetColumn
}

func (f *fakeValidatorMetadata) GetDataSet(ctx context.Context, spaceID string, datasetID string) (*pb.DataSet, error) {
	if f.dataset.GetSpaceId() == spaceID && f.dataset.GetDatasetId() == datasetID {
		return f.dataset, nil
	}
	return nil, fmt.Errorf("dataset not found")
}

func (f *fakeValidatorMetadata) ListDataSetColumns(ctx context.Context, spaceID string, datasetID string, textIndexedOnly bool, page *pb.Page) ([]*pb.DataSetColumn, *pb.PageResult, error) {
	return f.columns, &pb.PageResult{Total: uint64(len(f.columns))}, nil
}

func (f *fakeValidatorMetadata) ListDataSetSubjectsPage(ctx context.Context, spaceID string, datasetID string, subjectID string, page *pb.Page) ([]*pb.DataSetSubject, *pb.PageResult, error) {
	var out []*pb.DataSetSubject
	for _, binding := range f.bindings {
		if binding.GetSpaceId() == spaceID && binding.GetDatasetId() == datasetID && (subjectID == "" || binding.GetSubjectId() == subjectID) {
			out = append(out, binding)
		}
	}
	return out, &pb.PageResult{Total: uint64(len(out))}, nil
}
