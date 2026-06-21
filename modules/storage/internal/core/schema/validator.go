package schema

import (
	"context"
	"fmt"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// MetadataReader 定义写入 Schema 校验所需的元数据读取接口。
type MetadataReader interface {
	GetDataset(ctx context.Context, spaceID string, datasetID string) (*pb.Dataset, error)
	ListDatasetColumns(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error)
}

// Validator 校验写入行是否符合 Dataset 和字段定义。
type Validator struct {
	metadata MetadataReader
}

func NewValidator(store MetadataReader) *Validator {
	return &Validator{metadata: store}
}

func (v *Validator) ValidateWriteTimeSeriesRows(ctx context.Context, rows []*pb.TimeSeriesRow) error {
	for _, row := range rows {
		key := row.GetKey()
		if key == nil {
			return fmt.Errorf("key is required")
		}
		if err := validateDatasetColumns(ctx, v.metadata, key.GetSpaceId(), key.GetDatasetId(), pb.DataKind_DATA_KIND_TIME_SERIES, row.GetColumns()); err != nil {
			return err
		}
	}
	return nil
}

func (v *Validator) ValidateWriteRecordRows(ctx context.Context, rows []*pb.RecordRow) error {
	for _, row := range rows {
		key := row.GetKey()
		if key == nil {
			return fmt.Errorf("key is required")
		}
		if err := validateDatasetColumns(ctx, v.metadata, key.GetSpaceId(), key.GetDatasetId(), pb.DataKind_DATA_KIND_RECORD, row.GetColumns()); err != nil {
			return err
		}
	}
	return nil
}

func validateDatasetColumns(ctx context.Context, metadata MetadataReader, spaceID string, datasetID string, expectedKind pb.DataKind, values []*pb.ColumnValue) error {
	if spaceID == "" || datasetID == "" {
		return fmt.Errorf("space_id and dataset_id are required")
	}
	dataset, err := metadata.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return err
	}
	if dataset == nil {
		return fmt.Errorf("dataset %s not found", datasetID)
	}
	if dataset.GetStatus() != "" && dataset.GetStatus() != "active" {
		return fmt.Errorf("dataset %s is not active", datasetID)
	}
	if dataset.GetDataKind() != pb.DataKind_DATA_KIND_UNSPECIFIED && dataset.GetDataKind() != expectedKind {
		return fmt.Errorf("dataset %s data_kind mismatch: got %s want %s", datasetID, expectedKind.String(), dataset.GetDataKind().String())
	}
	columns, _, err := metadata.ListDatasetColumns(ctx, spaceID, datasetID, nil)
	if err != nil {
		return err
	}
	allowed := make(map[string]*pb.DatasetColumn, len(columns))
	for _, column := range columns {
		if column.GetStatus() == "" || column.GetStatus() == "active" {
			allowed[column.GetColumnName()] = column
		}
	}
	for _, value := range values {
		column := allowed[value.GetColumnName()]
		if column == nil {
			return fmt.Errorf("column %s is not registered in dataset %s", value.GetColumnName(), datasetID)
		}
		if value.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED &&
			column.GetValueType() != pb.FieldValueType_FIELD_VALUE_TYPE_UNSPECIFIED &&
			value.GetValueType() != column.GetValueType() {
			return fmt.Errorf("column %s type mismatch: got %s want %s", value.GetColumnName(), value.GetValueType().String(), column.GetValueType().String())
		}
	}
	return nil
}
